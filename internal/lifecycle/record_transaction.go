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
	FixtureDir string
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

func (r *recordTransaction) result() *RecordResult {
	id := r.connection
	if id == "" {
		id = "main"
	}
	result := &RecordResult{
		ActionResult: ActionResult{
			FixtureID:          r.fixture.FixtureID,
			ExecutionID:        r.execution.ExecutionID,
			ConnectionID:       id,
			ConnectionStatus:   ConnectionStatusUnknown,
			ExecutionStatus:    ExecutionStatusUnknown,
			ExecutionPath:      filepath.Join(r.dir, "execution.json"),
			ProcessReceiptPath: filepath.Join(r.dir, "receipts", id+".native-process.json"),
			ResultPath:         filepath.Join(r.dir, "receipts", id+".result.json"),
		},
	}
	for _, connection := range r.execution.Connections {
		if connection.ID == id {
			result.ReceiptPath = connection.Receipt
			if connection.ProcessReceipt != "" {
				result.ProcessReceiptPath = connection.ProcessReceipt
			}
			break
		}
	}
	return result
}

func (r *recordTransaction) attachPublishedMaterial(result *RecordResult, runErr error) {
	if result == nil || runErr != nil || r.execution.Artifact.ManifestSHA256 == "" {
		return
	}
	material := cloneTransactionMaterial(r.execution.Artifact)
	result.Material = &material
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
	current, err := readCurrent(r.fixtureDir, r.fixture.FixtureID)
	if err != nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, err)
	}
	if err := r.publish(*artifact); err != nil {
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, err)
	}
	if err := writeTransactionResult(ctx, &r.transactionContext, stream, nil); err != nil {
		// The selection switch precedes final notification. Restore the previous
		// selection when the caller cannot observe the committed result.
		restoreErr := writeCurrent(r.fixtureDir, r.fixture, current.MaterialID)
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, err, restoreErr)
	}
	return nil
}

func (r *recordTransaction) settleConnection(status string) (ready, passed bool, err error) {
	return settleTransactionConnection(r.fixtureDir, "record", r.execution.ExecutionID, r.connection, status)
}

func (r *recordTransaction) fail() error {
	return failTransaction(r.fixture, r.fixtureDir, "record", r.execution.ExecutionID, r.overlayRoot, r.restoreRoutes)
}

func (r *recordTransaction) restoreRoutes() error {
	var errs []error
	// Official SDK history reads may continue after protocol EOF. Its
	// orchestrator restores the record route after the driver has finished.
	if r.execution.Artifact.OfficialOrigin == nil {
		errs = append(errs, restoreRouteSlots(r.fixture, "record"))
	}
	if route, ok := r.fixture.Routes["mock"]; ok && route.Enabled {
		errs = append(errs, restoreRouteSlots(r.fixture, "mock"))
	}
	return errors.Join(errs...)
}

