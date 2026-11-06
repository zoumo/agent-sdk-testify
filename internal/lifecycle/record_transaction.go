package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// RecordRequest contains the immutable inputs shared by every connection of one
// record execution.
type RecordRequest struct {
	SessionDir string
	Connection string
	Args       []string
	Timeout    time.Duration
	Target     string
	ResultFD   int
}

// RecordProbeTracker records native process state during official preparation.
// The transaction supplies it separately so RecordContext remains read-only.
type RecordProbeTracker func(state string, pid int) error

// RecordPolicy supplies action-specific behavior. The transaction owns every
// lock, index transition, route, receipt, notification, and publication step.
type RecordPolicy interface {
	Prepare(context.Context, RecordContext, RecordProbeTracker) (tool.Object, error)
	Capture(context.Context, RecordContext) error
	Package(context.Context, RecordContext) (*corpus.Material, error)
}

// RecordContext is a read-only snapshot of transaction-owned state.
type RecordContext struct {
	transactionContext
}

func (c RecordContext) IsOfficialCapture() bool {
	return c.execution.Artifact.OfficialOrigin != nil
}

func (c RecordContext) ProcessReceipt() string {
	return filepath.Join(c.dir, "receipts", c.connection+".native-process.json")
}

type recordTransaction struct {
	lockedTransaction
}

func (r *recordTransaction) context() RecordContext {
	return RecordContext{
		transactionContext: r.transactionContext,
	}
}

func (r *recordTransaction) prepare(ctx context.Context, policy RecordPolicy) (plan tool.Object, err error) {
	lock, err := lockFile(filepath.Join(r.dir, "record-finalize.lock"), true, true)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	prepared, err := policy.Prepare(ctx, r.context(), r.trackNativeProbe)
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, errors.New("record policy prepared no plan")
	}
	return prepared, nil
}

func (r *recordTransaction) trackNativeProbe(state string, pid int) error {
	return writeAtomic(filepath.Join(r.dir, "receipts", r.connection+".probe.native-process.json"), tool.Object{"state": state, "pid": pid})
}

// finalize serializes completion across declared connections and owns every
// state transition from connection outcome through result delivery.
func (r *recordTransaction) finalize(ctx context.Context, policy RecordPolicy, stream *os.File, runErr error) (err error) {
	lock, lockErr := lockFile(filepath.Join(r.dir, "record-finalize.lock"), true, true)
	if lockErr != nil {
		// Without the finalization gate, close the execution with one terminal
		// index transition and do not attempt success or publication.
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, lockErr)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return r.finalizeOutcome(ctx, policy, stream, runErr)
}

func (r *recordTransaction) finalizeOutcome(ctx context.Context, policy RecordPolicy, stream *os.File, runErr error) error {
	status := recordStatus(runErr)
	ready, allPassed, settleErr := r.settleConnection(status)
	if settleErr != nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, settleErr)
	}
	if !ready {
		return writeTransactionResult(ctx, &r.transactionContext, stream, runErr)
	}
	if !allPassed {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, errors.New("record execution has failed connections"))
	}

	artifact, err := policy.Package(ctx, r.context())
	if err != nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, err)
	}
	if artifact == nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, errors.New("record policy produced no material"))
	}
	var current Current
	if err := tool.ReadJSON(filepath.Join(r.sessionDir, "current.json"), &current); err != nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, err)
	}
	if err := r.publish(*artifact); err != nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, err)
	}
	if err := writeTransactionResult(ctx, &r.transactionContext, stream, nil); err != nil {
		// The selection switch precedes final notification. Restore the previous
		// selection when the caller cannot observe the committed result.
		restoreErr := writeCurrent(r.sessionDir, r.session, current.MaterialID)
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, err, restoreErr)
	}
	return nil
}

func (r *recordTransaction) settleConnection(status string) (ready, passed bool, err error) {
	return settleTransactionConnection(r.sessionDir, "record", r.execution.ExecutionID, r.connection, status)
}

func (r *recordTransaction) fail() error {
	return failTransaction(r.session, r.sessionDir, "record", r.execution.ExecutionID, r.overlayRoot, r.restoreRoutes)
}

