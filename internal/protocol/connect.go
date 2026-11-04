package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"github.com/coder/websocket"
)

type ConnectOptions struct {
	URL           string
	TokenFile     string
	MaxFrameBytes int64
}

func Connect(ctx context.Context, o ConnectOptions, streams IO) error {
	u, err := url.Parse(o.URL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid WebSocket endpoint")
	}
	info, err := os.Lstat(o.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 65536 {
		return errors.New("private token file unavailable")
	}
	tokenBytes, err := os.ReadFile(o.TokenFile)
	if err != nil {
		return errors.New("private token file unreadable")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return errors.New("invalid bearer token")
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, response, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}, CompressionMode: websocket.CompressionDisabled})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return errors.New("WebSocket connection failed")
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(o.MaxFrameBytes)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseNow(); _ = streams.In.Close() })
	defer stop()
	var clientEOF atomic.Bool
	readDone := make(chan error, 1)
	go func() {
		for {
			kind, payload, err := conn.Read(ctx)
			if err != nil {
				if clientEOF.Load() {
					readDone <- nil
				} else {
					readDone <- errors.New("WebSocket closed before clean client EOF")
				}
				return
			}
			if kind != websocket.MessageText || !json.Valid(payload) || bytes.ContainsAny(payload, "\r\n") {
				readDone <- errors.New("WebSocket message is not one JSON line")
				return
			}
			if _, err := streams.Out.Write(append(payload, '\n')); err != nil {
				readDone <- errors.New("WebSocket output forwarding failed")
				return
			}
		}
	}()
	writeDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(streams.In, 64<<10)
		var line []byte
		for {
			part, err := reader.ReadSlice('\n')
			line = append(line, part...)
			if int64(len(line)) > o.MaxFrameBytes+1 {
				writeDone <- errors.New("client JSON line exceeds frame limit")
				return
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err == io.EOF && len(line) == 0 {
				clientEOF.Store(true)
				// Like an SDK remote Client.Close, EOF closes the connection we
				// own; it does not require the server to acknowledge shutdown or
				// send any shutdown RPC to the service.
				if err := conn.CloseNow(); err != nil {
					writeDone <- errors.New("WebSocket connection close failed")
				} else {
					writeDone <- nil
				}
				return
			}
			if err != nil || len(line) == 0 || line[len(line)-1] != '\n' {
				writeDone <- errors.New("client JSON line is incomplete")
				return
			}
			line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
			if !json.Valid(line) {
				writeDone <- errors.New("client line is not JSON")
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, line); err != nil {
				writeDone <- errors.New("WebSocket input forwarding failed")
				return
			}
			line = nil
		}
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			cancel()
		}
		return errors.Join(err, <-readDone)
	case err := <-readDone:
		if !clientEOF.Load() || err != nil {
			cancel()
		}
		return errors.Join(err, <-writeDone)
	}
}
