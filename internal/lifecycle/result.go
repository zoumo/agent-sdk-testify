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

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ConnectionStatus is the durable result of one connection in an action
// execution.
type ConnectionStatus string

const (
	ConnectionStatusUnknown ConnectionStatus = "unknown"
	ConnectionStatusPending ConnectionStatus = "pending"
	ConnectionStatusRunning ConnectionStatus = "running"
	ConnectionStatusPassed  ConnectionStatus = "passed"
	ConnectionStatusFailed  ConnectionStatus = "failed"
)

// ExecutionStatus is the durable result of all declared connections in one
// action execution.
type ExecutionStatus string

const (
	ExecutionStatusUnknown ExecutionStatus = "unknown"
	ExecutionStatusRunning ExecutionStatus = "running"
	ExecutionStatusOpen    ExecutionStatus = "open"
	ExecutionStatusPassed  ExecutionStatus = "passed"
	ExecutionStatusFailed  ExecutionStatus = "failed"
)

// ActionResult is the shared evidence returned after one record or mock
// connection call. A passed connection does not imply a complete execution.
type ActionResult struct {
	MaterialID           string
	PublicationCommitted bool
	FixtureID            string
	ExecutionID          string
	ConnectionID         string
	ConnectionStatus     ConnectionStatus
	ExecutionStatus      ExecutionStatus
	ExecutionComplete    bool
	Material             *corpus.Material
	ExecutionPath        string
	ReceiptPath          string
	ProcessReceiptPath   string
	ResultPath           string
}

// RecordResult identifies the execution and connection completed by one record
// call and, on success, the newly selected material.
type RecordResult struct {
	ActionResult
}

// MockResult identifies the execution and connection completed by one mock
// call.
type MockResult struct {
	ActionResult
	Execution *ExecutionReport
}

func (r *MockResult) refresh(fixtureDir string) error {
	r.ConnectionStatus = ConnectionStatusUnknown
	r.ExecutionStatus = ExecutionStatusUnknown
	r.ExecutionComplete = false
	report, err := ReadMockExecutionReport(fixtureDir, r.ExecutionID)
	if err != nil {
		r.Execution = nil
		return err
	}
	r.Execution = report
	r.ExecutionStatus = report.Status
	r.ExecutionComplete = report.Complete
	for _, connection := range report.Connections {
		if connection.ID == r.ConnectionID {
			r.ConnectionStatus = connection.Status
			break
		}
	}
	return nil
}

func (r *ActionResult) refresh(fixtureDir string) {
	r.ConnectionStatus = ConnectionStatusUnknown
	r.ExecutionStatus = ExecutionStatusUnknown
	r.ExecutionComplete = false
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return
	}
	for _, execution := range index.Executions {
		if execution.ID != r.ExecutionID {
			continue
		}
		r.ExecutionStatus = ExecutionStatus(execution.Status)
		r.ExecutionComplete = execution.Status == string(ExecutionStatusPassed) || execution.Status == string(ExecutionStatusFailed)
		for _, connection := range execution.Connections {
			if connection.ID == r.ConnectionID {
				r.ConnectionStatus = ConnectionStatus(connection.Status)
				break
			}
		}
		break
	}
}

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