func (r *recordTransaction) restoreRoutes() error {
	var errs []error
	// Official SDK history reads may continue after protocol EOF. Its
	// orchestrator restores the record route after the driver has finished.
	if r.execution.Artifact.OfficialOrigin == nil {
		errs = append(errs, restoreRouteSlots(r.session, "record"))
	}
	if route, ok := r.session.Routes["mock"]; ok && route.Enabled {
		errs = append(errs, restoreRouteSlots(r.session, "mock"))
	}
	return errors.Join(errs...)
}

func (r *recordTransaction) publish(artifact corpus.Material) error {
	if _, err := inspectMaterial(r.session, artifact); err != nil {
		return err
	}
	if err := validateRecordReport(artifact.Path); err != nil {
		return err
	}
	for _, connection := range r.execution.Connections {
		if err := validateCaptureTransport(connection.Receipt, 0); err != nil {
			return fmt.Errorf("connection %s: %w", connection.ID, err)
		}
	}
	if err := r.restoreRoutes(); err != nil {
		return err
	}
	if err := scrubExecutionAuthentication(r.session, r.overlayRoot); err != nil {
		return err
	}
	materialRoot := filepath.Join(r.sessionDir, "materials", artifact.ID)
	stage, err := os.MkdirTemp(filepath.Join(r.sessionDir, "materials"), ".record-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyMaterial(artifact.Path, stage); err != nil {
		return err
	}
	files, err := history.HashTree(stage)
	if err != nil {
		return err
	}
	delete(files, "material.json")
	artifact.Path = materialRoot
	artifact.Files = files
	if err := writeExclusive(filepath.Join(stage, "material.json"), artifact); err != nil {
		return err
	}
	if _, err := os.Lstat(materialRoot); !os.IsNotExist(err) {
		return errors.New("material identity already exists")
	}
	if err := os.Rename(stage, materialRoot); err != nil {
		return err
	}
	if _, err := inspectMaterial(r.session, artifact); err != nil {
		return err
	}
	execution, descriptorSHA256, err := bindRecordCapture(r.session, r.execution, r.artifactRoot, artifact)
	if err != nil {
		return err
	}
	// All fallible finalization precedes the atomic selection change.
	if err := updateIndex(r.sessionDir, func(index *Index) error {
		for _, existing := range index.Artifacts {
			if existing.ID == artifact.ID {
				return errors.New("artifact identity already exists")
			}
		}
		index.Artifacts = append(index.Artifacts, artifact)
		for i := range index.Executions {
			indexed := &index.Executions[i]
			if indexed.ID != execution.ExecutionID {
				continue
			}
			indexed.ArtifactID = artifact.ID
			indexed.DescriptorSHA256 = descriptorSHA256
			indexed.Status = "passed"
			indexed.UpdatedAt = now()
			for j := range indexed.Connections {
				indexed.Connections[j].Status = "passed"
			}
			return nil
		}
		return errors.New("record execution absent from index")
	}); err != nil {
		return err
	}
	if err := writeCurrent(r.sessionDir, r.session, artifact.ID); err != nil {
		return err
	}
	r.execution = execution
	return nil
}

func bindRecordCapture(session *Session, execution Execution, artifactRoot string, artifact corpus.Material) (Execution, string, error) {
	ids := make([]string, 0, len(execution.Connections))
	for _, connection := range execution.Connections {
		ids = append(ids, connection.ID)
	}
	connections, err := executionConnections(artifactRoot, "record", ids, &artifact, session.Routes["record"])
	if err != nil {
		return Execution{}, "", err
	}
	execution.Connections = connections
	execution.Artifact = artifact
	if err := execution.Validate(*session); err != nil {
		return Execution{}, "", err
	}
	path := filepath.Join(filepath.Dir(artifactRoot), "execution.json")
	if err := writeAtomic(path, execution); err != nil {
		return Execution{}, "", err
	}
	digest, err := tool.FileDigest(path)
	if err != nil {
		return Execution{}, "", err
	}
	return execution, digest, nil
}

