package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	case "started":
		return confirmedExited(processPID(process["pid"]), true)
	default:
		return errors.New("process launch is unresolved; session remains busy")
	}
}

// The caller holds the exclusive action lock. Check every process first: a
// refused finish must not modify evidence or clean any resource. PID reuse is
// deliberately conservative: a live or inaccessible PID never proves exit.
func recoverInterrupted(sessionDir string, session *Session) error {
	var active tool.Object
	activePath := filepath.Join(sessionDir, "official-active.json")
	err := tool.ReadJSON(activePath, &active)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		if active["sessionId"] != session.SessionID || active["tracked"] != true {
			return errors.New("untracked official activity; session remains busy")
		}
		if err := confirmedExited(processPID(active["pid"]), false); err != nil {
			return err
		}
		for _, value := range tool.Obj(active["processes"]) {
			if err := stoppedLaunch(tool.Obj(value)); err != nil {
				return err
			}
		}
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return err
	}
	var interrupted []string
	for _, execution := range index.Executions {
		pending := execution.Status == "running" || execution.Status == "open"
		dir := ExecutionDir(sessionDir, execution.ID)
		nativePaths, err := filepath.Glob(filepath.Join(dir, "receipts", "*.native-process.json"))
		if err != nil {
			return err
		}
		for _, path := range nativePaths {
			var process tool.Object
			if err := tool.ReadJSON(path, &process); err != nil {
				return err
			}
			if err := stoppedLaunch(process); err != nil {
				return err
			}
		}
		if !pending {
			continue
		}
		var descriptor Execution
		if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &descriptor); err != nil {
			return err
		}
		if err := descriptor.Validate(*session); err != nil {
			return err
		}
		if hash, err := tool.FileDigest(filepath.Join(dir, "execution.json")); err != nil || hash != execution.DescriptorSHA256 {
			return errors.New("execution descriptor changed")
		}
		for _, connection := range descriptor.Connections {
			claim := filepath.Join(filepath.Dir(connection.Receipt), "."+connection.ID+".started")
			if execution.Command == "record" {
				claim = filepath.Join(dir, "receipts", "."+connection.ID+".started")
			}
			var owner tool.Object
			if err := tool.ReadJSON(claim, &owner); pending && err == nil {
				if err := confirmedExited(processPID(owner["pid"]), false); err != nil {
					return err
				}
			} else if pending && err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("unresolved execution owner: %w", err)
			}
		}
		if pending {
			interrupted = append(interrupted, execution.ID)
		}
	}
	// All known processes are now confirmed gone. Retain failure before releasing
	// a reservation; later successful executions cannot erase this evidence.
	for _, id := range interrupted {
		if err := writeAtomic(filepath.Join(ExecutionDir(sessionDir, id), "interruption.json"), tool.Object{"status": "failed", "error": "execution interrupted", "cleanupComplete": true}); err != nil {
			return err
		}
	}
	if len(interrupted) != 0 {
		if err := updateIndex(sessionDir, func(index *Index) error {
			for i := range index.Executions {
				for _, id := range interrupted {
					if index.Executions[i].ID == id {
						index.Executions[i].Status = "failed"
						index.Executions[i].UpdatedAt = now()
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if active != nil {
		parent := "official-records"
		if active["mode"] == "verify" {
			parent = "verifications"
		}
		id := tool.Str(active["id"])
		if !filepath.IsLocal(id) || filepath.Base(id) != id {
			return errors.New("invalid official activity identity")
		}
		dir := filepath.Join(sessionDir, parent, id)
		failure := tool.Object{"schemaVersion": 1, "sessionId": session.SessionID, "id": id, "mode": active["mode"], "status": "failed", "error": "official orchestration interrupted", "cleanupComplete": true, "interrupted": active}
		if err := writeAtomic(filepath.Join(dir, "interruption.json"), failure); err != nil {
			return err
		}
		var previous tool.Object
		if err := tool.ReadJSON(filepath.Join(dir, "result.json"), &previous); err == nil {
			failure["previousResult"] = previous
		}
		if err := writeAtomic(filepath.Join(dir, "result.json"), failure); err != nil {
			return err
		}
		if err := os.Remove(activePath); err != nil {
			return err
		}
	}
	return nil
}
