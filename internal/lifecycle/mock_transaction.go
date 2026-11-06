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
	SessionDir string
	Connection string
	NativeArgs []string
	Timeout    time.Duration
	ProgressFD int
	ResultFD   int
	Artifact   corpus.Material
}

// MockPolicy supplies replay for one connection. The transaction owns every
// lock, index transition, route, receipt, notification, and closure step.
type MockPolicy interface {
	Replay(context.Context, MockContext) error
}

// MockContext is a read-only snapshot of transaction-owned state.
type MockContext struct {
	transactionContext
}

type mockTransaction struct {
	transactionContext
	lock *Lock
}

func (m *mockTransaction) context() MockContext {
	return MockContext{
		transactionContext: transactionContext{
			session:      m.session,
			plan:         m.plan,
			execution:    m.execution,
			connection:   m.connection,
			sessionDir:   m.sessionDir,
			dir:          m.dir,
			overlayRoot:  m.overlayRoot,
			artifactRoot: m.artifactRoot,
		},
	}
}

func (m *mockTransaction) close() error {
	if m == nil {
		return nil
	}
	return m.lock.Close()
}

func (m *mockTransaction) resultState() actionResultState {
	return actionResultState{
		session:    m.session,
		execution:  m.execution,
		connection: m.connection,
		dir:        m.dir,
	}
}

