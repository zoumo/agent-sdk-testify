package lifecycle

import (
	"context"
	"encoding/json"
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
			MaterialID:         r.execution.Artifact.ID,
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

func (r *recordTransaction) attachPublishedMaterial(result *RecordResult) {
	if result == nil {
		return
	}
	stable := filepath.Join(r.dir, "receipts", r.connection+".transport.json")
	if info, err := os.Lstat(stable); err == nil && info.Mode().IsRegular() {
		result.ReceiptPath = stable
	}
	if !r.publicationCommitted {
		return
	}
	result.PublicationCommitted = true
	result.MaterialID = r.execution.Artifact.ID
	material := cloneTransactionMaterial(r.execution.Artifact)
	result.Material = &material
}

func (r *recordTransaction) prepare(ctx context.Context, policy RecordPolicy) (plan tool.Object, err error) {
	lock, err := lockFile(filepath.Join(r.overlayRoot, "record-finalize.lock"), true, true)
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
	lock, lockErr := lockFile(filepath.Join(r.overlayRoot, "record-finalize.lock"), true, true)
	if lockErr != nil {
		// Without the finalization gate, close the execution with one terminal
		// index transition and do not attempt success or publication.
		return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, lockErr)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return r.finalizeOutcome(ctx, policy, stream, runErr)
}

func (r *recordTransaction) finalizeOutcome(ctx context.Context, policy RecordPolicy, stream *os.File, runErr error) error {
	// A connection can return before its peers publish the capture. Keep its
	// protocol receipt in the execution so that its immutable result survives
	// both a later capture move and a peer failure.
	if err := r.preserveConnectionReceipt(); err != nil {
		runErr = errors.Join(runErr, err)
	}
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
	if err := r.publish(*artifact); err != nil {
		if !r.publicationCommitted {
			return finishTransactionFailure(ctx, &r.transactionContext, stream, r.fail, runErr, err)
		}
		return errors.Join(err, writeTransactionResult(ctx, &r.transactionContext, stream, err))
	}
	return writeTransactionResult(ctx, &r.transactionContext, stream, nil)
}

func (r *recordTransaction) settleConnection(status string) (ready, passed bool, err error) {
	return settleTransactionConnection(r.fixtureDir, "record", r.execution.ExecutionID, r.connection, status)
}

func (r *recordTransaction) fail() error {
	var sourceErr error
	seed := filepath.Join(r.artifactRoot, "history", "seed")
	staging := filepath.Join(r.artifactRoot, "history-source")
	if _, err := os.Lstat(seed); err == nil {
		if _, err := os.Lstat(staging); os.IsNotExist(err) {
			sourceErr = os.Rename(seed, staging)
		} else if err == nil {
			sourceErr = os.RemoveAll(seed)
		} else {
			sourceErr = err
		}
		if sourceErr == nil {
			prefix := "executions/" + r.execution.ExecutionID + "/capture"
			sourceErr = history.RebindMaterialization(PackageRoot(r.fixtureDir, r.execution.Artifact.ID), r.dir, prefix+"/history/seed", prefix+"/history-source")
		}
	} else if !os.IsNotExist(err) {
		sourceErr = err
	}
	return errors.Join(sourceErr, failTransaction(r.fixture, r.fixtureDir, "record", r.execution.ExecutionID, r.overlayRoot, r.restoreRoutes))
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
	if err := r.commitPublication(artifact); err != nil {
		return err
	}
	return r.finishPublication()
}

func (r *recordTransaction) preserveConnectionReceipt() error {
	for _, c := range r.execution.Connections {
		if c.ID != r.connection {
			continue
		}
		data, err := os.ReadFile(c.Receipt)
		if os.IsNotExist(err) {
			return nil // failures before capture have no protocol receipt
		}
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(r.dir, "receipts", c.ID+".transport.json"), data, 0o600)
	}
	return errors.New("record connection absent from execution")
}

