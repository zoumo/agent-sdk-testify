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
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

// MockRequest contains the immutable inputs shared by every connection of one
// mock execution.
type MockRequest struct {
	FixtureDir  string
	Connection  string
	ExecutionID string
	NativeArgs  []string
	Timeout     time.Duration
	ProgressFD  int
	ResultFD    int
	Artifact    corpus.Material
	RequireNew  bool
}

// MockPolicy supplies replay for one connection. The transaction owns every
// lock, index transition, route, receipt, notification, and closure step.
type MockPolicy interface {
	Replay(context.Context, MockContext) error
}

// MockStartedPolicy optionally observes the claimed connection after running
// evidence is durable and before protocol replay starts.
type MockStartedPolicy interface {
	MockPolicy
	Started(context.Context, MockContext) error
}

// MockContext is a read-only snapshot of transaction-owned state.
type MockContext struct {
	transactionContext
}

type mockTransaction struct {
	lockedTransaction
}

func (m *mockTransaction) context() MockContext {
	return MockContext{
		transactionContext: m.transactionContext,
	}
}

func (m *mockTransaction) result() *MockResult {
	id := m.connection
	if id == "" {
		id = "main"
	}
	result := &MockResult{
		ActionResult: ActionResult{
			FixtureID:        m.fixture.FixtureID,
			ExecutionID:      m.execution.ExecutionID,
			ConnectionID:     id,
			ConnectionStatus: ConnectionStatusUnknown,
			ExecutionStatus:  ExecutionStatusUnknown,
			ExecutionPath:    filepath.Join(m.dir, "execution.json"),
			ResultPath:       filepath.Join(m.dir, "receipts", id+".result.json"),
		},
	}
	material := cloneTransactionMaterial(m.execution.Artifact)
	result.Material = &material
	for _, connection := range m.execution.Connections {
		if connection.ID == id {
			result.ReceiptPath = connection.Receipt
			result.ProcessReceiptPath = connection.ProcessReceipt
			break
		}
	}
	return result
}

// finalize serializes completion across declared connections and owns every
// state transition from process evidence through result delivery.
func (m *mockTransaction) finalize(ctx context.Context, stream *os.File, replayErr error) (err error) {
	lock, lockErr := lockFile(filepath.Join(m.dir, "mock-finalize.lock"), true, true)
	if lockErr != nil {
		processErr := m.writeProcessReceipt(replayErr)
		return finishTransactionFailure(ctx, &m.transactionContext, stream, m.fail, replayErr, processErr, lockErr)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return m.finalizeOutcome(ctx, stream, replayErr)
}

func (m *mockTransaction) finalizeOutcome(ctx context.Context, stream *os.File, replayErr error) error {
	replayErr = errors.Join(replayErr, contextFailure(ctx))
	processErr := m.writeProcessReceipt(replayErr)
	err := errors.Join(replayErr, processErr)
	status := "passed"
	if err != nil {
		status = "failed"
	}
	ready, allPassed, settleErr := m.settleConnection(status)
	if settleErr != nil {
		return finishTransactionFailure(ctx, &m.transactionContext, stream, m.fail, err, settleErr)
	}
	if !ready {
		return writeTransactionResult(ctx, &m.transactionContext, stream, err)
	}
	if !allPassed {
		return finishTransactionFailure(ctx, &m.transactionContext, stream, m.fail, err, errors.New("mock execution has failed connections"))
	}
	if contextErr := contextFailure(ctx); contextErr != nil {
		if _, _, settleErr := m.settleConnection("failed"); settleErr != nil {
			return finishTransactionFailure(ctx, &m.transactionContext, stream, m.fail, contextErr, settleErr)
		}
		return finishTransactionFailure(ctx, &m.transactionContext, stream, m.fail, contextErr)
	}
	if err := m.pass(); err != nil {
		return finishTransactionFailure(ctx, &m.transactionContext, stream, m.fail, err)
	}
	return writeTransactionResult(ctx, &m.transactionContext, stream, nil)
}

func (m *mockTransaction) writeProcessReceipt(replayErr error) error {
	path := m.context().ProcessReceipt()
	if path == "" {
		if m.context().connectionTransport() == ConnectionTransportInProcess {
			return nil
		}
		return errors.New("mock process receipt path is absent")
	}
	process := tool.Object{
		"schemaVersion": 1,
		"fixtureId":     m.fixture.FixtureID,
		"executionId":   m.execution.ExecutionID,
		"connectionId":  m.connection,
		"status":        "failed",
		"exitCode":      -1,
		"toolVersion":   version.String(),
	}
	if self, toolVersion, hash, err := SelfCommand(); err == nil {
		process["tool"] = self
		process["toolVersion"] = toolVersion
		process["toolSHA256"] = hash
	}
	if replayErr == nil {
		process["status"], process["exitCode"] = "passed", 0
	} else {
		process["exitCode"], process["error"] = 1, replayErr.Error()
		if class := FailureClassOf(replayErr); class != "" {
			process["failureClass"] = string(class)
		}
	}
	return tool.WriteJSON(path, process)
}

func (m *mockTransaction) settleConnection(status string) (ready, passed bool, err error) {
	return settleTransactionConnection(m.fixtureDir, "mock", m.execution.ExecutionID, m.connection, status)
}

func (m *mockTransaction) pass() error {
	if err := m.restoreRoutes(); err != nil {
		return ClassifyFailure(FailureClassCleanup, err)
	}
	if err := scrubExecutionAuthentication(m.fixture, m.overlayRoot); err != nil {
		return ClassifyFailure(FailureClassCleanup, err)
	}
	return updateIndex(m.fixtureDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != m.execution.ExecutionID {
				continue
			}
			execution.Status = "passed"
			execution.UpdatedAt = now()
			return nil
		}
		return errors.New("mock execution absent from index")
	})
}

