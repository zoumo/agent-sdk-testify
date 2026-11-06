package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type lockedTransaction struct {
	transactionContext
	lock *Lock
}

func (t *lockedTransaction) releaseLock() error {
	if t == nil {
		return nil
	}
	return t.lock.Close()
}

func claimTransactionConnection(sessionDir string, execution Execution, connection string) (string, error) {
	path, err := connectionClaimPath(execution, connection)
	if err != nil {
		return "", err
	}
	if err := createConnectionClaim(path); err != nil {
		return "", fmt.Errorf("%s connection %s already started: %w", execution.Command, connection, err)
	}
	if err := markConnection(sessionDir, execution.ExecutionID, connection, "running", "open"); err != nil {
		return "", errors.Join(err, removeFileIfExists(path))
	}
	return path, nil
}

func writeTransactionResult(ctx context.Context, run *transactionContext, stream *os.File, runErr error) error {
	if runErr != nil {
		if err := tool.WriteJSON(filepath.Join(run.dir, "receipts", run.connection+".failure.json"), tool.Object{"status": "failed", "error": runErr.Error()}); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return writeActionResult(ctx, run, stream, runErr)
}

func finishTransactionFailure(ctx context.Context, run *transactionContext, stream *os.File, fail func() error, causes ...error) error {
	err := errors.Join(append(causes, fail())...)
	return errors.Join(err, writeTransactionResult(ctx, run, stream, err))
}

func settleTransactionConnection(sessionDir, action, executionID, connection, status string) (ready, passed bool, err error) {
	passed = true
	err = updateIndex(sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != executionID {
				continue
			}
			setConnectionStarted(execution, connection, status)
			ready = true
			for _, declared := range execution.Connections {
				if declared.Status == "pending" || declared.Status == "running" {
					ready = false
				}
				if declared.Status != "passed" {
					passed = false
				}
			}
			execution.Status = "open"
			execution.UpdatedAt = now()
			return nil
		}
		return errors.New(action + " execution absent from index")
	})
	return ready, passed, err
}

func failTransaction(session *Session, sessionDir, action, executionID, overlayRoot string, restore func() error) error {
	errs := []error{restore(), scrubExecutionAuthentication(session, overlayRoot)}
	errs = append(errs, updateIndex(sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != executionID {
				continue
			}
			execution.Status = "failed"
			execution.ArtifactID = ""
			execution.UpdatedAt = now()
			for j := range execution.Connections {
				if execution.Connections[j].Status == "pending" || execution.Connections[j].Status == "running" {
					execution.Connections[j].Status = "failed"
				}
			}
			return nil
		}
		return errors.New(action + " execution absent from index")
	}))
	return errors.Join(errs...)
}