// RunRecord owns one record connection from allocation or join through result
// delivery and lock release.
func RunRecord(ctx context.Context, request RecordRequest, policy RecordPolicy) (runErr error) {
	if policy == nil {
		return errors.New("record policy is required")
	}
	if request.Connection == "" {
		request.Connection = "main"
	}
	result, err := ResultStream(request.ResultFD)
	if err != nil {
		return err
	}
	if result != nil {
		defer result.Close()
	}
	run, err := beginRecordTransaction(request)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, run.releaseLock()) }()
	completed := false
	defer func() {
		finalErr := runErr
		if !completed && finalErr == nil {
			finalErr = errors.New("record transaction aborted")
		}
		runErr = errors.Join(runErr, run.finalize(ctx, policy, result, finalErr))
	}()

	if err := notifyActionStarted(ctx, &run.transactionContext, result); err != nil {
		return err
	}
	plan, err := run.prepare(ctx, policy)
	if err != nil {
		return err
	}
	run.plan = plan
	if err := policy.Capture(ctx, run.context()); err != nil {
		return err
	}
	completed = true
	return nil
}

func recordStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "passed"
}

func beginRecordTransaction(request RecordRequest) (transaction *recordTransaction, err error) {
	sessionDir, err := canonicalSessionDir(request.SessionDir)
	if err != nil {
		return nil, err
	}
	allocation, err := lockFile(filepath.Join(sessionDir, "locks", "record-start.lock"), true, true)
	if err != nil {
		return nil, err
	}
	defer allocation.Close()

	lock, exclusive, err := beginActionLock(sessionDir)
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
	origin, err := recordOrigin(sessionDir, session, request.Connection)
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
	if !slices.Contains(ConnectionIDs(session.Source, plan), request.Connection) {
		return nil, fmt.Errorf("undeclared record connection %s", request.Connection)
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}

	var run *transactionContext
	allocated := false
	defer func() {
		if ok || !allocated || run == nil {
			return
		}
		aborted := &recordTransaction{
			lockedTransaction: lockedTransaction{
				transactionContext: *run,
			},
		}
		err = errors.Join(err, aborted.fail())
	}()
	if pending := pendingExecution(index); pending != nil {
		if pending.Command != "record" || connectionStarted(pending, request.Connection) {
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
		if !reflect.DeepEqual(execution.Artifact.OfficialOrigin, origin) || !slices.Equal(execution.Args, request.Args) || execution.Limits.TimeoutNS != int64(request.Timeout) {
			return nil, errors.New("record connection conflicts with execution target or timeout")
		}
		execution.RuntimeEnv, err = executionRuntimeEnvironment(session, "record", filepath.Dir(execution.Home))
		if err != nil {
			return nil, err
		}
		run = newTransactionContext(session, plan, execution, sessionDir)
	} else {
		if !exclusive {
			return nil, ErrBusy
		}
		run, err = createTransactionLocked(sessionDir, "record", request.Args, nil, request.Timeout, 0, nil, nil, origin)
		if err != nil {
			return nil, err
		}
		allocated = true
	}

	targetPath := filepath.Join(run.overlayRoot, "record-target.private")
	if existing, err := os.ReadFile(targetPath); err == nil {
		if string(existing) != request.Target {
			return nil, errors.New("record connection targets differ")
		}
	} else if os.IsNotExist(err) {
		if err := os.WriteFile(targetPath, []byte(request.Target), 0o600); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	run.connection = request.Connection
	var claimPath, originReceiptPath string
	claimed := false
	defer func() {
		if ok || allocated {
			return
		}
		if claimed {
			err = errors.Join(err, removeFileIfExists(claimPath))
		}
		if originReceiptPath != "" {
			err = errors.Join(err, removeFileIfExists(originReceiptPath))
		}
	}()
	if origin != nil {
		path := filepath.Join(run.dir, "receipts", request.Connection+".origin.json")
		proof := tool.Object{"schemaVersion": 1, "origin": origin, "connectionId": request.Connection, "executionId": run.execution.ExecutionID, "protocolPid": os.Getpid(), "supervisorPid": os.Getppid(), "toolSHA256": session.ToolSHA256}
		if err := writeExclusive(path, proof); err != nil {
			return nil, err
		}
		originReceiptPath = path
	}
	claimPath, err = claimTransactionConnection(sessionDir, run.execution, request.Connection)
	if err != nil {
		return nil, err
	}
	claimed = true
	if exclusive {
		if err := lock.Downgrade(); err != nil {
			return nil, err
		}
	}
	ok = true
	return &recordTransaction{
		lockedTransaction: lockedTransaction{
			transactionContext: *run,
			lock:               lock,
		},
	}, nil
}

func removeFileIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