func actionResult(run *transactionContext, status string, actionErr error) (string, tool.Object, error) {
	id := run.connection
	if id == "" {
		id = "main"
	}
	path := filepath.Join(run.dir, "receipts", id+".result.json")
	executionReference, err := PackageReference(run.fixtureDir, run.execution.Artifact.ID, filepath.Join(run.dir, "execution.json"))
	if err != nil {
		return "", nil, err
	}
	resultReference, err := PackageReference(run.fixtureDir, run.execution.Artifact.ID, path)
	if err != nil {
		return "", nil, err
	}
	value := tool.Object{
		"schemaVersion":        3,
		"materialId":           run.execution.Artifact.ID,
		"publicationCommitted": run.publicationCommitted,
		"fixtureId":            run.fixture.FixtureID,
		"executionId":          run.execution.ExecutionID,
		"connectionId":         id,
		"status":               status,
		"execution":            executionReference,
		"result":               resultReference,
	}
	value["connectionStatus"] = status
	value["executionStatus"] = "open"
	value["executionComplete"] = false
	if index, err := readIndex(IndexPath(run.fixtureDir)); err == nil {
		if indexed, err := findIndexExecution(index, run.execution.ExecutionID); err == nil && indexed != nil {
			for _, c := range indexed.Connections {
				if c.ID == id {
					value["connectionStatus"] = c.Status
				}
			}
			value["executionStatus"] = indexed.Status
			value["executionComplete"] = indexed.Status == "passed" || indexed.Status == "failed"
		}
	}
	if run.connectionTransport() == ConnectionTransportInProcess {
		value["transport"] = ConnectionTransportInProcess
	} else {
		value["processId"] = os.Getpid()
		value["command"] = run.execution.Command
	}
	for _, connection := range run.execution.Connections {
		if connection.ID != id {
			continue
		}
		receiptPath := connection.Receipt
		if run.execution.Command == "record" && status != "running" {
			stable := filepath.Join(run.dir, "receipts", id+".transport.json")
			if info, err := os.Lstat(stable); err == nil && info.Mode().IsRegular() {
				receiptPath = stable
			}
		}
		receipt, err := PackageReference(run.fixtureDir, run.execution.Artifact.ID, receiptPath)
		if err != nil {
			return "", nil, err
		}
		value["receipt"] = receipt
		if connection.ProcessReceipt != "" {
			process, err := PackageReference(run.fixtureDir, run.execution.Artifact.ID, connection.ProcessReceipt)
			if err != nil {
				return "", nil, err
			}
			value["processReceipt"] = process
		}
	}
	if run.publicationCommitted {
		value["executionStatus"] = "passed"
		value["executionComplete"] = true
		value["connectionStatus"] = "passed"
	}
	if run.execution.Command == "mock" || run.publicationCommitted {
		material := run.execution.Artifact
		value["material"] = tool.Object{
			"id": material.ID, "provenance": material.Provenance,
			"planSHA256": material.PlanSHA256, "manifestSHA256": material.ManifestSHA256,
			"recordingSHA256": material.RecordingSHA256,
		}
	}
	if actionErr != nil {
		value["error"] = actionErr.Error()
		if class := FailureClassOf(actionErr); class != "" {
			value["failureClass"] = string(class)
		}
	}
	return path, value, nil
}

// notifyActionStarted gives the caller the exact identity before protocol I/O.
// Each notification references durable state; stdout remains native protocol only.
func notifyActionStarted(ctx context.Context, run *transactionContext, stream *os.File) error {
	path, value, err := actionResult(run, "running", nil)
	if err != nil {
		return err
	}
	hash, err := tool.FileDigest(filepath.Join(run.dir, "execution.json"))
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

// writeActionResult runs after receipts, cleanup and the index are finalized.
// A connection result does not imply that other connections have completed.
func writeActionResult(ctx context.Context, run *transactionContext, stream *os.File, actionErr error) error {
	status := "passed"
	if actionErr != nil {
		status = "failed"
	}
	path, value, err := actionResult(run, status, actionErr)
	if err != nil {
		return err
	}
	hash, err := tool.FileDigest(filepath.Join(run.dir, "execution.json"))
	if err != nil {
		return err
	}
	value["executionSHA256"] = hash
	if err := writeAtomic(path, value); err != nil {
		return err
	}
	if stream != nil {
		return deliverActionResult(ctx, run, stream, path, value)
	}
	return sealExecution(run)
}

// deliverActionResult preserves the authoritative outcome when a notification
// is lost or only a prefix reaches the caller. No complete final line was sent.
func deliverActionResult(ctx context.Context, run *transactionContext, stream resultWriter, path string, value tool.Object) error {
	if err := notifyResult(context.WithoutCancel(ctx), stream, value); err != nil {
		value["deliveryError"] = err.Error()
		return errors.Join(err, writeAtomic(path, value), sealExecution(run))
	}
	return sealExecution(run)
}

// Notifications must not outlive a blocked or abandoned caller. Final results
// remain durable even when cancellation has already closed the protocol stream.
type resultWriter interface {
	Write([]byte) (int, error)
	SetWriteDeadline(time.Time) error
}

func notifyResult(ctx context.Context, stream resultWriter, value tool.Object) error {
	deadline := time.Now().Add(time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := stream.SetWriteDeadline(deadline); err != nil {
		file, ok := stream.(*os.File)
		if !ok {
			return err
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.Join(err, statErr)
		}
		return json.NewEncoder(stream).Encode(value)
	}
	stop := context.AfterFunc(ctx, func() { _ = stream.SetWriteDeadline(time.Now()) })
	defer stop()
	return json.NewEncoder(stream).Encode(value)
}
