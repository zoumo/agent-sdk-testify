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
	"strings"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Only the explicit record path calls this. SDK drivers own client behavior;
// Go owns the native service, token and connection proxy processes.
type remoteService struct {
	terminal              bool
	cmd                   *exec.Cmd
	done                  chan error
	log                   *os.File
	root, endpoint, token string
}

func (r *remoteService) URL() string       { return r.endpoint }
func (r *remoteService) TokenFile() string { return r.token }
func (r *remoteService) Stopped() bool {
	return r.cmd == nil || r.cmd.Process == nil || errors.Is(syscall.Kill(-r.cmd.Process.Pid, 0), syscall.ESRCH)
}
func (r *remoteService) Close() error { return r.close() }

// ReserveRecordService allocates only a private target. Start runs after the
// ordinary agent result identifies the execution overlay that owns its env.
func ReserveRecordService(ctx context.Context, root string) (interface {
	URL() string
	TokenFile() string
	Start(context.Context, Options, tool.Object) error
	Close() error
	Stopped() bool
}, error,
) {
	return reserveRemote(ctx, root)
}

func startRemote(ctx context.Context, o Options, plan tool.Object) (*remoteService, error) {
	r, err := reserveRemote(ctx, tool.Str(plan["root"]))
	if err != nil {
		return nil, err
	}
	if err := r.Start(ctx, o, plan); err != nil {
		_ = r.Close()
		return nil, err
	}
	return r, nil
}

func reserveRemote(ctx context.Context, root string) (*remoteService, error) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, err
	}
	r := &remoteService{root: root, endpoint: "ws://" + address, done: make(chan error, 1)}
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
	return r, nil
}

func (r *remoteService) Start(ctx context.Context, o Options, plan tool.Object) error {
	var err error
	var args []string
	nativeArgs := tool.Strings(plan["args"])
	for i := 0; i < len(nativeArgs); i++ {
		arg := nativeArgs[i]
		if arg == "--listen" && i+1 < len(nativeArgs) {
			i++
			continue
		}

		if arg != "--stdio" {
			args = append(args, arg)
		}
	}
	args = append(args, "--listen", r.endpoint, "--ws-auth", "capability-token", "--ws-token-file", r.token)
	r.cmd = exec.CommandContext(context.WithoutCancel(ctx), tool.Str(plan["cli"]), args...)
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r.cmd.Dir = tool.Str(plan["project"])
	r.cmd.Stdout = r.log
	r.cmd.Stderr = r.log
	r.cmd.Env, err = driverEnvironment(o, plan)
	if err == nil {
		err = r.cmd.Start()
	}
	if err != nil {
		r.terminal = true
		return err
	}
	go func() { r.done <- r.cmd.Wait() }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case e := <-r.done:
			r.terminal = true
			r.log.Close()
			os.Remove(r.token)
			return fmt.Errorf("native remote service ended before readiness: %w", e)
		case <-ctx.Done():
			_ = r.close()
			return ctx.Err()
		case <-ticker.C:
			var dialer net.Dialer
			dialer.Timeout = 100 * time.Millisecond
			conn, e := dialer.DialContext(ctx, "tcp", strings.TrimPrefix(r.endpoint, "ws://"))
			if e == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}

func (r *remoteService) close() (closeErr error) {
	defer r.log.Close()
	defer os.Remove(r.token)
	if r.cmd == nil || r.cmd.Process == nil {
		return nil
	}
	defer func() {
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		closeErr = errors.Join(closeErr, tool.WaitProcessGroup(r.cmd.Process.Pid))
	}()
	if r.terminal {
		return nil
	}
	select {
	case err := <-r.done:
		return fmt.Errorf("native remote service ended before owned cleanup: %w", err)
	default:
	}
	defer func() { r.terminal = true }()
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
