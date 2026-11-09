package lifecycle

import (
	"errors"
	"syscall"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func processPID(value any) int {
	if pid, ok := value.(int); ok {
		if pid > 0 {
			return pid
		}
		return 0
	}
	n, ok := value.(float64)
	if !ok || n <= 0 || n != float64(int(n)) {
		return 0
	}
	return int(n)
}

func confirmedExited(pid int, group bool) error {
	if pid <= 0 {
		return errors.New("process identity is unavailable")
	}
	if group {
		pid = -pid
	}
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return ErrBusy
	}
	return nil
}

func stoppedLaunch(process tool.Object) error {
	switch process["state"] {
	case "not-started":
		return nil
	case "stopped":
		return nil
	case "started":
		return confirmedExited(processPID(process["pid"]), true)
	default:
		return errors.New("process launch is unresolved; fixture remains busy")
	}
}