func (r *recordTransaction) publish(artifact corpus.Material) error {
	if _, err := inspectMaterial(r.fixture, artifact); err != nil {
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
	if err := scrubExecutionAuthentication(r.fixture, r.overlayRoot); err != nil {
		return err
	}
	materialRoot := filepath.Join(r.fixtureDir, "materials", artifact.ID)
	stage, err := os.MkdirTemp(filepath.Join(r.fixtureDir, "materials"), ".record-")
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
	artifact.FormatVersion = corpus.MaterialFormatVersion
	if err := writeExclusive(filepath.Join(stage, "material.json"), artifact); err != nil {
		return err
	}
	if _, err := os.Lstat(materialRoot); !os.IsNotExist(err) {
		return errors.New("material identity already exists")
	}
	if err := os.Rename(stage, materialRoot); err != nil {
		return err
	}
	if _, err := inspectMaterial(r.fixture, artifact); err != nil {
		return err
	}
	execution, descriptorSHA256, err := bindRecordCapture(r.fixture, r.execution, artifact)
	if err != nil {
		return err
	}
	// All fallible finalization precedes the atomic selection change.
	if err := updateIndex(r.fixtureDir, func(index *Index) error {
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
	if err := writeCurrent(r.fixtureDir, r.fixture, artifact.ID); err != nil {
		return err
	}
	r.execution = execution
	return nil
}

func bindRecordCapture(fixture *Fixture, execution Execution, artifact corpus.Material) (Execution, string, error) {
	ids := make([]string, 0, len(execution.Connections))
	for _, connection := range execution.Connections {
		ids = append(ids, connection.ID)
	}
	connections, err := executionConnections(fixture.dir, execution.ExecutionID, "record", ids, &artifact, fixture.Routes["record"])
	if err != nil {
		return Execution{}, "", err
	}
	execution.Connections = connections
	execution.Artifact = artifact
	execution.dir = fixture.dir
	sourceReference, err := FixtureReference(fixture.dir, execution.Source.Path)
	if err != nil {
		return Execution{}, "", err
	}
	execution.Source.Path = sourceReference
	if err := execution.Resolve(fixture, fixture.dir); err != nil {
		return Execution{}, "", err
	}
	if err := execution.Validate(*fixture); err != nil {
		return Execution{}, "", err
	}
	path := filepath.Join(ExecutionDir(fixture.dir, execution.ExecutionID), "execution.json")
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
func RunRecord(ctx context.Context, request RecordRequest, policy RecordPolicy) (callResult *RecordResult, runErr error) {
	if policy == nil {
		return nil, errors.New("record policy is required")
	}
	if request.Connection == "" {
		request.Connection = "main"
	}
	resultStream, err := ResultStream(request.ResultFD)
	if err != nil {
		return nil, err
	}
	if resultStream != nil {
		defer resultStream.Close()
	}
	run, err := beginRecordTransaction(request)
	if err != nil {
		return nil, err
	}
	callResult = run.result()
	defer func() { runErr = errors.Join(runErr, run.releaseLock()) }()
	completed := false
	defer func() {
		finalErr := runErr
		if !completed && finalErr == nil {
			finalErr = errors.New("record transaction aborted")
		}
		runErr = errors.Join(runErr, run.finalize(ctx, policy, resultStream, finalErr))
		run.attachPublishedMaterial(callResult, runErr)
		callResult.refresh(run.fixtureDir)
	}()

	if err := notifyActionStarted(ctx, &run.transactionContext, resultStream); err != nil {
		return callResult, err
	}
	plan, err := run.prepare(ctx, policy)
	if err != nil {
		return callResult, err
	}
	run.plan = plan
	if err := policy.Capture(ctx, run.context()); err != nil {
		return callResult, err
	}
	completed = true
	return callResult, nil
}

func recordStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "passed"
}

func beginRecordTransaction(request RecordRequest) (transaction *recordTransaction, err error) {
	fixtureDir, err := CanonicalFixtureDir(request.FixtureDir)
	if err != nil {
		return nil, err
	}
	request.Args = normalizeFixtureArgs(fixtureDir, request.Args)
	allocation, err := lockFile(filepath.Join(fixtureDir, "locks", "record-start.lock"), true, true)
	if err != nil {
		return nil, err
	}
	defer allocation.Close()

	lock, exclusive, err := beginActionLock(fixtureDir)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = lock.Close()
		}
	}()

	fixture, err := OpenFixture(fixtureDir)
	if err != nil {
		return nil, err
	}
	origin, err := recordOrigin(fixtureDir, fixture, request.Connection)
	if err != nil {
		return nil, err
	}
	if origin == nil {
		if err := checkOfficialIdle(fixtureDir); err != nil {
			return nil, err
		}
	}
	plan, err := LoadPlan(fixture)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ConnectionIDs(fixture.Source, plan), request.Connection) {
		return nil, fmt.Errorf("undeclared record connection %s", request.Connection)
	}
	index, err := readIndex(IndexPath(fixtureDir))
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
		dir := ExecutionDir(fixtureDir, pending.ID)
		execution, err := ReadExecution(fixtureDir, fixture, filepath.Join(dir, "execution.json"))
		if err != nil {
			return nil, err
		}
		if hash, err := tool.FileDigest(filepath.Join(dir, "execution.json")); err != nil || hash != pending.DescriptorSHA256 {
			return nil, errors.New("record descriptor changed")
		}
		if !reflect.DeepEqual(execution.Artifact.OfficialOrigin, origin) || !slices.Equal(execution.Args, request.Args) || execution.Limits.TimeoutNS != int64(request.Timeout) {
			return nil, errors.New("record connection conflicts with execution target or timeout")
		}
		execution.RuntimeEnv, err = executionRuntimeEnvironment(fixture, "record", filepath.Dir(execution.Home))
		if err != nil {
			return nil, err
		}
		run = newTransactionContext(fixture, plan, *execution, fixtureDir)
	} else {
		if !exclusive {
			return nil, ErrBusy
		}
		run, err = createTransactionLocked(fixtureDir, "record", request.Args, nil, request.Timeout, 0, nil, nil, origin)
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
		proof := tool.Object{"schemaVersion": 1, "origin": origin, "connectionId": request.Connection, "executionId": run.execution.ExecutionID, "protocolPid": os.Getpid(), "supervisorPid": os.Getppid()}
		fixture.Tool.bindSHA256(proof)
		if err := writeExclusive(path, proof); err != nil {
			return nil, err
		}
		originReceiptPath = path
	}
	claimPath, err = claimTransactionConnection(fixtureDir, run.execution, request.Connection)
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
