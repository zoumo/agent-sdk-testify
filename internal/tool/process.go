package tool

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Command struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type Result struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func AbsoluteExecutable(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	return filepath.Abs(p)
}

// WaitDriver owns the process group so cancellation also reaps descendants.
func WaitDriver(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

type PeerOptions struct {
	Request   string
	Lifecycle string
	In        io.Reader
	Out       io.Writer
	Err       io.Writer
}

func RunPeer(o PeerOptions) (runErr error) {
	if o.Request == "" {
		return errors.New("peer requires --request")
	}
	var c Command
	if err := ReadJSON(o.Request, &c); err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), c.Command, c.Args...)
	cmd.Stdin = o.In
	cmd.Stdout = o.Out
	cmd.Stderr = o.Err
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	if err := cmd.Start(); err != nil {
		return err
	}
	if o.Lifecycle != "" {
		defer func() {
			code := cmd.ProcessState.ExitCode()
			report := Object{"schemaVersion": 1, "exitCode": code, "reaped": cmd.ProcessState != nil, "cleanupComplete": true}
			if runErr != nil {
				report["error"] = runErr.Error()
			}
			runErr = errors.Join(runErr, WriteJSON(o.Lifecycle, report))
		}()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case sig := <-ch:
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
		}
		_ = cmd.Process.Signal(sig)
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			return <-done
		}
	}
}
