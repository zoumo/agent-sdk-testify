package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Command struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	Args    []string          `json:"args,omitempty"`
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
func WaitDriver(ctx context.Context, cmd *exec.Cmd, started ...func(int) error) (runErr error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		runErr = errors.Join(runErr, WaitProcessGroup(cmd.Process.Pid))
	}()
	for _, hook := range started {
		if err := hook(cmd.Process.Pid); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return errors.Join(err, cmd.Wait())
		}
	}
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

// WaitProcessGroup confirms that even descendants abandoned by an SDK have
// stopped. A surviving group leaves the official reservation intact.
func WaitProcessGroup(pid int) error {
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-deadline.C:
			return errors.New("owned process group has not exited")
		case <-ticker.C:
		}
	}
}

type PeerOptions struct {
	Request   string
	Lifecycle string
	Results   string
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
	if len(c.Env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range c.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Stdin = o.In
	cmd.Stdout = o.Out
	cmd.Stderr = o.Err
	if o.Results != "" {
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		defer reader.Close()
		defer writer.Close()
		output, err := os.OpenFile(o.Results, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer output.Close()
		cmd.ExtraFiles = []*os.File{writer}
		cmd.Args = append(cmd.Args, "--result-fd", "3")
		// Native argv follow --. Notification flags must precede that separator.
		for i, arg := range cmd.Args[:len(cmd.Args)-2] {
			if arg == "--" {
				cmd.Args = append(append(append([]string{}, cmd.Args[:i]...), "--result-fd", "3"), cmd.Args[i:len(cmd.Args)-2]...)
				break
			}
		}
		done := make(chan error, 1)
		go func() {
			decoder := json.NewDecoder(reader)
			encoder := json.NewEncoder(output)
			for {
				var value json.RawMessage
				err := decoder.Decode(&value)
				if errors.Is(err, io.EOF) {
					done <- nil
					return
				}
				if err == nil {
					err = encoder.Encode(value)
				}
				if err != nil {
					done <- fmt.Errorf("execution notification: %w", err)
					return
				}
			}
		}()
		defer func() {
			_ = writer.Close()
			select {
			case err := <-done:
				runErr = errors.Join(runErr, err)
			case <-time.After(2 * time.Second):
				_ = reader.Close()
				runErr = errors.Join(runErr, errors.New("execution notification pipe did not close"), <-done)
			}
		}()
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	if o.Lifecycle != "" {
		if err := WriteJSON(o.Lifecycle, Object{"schemaVersion": 1, "supervisorPid": os.Getpid(), "started": false, "reaped": false}); err != nil {
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if o.Results != "" {
		_ = cmd.ExtraFiles[0].Close()
	}
	if o.Lifecycle != "" {
		defer func() {
			code := -1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			report := Object{"schemaVersion": 1, "pid": cmd.Process.Pid, "supervisorPid": os.Getpid(), "started": true, "exitCode": code, "reaped": cmd.ProcessState != nil, "cleanupComplete": true}
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