func (m *mockTransaction) fail() error {
	return failTransaction(m.fixture, m.fixtureDir, "mock", m.execution.ExecutionID, m.overlayRoot, m.restoreRoutes)
}

func (m *mockTransaction) restoreRoutes() error {
	var active tool.Object
	err := tool.ReadJSON(filepath.Join(m.fixtureDir, "official-active.json"), &active)
	if err == nil {
		if active["fixtureId"] == m.fixture.FixtureID && active["mode"] == "verify" {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return restoreRouteSlots(m.fixture, "mock")
}

// RunMock owns one mock connection from allocation or join through result
// delivery and lock release.
func RunMock(ctx context.Context, request MockRequest, policy MockPolicy) (result *MockResult, runErr error) {
	if policy == nil {
		return nil, errors.New("mock policy is required")
	}
	if request.Connection == "" {
		request.Connection = "main"
	}
	if request.Timeout < 0 {
		return nil, errors.New("mock timeout must be nonnegative")
	}
	if request.ProgressFD != -1 && request.ProgressFD < 3 {
		return nil, errors.New("progress-fd must be >=3")
	}
	resultStream, err := ResultStream(request.ResultFD)
	if err != nil {
		return nil, err
	}
	if resultStream != nil {
		defer resultStream.Close()
	}
	run, err := beginMockTransaction(request)
	if err != nil {
		return nil, err
	}
	result = run.result()
	defer func() { runErr = errors.Join(runErr, run.releaseLock()) }()
	if run.execution.Limits.TimeoutNS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(run.execution.Limits.TimeoutNS))
		defer cancel()
	}
	completed := false
	defer func() {
		finalErr := runErr
		if !completed && finalErr == nil {
			finalErr = errors.New("mock transaction aborted")
		}
		runErr = errors.Join(runErr, run.finalize(ctx, resultStream, finalErr))
		if refreshErr := result.refresh(run.fixtureDir); refreshErr != nil {
			runErr = errors.Join(runErr, ClassifyFailure(FailureClassInternal, refreshErr))
		}
	}()

	if err := notifyActionStarted(ctx, &run.transactionContext, resultStream); err != nil {
		return result, err
	}
	if policyWithStart, ok := policy.(MockStartedPolicy); ok {
		if err := policyWithStart.Started(ctx, run.context()); err != nil {
			return result, err
		}
	}
	if err := policy.Replay(ctx, run.context()); err != nil {
		return result, err
	}
	runErr = contextFailure(ctx)
	completed = runErr == nil
	return result, runErr
}

