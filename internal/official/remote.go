package official

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Only the explicit record path calls this. SDK drivers own client behavior;
// Go owns the native service, token and connection proxy processes.
type remoteService struct {
	cmd                   *exec.Cmd
	done                  chan error
	log                   *os.File
	root, endpoint, token string
}

func startRemote(ctx context.Context, o Options, plan tool.Object) (*remoteService, error) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, err
	}
	r := &remoteService{root: tool.Str(plan["root"]), endpoint: "ws://" + address, done: make(chan error, 1)}
	r.token = filepath.Join(r.root, "remote-token.private")
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return nil, err
	}
	if err := os.WriteFile(r.token, []byte(hex.EncodeToString(secret[:])), 0o600); err != nil {
		return nil, err
	}
	r.log, err = os.OpenFile(filepath.Join(r.root, "remote-server-stderr.private"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		os.Remove(r.token)
		return nil, err
	}
	var args []string
	for _, arg := range tool.Strings(plan["args"]) {
		if arg != "--stdio" {
			args = append(args, arg)
		}
	}
	args = append(args, "--listen", r.endpoint, "--ws-auth", "capability-token", "--ws-token-file", r.token)
	r.cmd = exec.CommandContext(context.WithoutCancel(ctx), tool.Str(plan["cli"]), args...)
	r.cmd.Dir = tool.Str(plan["project"])
	r.cmd.Stdout = r.log
	r.cmd.Stderr = r.log
	r.cmd.Env, err = driverEnvironment(o, plan)
	if err == nil {
		err = r.cmd.Start()
	}
	if err != nil {
		r.log.Close()
		os.Remove(r.token)
		return nil, err
	}
	go func() { r.done <- r.cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case e := <-r.done:
			r.log.Close()
			os.Remove(r.token)
			return nil, fmt.Errorf("native remote service ended before readiness: %w", e)
		case <-ctx.Done():
			_ = r.close()
			return nil, ctx.Err()
		case <-ticker.C:
			var dialer net.Dialer
			dialer.Timeout = 100 * time.Millisecond
			conn, e := dialer.DialContext(ctx, "tcp", address)
			if e == nil {
				_ = conn.Close()
				return r, nil
			}
		}
	}
}

func (r *remoteService) close() error {
	defer r.log.Close()
	defer os.Remove(r.token)
	select {
	case err := <-r.done:
		return fmt.Errorf("native remote service ended before owned cleanup: %w", err)
	default:
	}
	_ = r.cmd.Process.Signal(os.Interrupt)
	select {
	case err := <-r.done:
		_ = tool.WriteJSON(filepath.Join(r.root, "remote-service.json"), tool.Object{"cleanupComplete": err == nil, "serverExitCode": r.cmd.ProcessState.ExitCode(), "forcedCleanup": false})
		return err
	case <-time.After(3 * time.Second):
		_ = r.cmd.Process.Signal(syscall.SIGKILL)
		<-r.done
		return errors.New("native remote service required forced cleanup")
	}
}
