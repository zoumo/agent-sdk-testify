package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type mockRemoteService struct {
	endpoint    remoteEndpoint
	server      *http.Server
	cancel      context.CancelFunc
	done        chan error
	served      chan error
	once        sync.Once
	mu          sync.Mutex
	closing     bool
	connections int
	active      map[*websocket.Conn]bool
	handlers    sync.WaitGroup
	err         error
}

// The runner owns the offline service and its protocol children. Drivers see
// only the endpoint and SDK operations; each connection uses its supplied peer.
func startMockRemote(parent context.Context, main, probe command, root string) (*mockRemoteService, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		listener.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	service := &mockRemoteService{endpoint: remoteEndpoint{URL: "ws://" + listener.Addr().String(), BearerToken: hex.EncodeToString(secret[:])}, cancel: cancel, done: make(chan error, 2), served: make(chan error, 1), active: map[*websocket.Conn]bool{}}
	peers := []command{main, probe}
	service.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+service.endpoint.BearerToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		service.mu.Lock()
		if service.closing {
			service.mu.Unlock()
			w.WriteHeader(http.StatusGone)
			return
		}
		index := service.connections
		if index >= len(peers) {
			service.err = errors.Join(service.err, errors.New("unexpected remote connection"))
			service.mu.Unlock()
			w.WriteHeader(http.StatusGone)
			return
		}
		service.connections++
		service.handlers.Add(1)
		service.mu.Unlock()
		defer service.handlers.Done()
		conn, e := websocket.Accept(w, r, nil)
		if e != nil {
			service.done <- e
			return
		}
		defer conn.CloseNow()
		service.mu.Lock()
		service.active[conn] = true
		closing := service.closing
		service.mu.Unlock()
		defer func() { service.mu.Lock(); delete(service.active, conn); service.mu.Unlock() }()
		if closing {
			conn.CloseNow()
		}
		service.done <- forwardPeerSocket(ctx, conn, peers[index], filepath.Join(root, fmt.Sprintf("phase-%d-stderr.private", index)))
	})}
	go func() { service.served <- service.server.Serve(listener) }()
	return service, nil
}

func (s *mockRemoteService) close() error {
	s.once.Do(func() {
		defer s.cancel()
		s.mu.Lock()
		s.closing = true
		count := s.connections
		runErr := s.err
		s.mu.Unlock()
		runErr = errors.Join(runErr, s.server.Close())
		if err := <-s.served; !errors.Is(err, http.ErrServerClosed) {
			runErr = errors.Join(runErr, err)
		}
		complete := make(chan struct{})
		go func() { s.handlers.Wait(); close(complete) }()
		abort := func() {
			s.cancel()
			s.mu.Lock()
			for conn := range s.active {
				conn.CloseNow()
			}
			s.mu.Unlock()
		}
		if count != 2 {
			runErr = errors.Join(runErr, fmt.Errorf("remote case opened %d/2 required connections", count))
			abort()
		}
		select {
		case <-complete:
		case <-time.After(3 * time.Second):
			runErr = errors.Join(runErr, errors.New("remote peer did not drain before cleanup"))
			abort()
			select {
			case <-complete:
			case <-time.After(3 * time.Second):
				runErr = errors.Join(runErr, errors.New("remote peer cleanup did not complete"))
			}
		}
		for range count {
			select {
			case err := <-s.done:
				runErr = errors.Join(runErr, err)
			default:
				runErr = errors.Join(runErr, errors.New("remote peer completion unavailable"))
			}
		}
		s.err = runErr
	})
	return s.err
}

func forwardPeerSocket(ctx context.Context, conn *websocket.Conn, peer command, stderrPath string) error {
	cmd := exec.CommandContext(ctx, peer.Command, peer.Args...)
	cmd.Env = []string{}
	cmd.WaitDelay = 3 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancellation owns the entire peer tree, including the lifetime wrapper
	// and mock process. Killing only the wrapper would orphan the mock.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	// A descendant can retain stdout after the direct peer exits. Cancellation
	// must unblock pipe readers before Wait can reap the direct peer; waiting
	// for natural EOF here can otherwise strand a zombie until cleanup times out.
	canceled := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(canceled)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = input.Close()
		_ = output.Close()
		_ = conn.CloseNow()
	})
	defer func() {
		if !stopCancel() {
			<-canceled
		}
	}()
	readDone := make(chan error, 1)
	go func() {
		for {
			kind, frame, err := conn.Read(ctx)
			if err != nil {
				readDone <- nil
				return
			}
			if kind != websocket.MessageText || bytes.ContainsAny(frame, "\r\n") {
				readDone <- errors.New("invalid SDK WebSocket frame")
				return
			}
			if _, err := input.Write(append(frame, '\n')); err != nil {
				readDone <- err
				return
			}
		}
	}()
	writeDone := make(chan error, 1)
	go func() {
		scan := bufio.NewScanner(output)
		scan.Buffer(make([]byte, 4096), 4<<20)
		for scan.Scan() {
			if err := conn.Write(ctx, websocket.MessageText, scan.Bytes()); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- scan.Err()
	}()
	var inputErr, outputErr error
	select {
	case inputErr = <-readDone:
		_ = input.Close()
		outputErr = <-writeDone
	case outputErr = <-writeDone:
		_ = conn.CloseNow()
		inputErr = <-readDone
		_ = input.Close()
	}
	processErr := cmd.Wait()
	logErr := os.WriteFile(stderrPath, stderr.Bytes(), 0600)
	return errors.Join(inputErr, outputErr, processErr, logErr)
}
