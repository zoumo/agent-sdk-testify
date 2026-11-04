package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ResultStream duplicates a caller-owned descriptor before an execution starts.
// Close-on-exec prevents the native Agent from keeping the notification pipe open.
func ResultStream(fd int) (*os.File, error) {
	if fd == -1 || fd == 0 {
		return nil, nil
	}
	if fd < 3 {
		return nil, errors.New("result-fd must be >=3")
	}
	duplicate, err := syscall.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("result-fd: %w", err)
	}
	syscall.CloseOnExec(duplicate)
	// Go needs a pollable descriptor so closing or setting a deadline can
	// interrupt pipe backpressure. The inherited descriptor is notification-only.
	if err := syscall.SetNonblock(duplicate, true); err != nil {
		_ = syscall.Close(duplicate)
		return nil, err
	}
	return os.NewFile(uintptr(duplicate), "execution-result"), nil
}

func actionResult(run *ActionRun, status string, actionErr error) (string, tool.Object) {
	id := run.Connection
	if id == "" {
		id = "main"
	}
	path := filepath.Join(run.Dir, "receipts", id+".result.json")
	value := tool.Object{
		"schemaVersion": 1,
		"sessionId":     run.Session.SessionID,
		"processId":     os.Getpid(),
		"executionId":   run.Execution.ExecutionID,
		"connectionId":  id,
		"command":       run.Execution.Command,
		"status":        status,
		"execution":     filepath.Join(run.Dir, "execution.json"),
		"result":        path,
	}
	for _, connection := range run.Execution.Connections {
		if connection.ID == id {
			value["receipt"] = connection.Receipt
			if connection.ProcessReceipt != "" {
				value["processReceipt"] = connection.ProcessReceipt
			}
		}
	}
	if run.Execution.Command == "mock" || (status == "passed" && run.Execution.Artifact.ManifestSHA256 != "") {
		material := run.Execution.Artifact
		value["material"] = tool.Object{
			"id": material.ID, "path": material.Path, "provenance": material.Provenance,
			"planSHA256": material.PlanSHA256, "manifestSHA256": material.ManifestSHA256,
			"recordingSHA256": material.RecordingSHA256,
		}
	}
	if actionErr != nil {
		value["error"] = actionErr.Error()
	}
	return path, value
}

// NotifyActionStarted gives the caller the exact identity before protocol I/O.
// Each notification references durable state; stdout remains native protocol only.
func NotifyActionStarted(ctx context.Context, run *ActionRun, stream *os.File) error {
	path, value := actionResult(run, "running", nil)
	hash, err := tool.FileDigest(filepath.Join(run.Dir, "execution.json"))
	if err != nil {
		return err
	}
	value["executionSHA256"] = hash
	if err := writeAtomic(path, value); err != nil {
		return err
	}
	if stream != nil {
		return notifyResult(ctx, stream, value)
	}
	return nil
}

// WriteActionResult runs after receipts, cleanup and the index are finalized.
// A connection result does not imply that other connections have completed.
func WriteActionResult(ctx context.Context, run *ActionRun, stream *os.File, actionErr error) error {
	status := "passed"
	if actionErr != nil {
		status = "failed"
	}
	path, value := actionResult(run, status, actionErr)
	hash, err := tool.FileDigest(filepath.Join(run.Dir, "execution.json"))
	if err != nil {
		return err
	}
	value["executionSHA256"] = hash
	if err := writeAtomic(path, value); err != nil {
		return err
	}
	if stream != nil {
		return notifyResult(context.WithoutCancel(ctx), stream, value)
	}
	return nil
}

// Notifications must not outlive a blocked or abandoned caller. Final results
// remain durable even when cancellation has already closed the protocol stream.
func notifyResult(ctx context.Context, stream *os.File, value tool.Object) error {
	deadline := time.Now().Add(time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := stream.SetWriteDeadline(deadline); err != nil {
		info, statErr := stream.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.Join(err, statErr)
		}
		return json.NewEncoder(stream).Encode(value)
	}
	stop := context.AfterFunc(ctx, func() { _ = stream.SetWriteDeadline(time.Now()) })
	defer stop()
	return json.NewEncoder(stream).Encode(value)
}