// finalize serializes completion across declared connections and owns every
// state transition from process evidence through result delivery.
func (m *mockTransaction) finalize(ctx context.Context, stream *os.File, replayErr error) (err error) {
	lock, lockErr := lockFile(filepath.Join(m.dir, "mock-finalize.lock"), true, true)
	if lockErr != nil {
		return m.finalizeOutcome(ctx, stream, replayErr, lockErr)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return m.finalizeOutcome(ctx, stream, replayErr, nil)
}

func (m *mockTransaction) finalizeOutcome(ctx context.Context, stream *os.File, replayErr, finalizationErr error) error {
	processErr := m.writeProcessReceipt(replayErr)
	err := errors.Join(replayErr, processErr, finalizationErr)
	status := "passed"
	if err != nil {
		status = "failed"
	}
	ready, allPassed, settleErr := m.settleConnection(status)
	if settleErr != nil {
		err = errors.Join(err, settleErr, m.fail())
		return errors.Join(err, m.writeResult(ctx, stream, err))
	}
	if !ready {
		return m.writeResult(ctx, stream, err)
	}
	if !allPassed {
		err = errors.Join(err, errors.New("mock execution has failed connections"), m.fail())
		return errors.Join(err, m.writeResult(ctx, stream, err))
	}
	if err := m.pass(); err != nil {
		err = errors.Join(err, m.fail())
		return errors.Join(err, m.writeResult(ctx, stream, err))
	}
	return m.writeResult(ctx, stream, nil)
}

func (m *mockTransaction) writeProcessReceipt(replayErr error) error {
	path := m.context().ProcessReceipt()
	if path == "" {
		return errors.New("mock process receipt path is absent")
	}
	process := tool.Object{
		"schemaVersion": 1,
		"sessionId":     m.session.SessionID,
		"executionId":   m.execution.ExecutionID,
		"connectionId":  m.connection,
		"status":        "failed",
		"exitCode":      -1,
		"cwd":           m.execution.Workspace,
		"toolVersion":   version.Version,
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
	}
	return tool.WriteJSON(path, process)
}

func (m *mockTransaction) settleConnection(status string) (ready, passed bool, err error) {
	return settleTransactionConnection(m.sessionDir, "mock", m.execution.ExecutionID, m.connection, status)
}

func (m *mockTransaction) pass() error {
	if err := m.restoreRoutes(); err != nil {
		return err
	}
	if err := scrubExecutionAuthentication(m.session, m.overlayRoot); err != nil {
		return err
	}
	return updateIndex(m.sessionDir, func(index *Index) error {
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
	return failTransaction(m.session, m.sessionDir, "mock", m.execution.ExecutionID, m.overlayRoot, m.restoreRoutes)
}

func (m *mockTransaction) abort() error {
	return failTransaction(m.session, m.sessionDir, "mock", m.execution.ExecutionID, m.overlayRoot, func() error {
		return restoreRouteSlots(m.session, "mock")
	})
}

func (m *mockTransaction) restoreRoutes() error {
	var active tool.Object
	err := tool.ReadJSON(filepath.Join(m.sessionDir, "official-active.json"), &active)
	if err == nil {
		if active["sessionId"] == m.session.SessionID && active["mode"] == "verify" {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return restoreRouteSlots(m.session, "mock")
}

func (m *mockTransaction) writeResult(ctx context.Context, stream *os.File, runErr error) error {
	if runErr != nil {
		if err := tool.WriteJSON(filepath.Join(m.dir, "receipts", m.connection+".failure.json"), tool.Object{"status": "failed", "error": runErr.Error()}); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return writeActionResult(ctx, m.resultState(), stream, runErr)
}

// RunMock owns one mock connection from allocation or join through result
// delivery and lock release.
func RunMock(ctx context.Context, request MockRequest, policy MockPolicy) (runErr error) {
	if policy == nil {
		return errors.New("mock policy is required")
	}
	if request.Connection == "" {
		request.Connection = "main"
	}
	if request.Timeout < 0 {
		return errors.New("mock timeout must be nonnegative")
	}
	if request.ProgressFD != -1 && request.ProgressFD < 3 {
		return errors.New("progress-fd must be >=3")
	}
	result, err := ResultStream(request.ResultFD)
	if err != nil {
		return err
	}
	if result != nil {
		defer result.Close()
	}
	run, err := beginMockTransaction(request)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, run.close()) }()
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
		runErr = errors.Join(runErr, run.finalize(ctx, result, finalErr))
	}()

	if err := notifyActionStarted(ctx, run.resultState(), result); err != nil {
		return err
	}
	if err := policy.Replay(ctx, run.context()); err != nil {
		return err
	}
	completed = true
	return nil
}

func beginMockTransaction(request MockRequest) (transaction *mockTransaction, err error) {
	sessionDir, err := canonicalSessionDir(request.SessionDir)
	if err != nil {
		return nil, err
	}
	allocation, err := lockFile(filepath.Join(sessionDir, "locks", "mock-start.lock"), true, true)
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
	route, routeAvailable := session.Routes["mock"]
	if !routeAvailable || !route.Enabled {
		return nil, errors.New("mock is not available for this session")
	}
	current, err := LatestArtifact(sessionDir, session)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*current, request.Artifact) {
		return nil, errors.New("current recording changed before execution")
	}
	if _, err := executionConnections(sessionDir, "mock", current.Connections, current, route); err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}

	var action *ActionRun
	allocated := false
	defer func() {
		if ok || !allocated || action == nil {
			return
		}
		aborted := &mockTransaction{
			transactionContext: transactionContext{
				session:      action.Session,
				plan:         action.Plan,
				execution:    action.Execution,
				connection:   request.Connection,
				sessionDir:   sessionDir,
				dir:          action.Dir,
				overlayRoot:  action.OverlayRoot,
				artifactRoot: action.ArtifactRoot,
			},
		}
		err = errors.Join(err, aborted.abort())
	}()
	if pending := pendingExecution(index); pending != nil {
		if pending.Command != "mock" || connectionStarted(pending, request.Connection) {
			if pending.Command == "mock" {
				return nil, fmt.Errorf("mock connection %s already started for execution %s", request.Connection, pending.ID)
			}
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
			return nil, errors.New("mock descriptor changed")
		}
		if err := validateMockJoin(execution, request.NativeArgs, request.Timeout, request.ProgressFD, &request.Artifact); err != nil {
			return nil, err
		}
		plan, err := LoadPlan(session)
		if err != nil {
			return nil, err
		}
		action = &ActionRun{Session: session, Plan: plan, Execution: execution, Dir: dir, OverlayRoot: filepath.Dir(execution.Home), ArtifactRoot: execution.ArtifactRoot, lock: lock}
	} else {
		if !exclusive {
			return nil, ErrBusy
		}
		if !slices.Contains(current.Connections, request.Connection) {
			return nil, fmt.Errorf("undeclared mock connection %s", request.Connection)
		}
		commandArgs := append([]string{"--session-dir", sessionDir, "--connection", request.Connection}, request.NativeArgs...)
		action, err = createActionLocked(sessionDir, "mock", commandArgs, request.NativeArgs, request.Timeout, request.ProgressFD, current, nil, lock, nil)
		if err != nil {
			return nil, err
		}
		allocated = true
	}

	action.Connection = request.Connection
	claimPath, err := connectionClaimPath(action.Execution, request.Connection)
	if err != nil {
		return nil, err
	}
	claimed := false
	defer func() {
		if ok || allocated || !claimed {
			return
		}
		err = errors.Join(err, removeFileIfExists(claimPath))
	}()
	if err := createConnectionClaim(claimPath); err != nil {
		return nil, fmt.Errorf("mock connection %s already started: %w", request.Connection, err)
	}
	claimed = true
	if err := markConnection(sessionDir, action.Execution.ExecutionID, request.Connection, "running", "open"); err != nil {
		return nil, err
	}
	if exclusive {
		if err := lock.Downgrade(); err != nil {
			return nil, err
		}
	}
	ok = true
	return &mockTransaction{
		transactionContext: transactionContext{
			session:      action.Session,
			plan:         action.Plan,
			execution:    action.Execution,
			connection:   request.Connection,
			sessionDir:   sessionDir,
			dir:          action.Dir,
			overlayRoot:  action.OverlayRoot,
			artifactRoot: action.ArtifactRoot,
		},
		lock: lock,
	}, nil
}
