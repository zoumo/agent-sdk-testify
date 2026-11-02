package tool

import (
	"context"
	"errors"
	"flag"
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
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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

func RunPeer(args []string) (runErr error) {
	flags := flag.NewFlagSet("peer", flag.ContinueOnError)
	requestPath := flags.String("request", "", "peer command file")
	lifecycle := flags.String("lifecycle", "", "internal protocol child exit evidence")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var c Command
	if err := ReadJSON(*requestPath, &c); err != nil {
		return err
	}
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	if err := cmd.Start(); err != nil {
		return err
	}
	if *lifecycle != "" {
		defer func() {
			code := cmd.ProcessState.ExitCode()
			report := Object{"schemaVersion": 1, "exitCode": code, "reaped": cmd.ProcessState != nil, "cleanupComplete": true}
			if runErr != nil {
				report["error"] = runErr.Error()
			}
			runErr = errors.Join(runErr, WriteJSON(*lifecycle, report))
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
