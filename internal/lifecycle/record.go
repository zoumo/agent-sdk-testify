package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// BeginRecordConnection allocates or joins a normal record execution. Allocation
// is serialized separately from connection lifetimes, which share the action lock.
func BeginRecordConnection(sessionDir, connection string, args []string, timeout time.Duration, target string) (*ActionRun, error) {
	allocation, err := lockFile(filepath.Join(sessionDir, "locks", "record-start.lock"), true, true)
	if err != nil {
		return nil, err
	}
	defer allocation.Close()
	lock, exclusive, err := beginMockLock(sessionDir)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = lock.Close()
		}
	}()
	session, err := OpenSession(sessionDir)
	if err != nil {
		return nil, err
	}
	origin, err := recordOrigin(sessionDir, session, connection)
	if err != nil {
		return nil, err
	}
	if origin == nil {
		if err := checkOfficialIdle(sessionDir); err != nil {
			return nil, err
		}
	}
	plan, err := LoadPlan(session)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ConnectionIDs(session.Source, plan), connection) {
		return nil, fmt.Errorf("undeclared record connection %s", connection)
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}
	var run *ActionRun
	if pending := pendingExecution(index); pending != nil {
		if pending.Command != "record" || connectionStarted(pending, connection) {
			return nil, ErrBusy
		}
		dir := ExecutionDir(sessionDir, pending.ID)
		var execution Execution
		if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
			return nil, err
		}
		if err := execution.Validate(*session); err != nil {
			return nil, err
		}
		if hash, err := tool.FileDigest(filepath.Join(dir, "execution.json")); err != nil || hash != pending.DescriptorSHA256 {
			return nil, errors.New("record descriptor changed")
		}
		if !reflect.DeepEqual(execution.Artifact.OfficialOrigin, origin) || !slices.Equal(execution.Args, args) || execution.Limits.TimeoutNS != int64(timeout) {
			return nil, errors.New("record connection conflicts with execution target or timeout")
		}
		execution.RuntimeEnv, err = executionRuntimeEnvironment(session, "record", filepath.Dir(execution.Home))
		if err != nil {
			return nil, err
		}
		run = &ActionRun{Session: session, Plan: plan, Execution: execution, Dir: dir, OverlayRoot: filepath.Dir(execution.Home), ArtifactRoot: execution.ArtifactRoot, lock: lock}
	} else {
		if !exclusive {
			return nil, ErrBusy
		}
		run, err = createActionLocked(sessionDir, "record", args, timeout, nil, nil, lock, origin)
		if err != nil {
			return nil, err
		}
	}
	targetPath := filepath.Join(run.OverlayRoot, "record-target.private")
	if existing, err := os.ReadFile(targetPath); err == nil {
		if string(existing) != target {
			return nil, errors.New("record connection targets differ")
		}
	} else if os.IsNotExist(err) {
		if err := os.WriteFile(targetPath, []byte(target), 0o600); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	run.Connection = connection
	if origin != nil {
		proof := tool.Object{"schemaVersion": 1, "origin": origin, "connectionId": connection, "executionId": run.Execution.ExecutionID, "protocolPid": os.Getpid(), "supervisorPid": os.Getppid(), "toolSHA256": session.ToolSHA256}
		if err := writeExclusive(filepath.Join(run.Dir, "receipts", connection+".origin.json"), proof); err != nil {
			return nil, err
		}
	}
	if err := claimMockConnection(run.Execution, connection); err != nil {
		return nil, err
	}
	if err := markMockConnection(sessionDir, run.Execution.ExecutionID, connection, "running", "open"); err != nil {
		return nil, err
	}
	if exclusive {
		if err := lock.Downgrade(); err != nil {
			return nil, err
		}
	}
	ok = true
	return run, nil
}

// RecordFinalizationLock prevents simultaneous final connections from publishing
// twice. Callers hold it through material closure and result persistence.
func RecordFinalizationLock(run *ActionRun) (*Lock, error) {
	return lockFile(filepath.Join(run.Dir, "record-finalize.lock"), true, true)
}

func CompleteRecordConnection(run *ActionRun, status string) (ready, passed bool, err error) {
	passed = true
	err = updateIndex(filepath.Dir(filepath.Dir(run.Dir)), func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != run.Execution.ExecutionID {
				continue
			}
			setConnectionStarted(execution, run.Connection, status)
			ready = true
			for _, c := range execution.Connections {
				if c.Status == "pending" || c.Status == "running" {
					ready = false
				}
				if c.Status != "passed" {
					passed = false
				}
			}
			execution.Status = "open"
			execution.UpdatedAt = now()
			return nil
		}
		return errors.New("record execution absent from index")
	})
	return ready, passed, err
}
