//go:build darwin || linux

package capture

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

func environment(o Options) []string {
	var env []string
	if !o.ClearEnv {
		env = os.Environ()
	}
	for _, override := range o.Env {
		key, _, _ := strings.Cut(override, "=")
		prefix := key + "="
		filtered := env[:0]
		for _, item := range env {
			if !strings.HasPrefix(item, prefix) {
				filtered = append(filtered, item)
			}
		}
		env = append(filtered, override)
	}
	return env
}
func prepare(o Options) (*exec.Cmd, recording.NativeIdentity, error) {
	identity := recording.NativeIdentity{Args: append([]string{}, o.Command[1:]...), Version: o.NativeVersion}
	cwd := o.CWD
	var err error
	if cwd == "" {
		cwd, err = os.Getwd()
	} else {
		cwd, err = filepath.Abs(cwd)
	}
	if err != nil {
		return nil, identity, err
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, identity, err
	}
	stat, err := os.Stat(cwd)
	if err != nil {
		return nil, identity, err
	}
	if !stat.IsDir() {
		return nil, identity, fmt.Errorf("child cwd is not a directory: %s", cwd)
	}
	identity.CWD = cwd
	env := environment(o)
	executable := o.Command[0]
	if !strings.ContainsRune(executable, os.PathSeparator) {
		var search string
		for _, item := range env {
			if strings.HasPrefix(item, "PATH=") {
				search = strings.TrimPrefix(item, "PATH=")
			}
		}
		executable = ""
		for _, dir := range filepath.SplitList(search) {
			if dir == "" {
				dir = "."
			}
			candidate, e := filepath.Abs(filepath.Join(dir, o.Command[0]))
			if e != nil {
				continue
			}
			if p, e := exec.LookPath(candidate); e == nil {
				executable = p
				break
			}
		}
		if executable == "" {
			return nil, identity, fmt.Errorf("resolve executable %q in child PATH: %w", o.Command[0], exec.ErrNotFound)
		}
	} else {
		executable, err = filepath.Abs(executable)
		if err == nil {
			executable, err = exec.LookPath(executable)
		}
		if err != nil {
			return nil, identity, err
		}
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, identity, err
	}
	identity.Executable = executable
	identity.SHA256, err = hashFile(executable)
	if err != nil {
		return nil, identity, err
	}
	cmd := exec.Command(executable, o.Command[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, identity, nil
}

// os.Stdin/os.Stdout may be wrappers around blocking descriptors that Go's
// poller does not own. A nonblocking duplicate registered with the poller makes
// Close interrupt both idle reads and backpressured writes. The handler owns
// these streams for this invocation; no goroutine is abandoned in a syscall.
func interruptible(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, errors.New("missing stdio descriptor")
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err = syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

func groupExists(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	// Darwin can return EPERM for a group containing only a child in its exit
	// transition. It still exists until Wait/reaping makes ESRCH observable.
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return err == nil, err
}
func signalGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// A direct child's successful Wait does not prove that inherited pipe holders
// or other descendants are gone. Bound both TERM and KILL phases for the group.
func cleanupGroup(pid int, grace time.Duration) error {
	exists, err := groupExists(pid)
	if err != nil || !exists {
		return err
	}
	var cleanupErr error
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		cleanupErr = errors.Join(cleanupErr, signalGroup(pid, sig))
		deadline := time.Now().Add(grace)
		for {
			exists, err = groupExists(pid)
			if err != nil {
				return errors.Join(cleanupErr, err)
			}
			if !exists {
				return nil
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				break
			}
			time.Sleep(min(remaining, 5*time.Millisecond))
		}
	}
	return errors.Join(cleanupErr, fmt.Errorf("owned process group %d still exists after TERM/KILL cleanup", pid))
}

type nativeExit struct {
	reaped bool
	code   *int
	signal string
	err    error
}

// With *os.File stdio and exec.Command (not CommandContext), exec has no copy
// workers or context watcher. Reap this one owned PID directly with WNOHANG:
// even an unkillable kernel task cannot strand a blocking Cmd.Wait goroutine
// past the final cleanup deadline. Release closes Go's process handle.
func waitNative(cmd *exec.Cmd, processMu *sync.Mutex, abandon <-chan struct{}) nativeExit {
	// Reaping and releasing must be atomic with respect to a direct-child
	// signal: after Wait4 the PID can be reused, and Release mutates Process.Pid.
	poll := func() (nativeExit, bool) {
		processMu.Lock()
		defer processMu.Unlock()
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(cmd.Process.Pid, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			return nativeExit{}, false
		}
		if err != nil {
			_ = cmd.Process.Release()
			return nativeExit{err: fmt.Errorf("reap native: %w", err)}, true
		}
		if pid != 0 {
			_ = cmd.Process.Release()
			result := nativeExit{reaped: true}
			if status.Signaled() {
				result.signal = status.Signal().String()
				result.err = fmt.Errorf("native process signal: %s", result.signal)
			} else {
				code := status.ExitStatus()
				result.code = &code
				if code != 0 {
					result.err = fmt.Errorf("native process exit status %d", code)
				}
			}
			return result, true
		}
		return nativeExit{}, false
	}
	for {
		if result, done := poll(); done {
			return result
		}
		select {
		case <-abandon:
			processMu.Lock()
			_ = cmd.Process.Release()
			processMu.Unlock()
			return nativeExit{err: errors.New("native process not reaped by cleanup deadline")}
		case <-time.After(5 * time.Millisecond):
		}
	}
}