func (r *recordTransaction) commitPublication(artifact corpus.Material) error {
	if artifact.ID != r.execution.Artifact.ID || artifact.Path != r.artifactRoot {
		return errors.New("record material differs from its allocated package")
	}

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
	materialRoot := MaterialRoot(r.fixtureDir, artifact.ID)
	packageRoot := PackageRoot(r.fixtureDir, artifact.ID)
	stage := filepath.Join(packageRoot, ".material-staging")
	if _, err := os.Lstat(materialRoot); !os.IsNotExist(err) {
		return errors.New("material identity already exists")
	}
	captureRoot := artifact.Path
	files, err := history.HashTree(captureRoot)
	if err != nil {
		return err
	}
	delete(files, "material.json")
	artifact.Path = materialRoot
	if artifact.FormatVersion == 3 {
		artifact.Files = nil
	} else {
		artifact.Files = files
		artifact.FormatVersion = 2
	}
	if err := writeExclusive(filepath.Join(captureRoot, "material.json"), artifact); err != nil {
		return err
	}
	current, err := readCurrent(r.fixtureDir, r.fixture.FixtureID)
	if err != nil {
		return err
	}
	materialHash, err := tool.FileDigest(filepath.Join(captureRoot, "material.json"))
	if err != nil {
		return err
	}
	publication := &Publication{MaterialID: artifact.ID, ExecutionID: r.execution.ExecutionID, PreviousMaterialID: current.MaterialID, MaterialSHA256: materialHash, ManifestSHA256: artifact.ManifestSHA256, Source: "executions/" + r.execution.ExecutionID + "/capture", Staging: ".material-staging", Target: "material"}
	if err := updateIndex(r.fixtureDir, func(index *Index) error {
		for i := range index.Executions {
			if index.Executions[i].ID == r.execution.ExecutionID {
				index.Executions[i].Publishing = publication
				return nil
			}
		}
		return errors.New("record execution absent from index")
	}); err != nil {
		return err
	}
	// Register all locations and sealed hashes before moving the only capture.
	if err := os.Rename(captureRoot, stage); err != nil {
		return err
	}
	if err := os.Rename(stage, materialRoot); err != nil {
		return err
	}
	if err := history.RebindMaterialization(packageRoot, r.dir, "executions/"+r.execution.ExecutionID+"/capture/history/seed", "material/history/seed"); err != nil {
		return err
	}
	if _, err := inspectMaterial(r.fixture, artifact); err != nil {
		return err
	}
	// Register the sealed core before selection. The publication journal retains
	// ownership if a later control write fails.
	if err := updateIndex(r.fixtureDir, func(index *Index) error {
		for _, existing := range index.Artifacts {
			if existing.ID == artifact.ID {
				return errors.New("artifact identity already exists")
			}
		}
		index.Artifacts = append(index.Artifacts, artifact)
		return nil
	}); err != nil {
		return err
	}
	if err := writeCurrent(r.fixtureDir, r.fixture, artifact.ID); err != nil {
		return err
	}
	r.publicationCommitted = true
	r.execution.Artifact = artifact
	for i := range r.execution.Connections {
		c := &r.execution.Connections[i]
		c.Material, c.MaterialID = artifact.Path, artifact.ID
		c.PlanSHA256, c.ManifestSHA256 = artifact.PlanSHA256, artifact.ManifestSHA256
		recording, err := connectionRecordingPath(artifact.Path, c.ID)
		if err != nil {
			return err
		}
		c.RecordingSHA256, err = tool.FileDigest(recording)
		if err != nil {
			return err
		}
		c.Receipt = filepath.Join(filepath.Dir(recording), "transport.json")
	}
	return nil
}

func (r *recordTransaction) finishPublication() error {
	// Cleanup and delivery failures after selection retain the new current and
	// its exact identity. Never route them through the pre-publication failure path.
	return errors.Join(updateIndex(r.fixtureDir, func(index *Index) error {
		for i := range index.Executions {
			e := &index.Executions[i]
			if e.ID == r.execution.ExecutionID {
				e.ArtifactID = r.execution.Artifact.ID
				e.Status = "passed"
				e.UpdatedAt = now()
				return nil
			}
		}
		return errors.New("record execution absent from index")
	}), r.restoreRoutes(), scrubExecutionAuthentication(r.fixture, r.overlayRoot), RefreshRoutes(r.fixture))
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
		run.attachPublishedMaterial(callResult)
		callResult.refresh(run.fixtureDir)
		if run.publicationCommitted {
			callResult.ExecutionStatus = ExecutionStatusPassed
			callResult.ExecutionComplete = true
			callResult.ConnectionStatus = ConnectionStatusPassed
		}
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
		originBytes, err := json.MarshalIndent(origin, "", "  ")
		if err != nil {
			return nil, err
		}
		originHash := tool.DigestBytes(append(originBytes, '\n'))
		proof := tool.Object{"schemaVersion": 2, "originSHA256": originHash, "runId": run.execution.Artifact.OfficialOrigin.RunID, "connectionId": request.Connection, "executionId": run.execution.ExecutionID, "protocolPid": os.Getpid(), "supervisorPid": os.Getppid()}
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