func beginMockTransaction(request MockRequest) (transaction *mockTransaction, err error) {
	fixtureDir, err := CanonicalFixtureDir(request.FixtureDir)
	if err != nil {
		return nil, err
	}
	allocation, err := lockFile(filepath.Join(fixtureDir, "locks", "mock-start.lock"), true, true)
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
	route, routeAvailable := fixture.Routes["mock"]
	if !routeAvailable || !route.Enabled {
		return nil, errors.New("mock is not available for this fixture")
	}
	current, err := LatestArtifact(fixtureDir, fixture)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*current, request.Artifact) {
		return nil, errors.New("current recording changed before execution")
	}
	if _, err := inspectMaterial(fixture, *current); err != nil {
		return nil, err
	}
	if _, err := executionConnections(fixtureDir, "mock", current.Connections, current, route); err != nil {
		return nil, err
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
		err = errors.Join(err, failTransaction(run.fixture, fixtureDir, "mock", run.execution.ExecutionID, run.overlayRoot, func() error {
			return restoreRouteSlots(run.fixture, "mock")
		}))
	}()
	var pending *IndexExecution
	if request.ExecutionID != "" {
		pending, err = findIndexExecution(index, request.ExecutionID)
		if err != nil {
			return nil, err
		}
		if pending == nil {
			return nil, fmt.Errorf("mock execution %s is absent from index", request.ExecutionID)
		}
		if pending.Command != "mock" {
			return nil, fmt.Errorf("execution %s is not a mock execution", request.ExecutionID)
		}
		if pending.Status != string(ExecutionStatusOpen) {
			return nil, fmt.Errorf("mock execution %s is already complete", request.ExecutionID)
		}
		if other := pendingExecution(index); other != nil && other.ID != pending.ID {
			return nil, fmt.Errorf("fixture has unfinished execution %s", other.ID)
		}
	} else {
		if _, err := findIndexExecution(index, ""); err != nil {
			return nil, err
		}
		pending = pendingExecution(index)
		if pending != nil && request.RequireNew {
			return nil, fmt.Errorf("fixture has unfinished execution %s", pending.ID)
		}
	}
	if pending != nil {
		if pending.Command != "mock" {
			return nil, ErrBusy
		}
		dir := ExecutionDir(fixtureDir, pending.ID)
		var execution Execution
		if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
			return nil, err
		}
		if err := execution.Validate(*fixture); err != nil {
			return nil, err
		}
		if err := validateMockExecutionContract(fixture, fixtureDir, index, *pending, execution); err != nil {
			return nil, err
		}
		if hash, err := tool.FileDigest(filepath.Join(dir, "execution.json")); err != nil || hash != pending.DescriptorSHA256 {
			return nil, errors.New("mock descriptor changed")
		}
		statuses, err := reportConnectionStatuses(*pending, execution)
		if err != nil {
			return nil, err
		}
		if err := validateExecutionStatus(ExecutionStatus(pending.Status), statuses, false); err != nil {
			return nil, err
		}
		if connectionStarted(pending, request.Connection) {
			return nil, fmt.Errorf("mock connection %s already started for execution %s", request.Connection, pending.ID)
		}
		if err := validateMockJoin(execution, request.NativeArgs, request.Timeout, request.ProgressFD, &request.Artifact); err != nil {
			return nil, err
		}
		plan, err := LoadPlan(fixture)
		if err != nil {
			return nil, err
		}
		run = newTransactionContext(fixture, plan, execution, fixtureDir)
	} else {
		if !exclusive {
			return nil, ErrBusy
		}
		if !slices.Contains(current.Connections, request.Connection) {
			return nil, fmt.Errorf("undeclared mock connection %s", request.Connection)
		}
		commandArgs := append([]string{"--fixture-dir", fixtureDir, "--connection", request.Connection}, request.NativeArgs...)
		run, err = createTransactionLocked(fixtureDir, "mock", commandArgs, request.NativeArgs, request.Timeout, request.ProgressFD, current, nil, nil)
		if err != nil {
			return nil, err
		}
		allocated = true
	}

	run.connection = request.Connection
	claimed := false
	claimPath := ""
	defer func() {
		if ok || allocated || !claimed {
			return
		}
		err = errors.Join(err, removeFileIfExists(claimPath))
	}()
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
	return &mockTransaction{
		lockedTransaction: lockedTransaction{
			transactionContext: *run,
			lock:               lock,
		},
	}, nil
}
