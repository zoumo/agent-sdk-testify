package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type mockPolicyFunc func(context.Context, MockContext) error

func (p mockPolicyFunc) Replay(ctx context.Context, run MockContext) error {
	err := p(ctx, run)
	if receiptErr := writeMockPolicyReceipt(run, err); receiptErr != nil {
		return errors.Join(err, receiptErr)
	}
	return err
}

func writeMockPolicyReceipt(run MockContext, replayErr error) error {
	path := run.ConnectionReceipt()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	artifact := run.Artifact()
	connectionPath, err := connectionRecordingPath(artifact.Path, run.Connection())
	if err != nil {
		return err
	}
	recordingSHA256, err := tool.FileDigest(connectionPath)
	if err != nil {
		return err
	}
	frames, err := recordingFrameCount(connectionPath, run.Fixture().AgentType)
	if err != nil {
		return err
	}
	transport := ""
	for _, connection := range run.Execution().Connections {
		if connection.ID == run.Connection() {
			transport = connection.Transport
			break
		}
	}
	receipt := tool.Object{
		"schemaVersion":   2,
		"materialId":      run.Artifact().ID,
		"executionId":     run.Execution().ExecutionID,
		"connectionId":    run.Connection(),
		"version":         1,
		"status":          "failed",
		"peer":            "test mock policy",
		"transport":       transport,
		"provider":        run.Fixture().AgentType,
		"recording":       run.MaterialReference(),
		"planSHA256":      artifact.PlanSHA256,
		"recordingSHA256": recordingSHA256,
		"frames":          frames,
		"nativeLaunched":  false,
		"fullConsumption": replayErr == nil,
		"clientEOF":       replayErr == nil,
		"cleanupComplete": true,
	}
	if replayErr == nil {
		receipt["status"] = "passed"
	} else {
		receipt["error"] = replayErr.Error()
		cause := replayErr
		if FailureClassOf(cause) == "" {
			cause = ClassifyFailure(FailureClassInternal, cause)
		}
		receipt["failureClass"] = string(FailureClassOf(cause))
	}
	return tool.WriteJSON(path, receipt)
}

func prepareMockTransactionFixture(t *testing.T, caseID string) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	fixtureDir := filepath.Join(root, "fixture")
	_, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Agent: "codex", Case: caseID})
	require.NoError(t, err)
	return fixtureDir
}

func mockRequest(t *testing.T, fixtureDir, connection string, fd int) MockRequest {
	t.Helper()
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	artifact, err := LatestArtifact(fixtureDir, fixture)
	require.NoError(t, err)
	return MockRequest{
		FixtureDir: fixtureDir,
		Connection: connection,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		Timeout:    time.Second,
		ProgressFD: -1,
		ResultFD:   fd,
		Artifact:   *artifact,
	}
}

func assertMockRouteAtTemplate(t *testing.T, fixtureDir string) {
	t.Helper()
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	workspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "mock", "workspace"), workspace)
}

func TestMockTransactionCommitsFinalReplay(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	currentBefore, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	result, fd := recordResultFile(t)
	request := mockRequest(t, fixtureDir, "main", fd)
	replays := 0

	mockResult, err := RunMock(t.Context(), request, mockPolicyFunc(func(_ context.Context, run MockContext) error {
		replays++
		require.Equal(t, "main", run.Connection())
		require.Equal(t, "mock", run.Execution().Command)
		require.Equal(t, run.Execution().Artifact, run.Artifact())
		require.DirExists(t, run.Workspace())
		require.NotEmpty(t, run.ConnectionReceipt())
		require.NotEmpty(t, run.ProcessReceipt())
		require.NotEmpty(t, run.ArtifactRoot())

		execution := run.Execution()
		execution.Connections[0].Receipt = filepath.Join(t.TempDir(), "tampered.json")
		require.NotEqual(t, execution.Connections[0].Receipt, run.ConnectionReceipt())

		rows := readRecordResultsIfAny(t, result)
		require.Len(t, rows, 1)
		require.Equal(t, "running", rows[0]["status"])
		require.NoFileExists(t, run.ProcessReceipt())
		require.NoError(t, os.WriteFile(filepath.Join(execution.Config, "auth.json"), []byte("transient\n"), 0o600))
		return nil
	}))
	require.NoError(t, err)
	require.Equal(t, 1, replays)

	rows := readRecordResults(t, result)
	require.Equal(t, "running", rows[0]["status"])
	require.Equal(t, "passed", rows[1]["status"])
	require.Equal(t, rows[0]["executionId"], rows[1]["executionId"])
	require.NotNil(t, rows[1]["material"])

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.NotNil(t, mockResult)
	require.Equal(t, request.Artifact, *mockResult.Material)
	require.Equal(t, index.Executions[0].ID, mockResult.ExecutionID)
	require.Equal(t, "main", mockResult.ConnectionID)
	require.Equal(t, ConnectionStatusPassed, mockResult.ConnectionStatus)
	require.Equal(t, ExecutionStatusPassed, mockResult.ExecutionStatus)
	require.True(t, mockResult.ExecutionComplete)
	require.FileExists(t, mockResult.ExecutionPath)
	require.FileExists(t, mockResult.ResultPath)
	require.Equal(t, "passed", index.Executions[0].Status)
	execution := readRecordExecution(t, fixtureDir, index.Executions[0].ID)
	require.Equal(t, "passed", index.Executions[0].Connections[0].Status)
	require.NoFileExists(t, filepath.Join(execution.Config, "auth.json"))
	require.NoFileExists(t, filepath.Join(executionDirForTest(fixtureDir, execution), "receipts", "main.failure.json"))

	process := tool.Object{}
	require.NoError(t, tool.ReadJSON(execution.Connections[0].ProcessReceipt, &process))
	require.Equal(t, "passed", process["status"])
	require.EqualValues(t, 0, process["exitCode"])
	requireV1ProcessReceiptWithoutLocation(t, process)

	currentAfter, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, currentBefore, currentAfter)
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestMockTransactionFailurePreservesCurrentAndEvidence(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	currentBefore, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	materialsBefore, err := os.ReadFile(MaterialIndexPath(fixtureDir))
	require.NoError(t, err)
	result, fd := recordResultFile(t)
	replayErr := errors.New("synthetic replay failure")

	_, err = RunMock(t.Context(), mockRequest(t, fixtureDir, "main", fd), mockPolicyFunc(func(_ context.Context, run MockContext) error {
		require.NoError(t, os.WriteFile(filepath.Join(run.Execution().Config, "auth.json"), []byte("transient\n"), 0o600))
		return replayErr
	}))
	require.ErrorIs(t, err, replayErr)

	rows := readRecordResults(t, result)
	require.Equal(t, "running", rows[0]["status"])
	require.Equal(t, "failed", rows[1]["status"])
	require.Contains(t, rows[1]["error"], replayErr.Error())

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Equal(t, "failed", index.Executions[0].Connections[0].Status)
	execution := readRecordExecution(t, fixtureDir, index.Executions[0].ID)
	report, err := ReadMockExecutionReport(fixtureDir, execution.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusFailed, report.Status)
	require.False(t, report.Connections[0].FullConsumption)
	require.Contains(t, report.Connections[0].Error, replayErr.Error())
	require.Error(t, ValidateMockConnection("codex", execution, execution.Connections[0]))
	executionDir := executionDirForTest(fixtureDir, execution)
	require.NoFileExists(t, filepath.Join(execution.Config, "auth.json"))

	process := tool.Object{}
	require.NoError(t, tool.ReadJSON(execution.Connections[0].ProcessReceipt, &process))
	require.Equal(t, "failed", process["status"])
	require.EqualValues(t, 1, process["exitCode"])
	require.Contains(t, process["error"], replayErr.Error())
	requireV1ProcessReceiptWithoutLocation(t, process)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Equal(t, "failed", failure["status"])
	require.Contains(t, failure["error"], replayErr.Error())

	currentAfter, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, currentBefore, currentAfter)
	materialsAfter, err := os.ReadFile(MaterialIndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, materialsBefore, materialsAfter)
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestMockTransactionJoinsMultiPeerExecution(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-032")
	request := mockRequest(t, fixtureDir, "main", 0)
	var executionID, overlayRoot string

	mainResult, mainFD := recordResultFile(t)
	request.ResultFD = mainFD
	_, err := RunMock(t.Context(), request, mockPolicyFunc(func(_ context.Context, run MockContext) error {
		executionID, overlayRoot = run.Execution().ExecutionID, run.ExecutionDir()
		return nil
	}))
	require.NoError(t, err)
	mainRows := readRecordResults(t, mainResult)
	require.Equal(t, "passed", mainRows[1]["status"])
	require.NotNil(t, mainRows[1]["material"])

	openIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, openIndex.Executions, 1)
	require.Equal(t, "open", openIndex.Executions[0].Status)
	require.Equal(t, "passed", openIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "pending", openIndex.Executions[0].Connections[1].Status)
	report, err := ReadMockExecutionReport(fixtureDir, executionID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusOpen, report.Status)
	require.False(t, report.Complete)
	require.Equal(t, ConnectionStatusPassed, report.Connections[0].Status)
	require.Equal(t, ConnectionStatusPending, report.Connections[1].Status)
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	openWorkspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.NotEqual(t, filepath.Join(fixture.ResourceRoot, "templates", "mock", "workspace"), openWorkspace)

	probeResult, probeFD := recordResultFile(t)
	request.Connection, request.ResultFD = "probe", probeFD
	_, err = RunMock(t.Context(), request, mockPolicyFunc(func(_ context.Context, run MockContext) error {
		require.Equal(t, executionID, run.Execution().ExecutionID)
		require.Equal(t, overlayRoot, run.ExecutionDir())
		require.Equal(t, "probe", run.Connection())
		return nil
	}))
	require.NoError(t, err)
	probeRows := readRecordResults(t, probeResult)
	require.Equal(t, "passed", probeRows[1]["status"])
	require.NotNil(t, probeRows[1]["material"])
	require.Equal(t, mainRows[0]["executionId"], probeRows[0]["executionId"])

	closedIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, closedIndex.Executions, 1)
	require.Equal(t, "passed", closedIndex.Executions[0].Status)
	require.Equal(t, "passed", closedIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "passed", closedIndex.Executions[0].Connections[1].Status)
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestMockTransactionFinalPeerClosesFailedExecution(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-032")
	request := mockRequest(t, fixtureDir, "main", 0)
	mainResult, mainFD := recordResultFile(t)
	request.ResultFD = mainFD
	mainErr := errors.New("main replay failed")
	_, err := RunMock(t.Context(), request, mockPolicyFunc(func(context.Context, MockContext) error {
		return mainErr
	}))
	require.ErrorIs(t, err, mainErr)
	require.Equal(t, "failed", readRecordResults(t, mainResult)[1]["status"])

	openIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "open", openIndex.Executions[0].Status)
	require.Equal(t, "pending", openIndex.Executions[0].Connections[1].Status)

	probeResult, probeFD := recordResultFile(t)
	request.Connection, request.ResultFD = "probe", probeFD
	_, err = RunMock(t.Context(), request, mockPolicyFunc(func(context.Context, MockContext) error {
		return nil
	}))
	require.EqualError(t, err, "mock execution has failed connections")
	probeRows := readRecordResults(t, probeResult)
	require.Equal(t, "failed", probeRows[1]["status"])
	require.Contains(t, probeRows[1]["error"], "mock execution has failed connections")

	closedIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "failed", closedIndex.Executions[0].Status)
	require.Equal(t, "failed", closedIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "passed", closedIndex.Executions[0].Connections[1].Status)
	report, err := ReadMockExecutionReport(fixtureDir, closedIndex.Executions[0].ID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusFailed, report.Status)
	require.True(t, report.Complete)
	require.Equal(t, ConnectionStatusFailed, report.Connections[0].Status)
	require.Equal(t, ConnectionStatusPassed, report.Connections[1].Status)
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func beginActiveMockVerify(t *testing.T, fixtureDir string) *OfficialRun {
	t.Helper()
	activity, err := BeginOfficial(fixtureDir, "verify")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = activity.Complete(tool.Object{"mode": "verify", "status": "passed", "cleanupComplete": true})
	})
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	overlay := filepath.Join(t.TempDir(), "official-mock-overlay")
	require.NoError(t, tool.CopyTree(filepath.Join(fixture.ResourceRoot, "templates", "mock"), overlay))
	require.NoError(t, bindRouteSlots(filepath.Join(fixture.ResourceRoot, "routes", "mock"), overlay))
	return activity
}

func TestMockTransactionPreservesRouteDuringActiveOfficialVerify(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	activity := beginActiveMockVerify(t, fixtureDir)
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)

	_, fd := recordResultFile(t)
	workspaceAtReplay := ""
	_, err = RunMock(t.Context(), mockRequest(t, fixtureDir, "main", fd), mockPolicyFunc(func(context.Context, MockContext) error {
		execution, err := readIndex(IndexPath(fixtureDir))
		require.NoError(t, err)
		workspaceAtReplay = readRecordExecution(t, fixtureDir, execution.Executions[0].ID).Workspace
		return nil
	}))
	require.NoError(t, err)
	workspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, workspaceAtReplay, workspace)
	require.NoError(t, activity.RestoreMockRoute())
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestMockTransactionFailurePreservesRouteDuringActiveOfficialVerify(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	activity := beginActiveMockVerify(t, fixtureDir)

	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	replayErr := errors.New("synthetic replay failure")
	workspaceAtReplay := ""
	_, err = RunMock(t.Context(), mockRequest(t, fixtureDir, "main", 0), mockPolicyFunc(func(context.Context, MockContext) error {
		bound, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
		require.NoError(t, err)
		require.NotEqual(t, filepath.Join(fixture.ResourceRoot, "templates", "mock", "workspace"), bound)
		workspaceAtReplay = bound
		return replayErr
	}))
	require.ErrorIs(t, err, replayErr)
	workspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, workspaceAtReplay, workspace)
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.NoError(t, activity.RestoreMockRoute())
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestMockTransactionReleasesLockWhenFinalizationFails(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	result, fd := recordResultFile(t)
	_, err := RunMock(t.Context(), mockRequest(t, fixtureDir, "main", fd), mockPolicyFunc(func(_ context.Context, run MockContext) error {
		return os.Mkdir(run.ProcessReceipt(), 0o700)
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a directory")

	rows := readRecordResults(t, result)
	require.Equal(t, "failed", rows[1]["status"])
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "failed", index.Executions[0].Status)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[0].ID), "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], "is a directory")
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestMockTransactionClosesWhenFinalizationLockCannotOpen(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	result, fd := recordResultFile(t)
	_, err := RunMock(t.Context(), mockRequest(t, fixtureDir, "main", fd), mockPolicyFunc(func(_ context.Context, run MockContext) error {
		return os.Mkdir(filepath.Join(run.OverlayRoot(), "mock-finalize.lock"), 0o700)
	}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a directory")

	rows := readRecordResults(t, result)
	require.Equal(t, "failed", rows[1]["status"])
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Equal(t, "failed", index.Executions[0].Connections[0].Status)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[0].ID), "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], "is a directory")
	execution := readRecordExecution(t, fixtureDir, index.Executions[0].ID)
	process := tool.Object{}
	require.NoError(t, tool.ReadJSON(execution.Connections[0].ProcessReceipt, &process))
	require.Equal(t, "passed", process["status"])
	requireV1ProcessReceiptWithoutLocation(t, process)
	assertMockRouteAtTemplate(t, fixtureDir)
	assertRecordLockAvailable(t, fixtureDir)
}

func requireV1ProcessReceiptWithoutLocation(t *testing.T, process tool.Object) {
	t.Helper()
	require.EqualValues(t, 1, process["schemaVersion"])
	require.NotContains(t, process, "cwd")
	require.NotContains(t, process, "workspace")
}

func TestMockTransactionRejectsDuplicateAndIncompatibleJoins(t *testing.T) {
	fixtureDir := prepareMockTransactionFixture(t, "SDK-CASE-032")
	request := mockRequest(t, fixtureDir, "main", 0)
	_, fd := recordResultFile(t)
	request.ResultFD = fd
	_, err := RunMock(t.Context(), request, mockPolicyFunc(func(context.Context, MockContext) error {
		return nil
	}))
	require.NoError(t, err)
	indexBefore, err := os.ReadFile(IndexPath(fixtureDir))
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		mutate func(*MockRequest)
		want   string
	}{
		{
			name: "duplicate connection",
			want: "already started",
		},
		{
			name: "undeclared connection",
			mutate: func(request *MockRequest) {
				request.Connection = "missing"
			},
			want: "undeclared mock connection missing",
		},
		{
			name: "material",
			mutate: func(request *MockRequest) {
				request.Artifact.Path = "/different"
			},
			want: "current recording changed before execution",
		},
		{
			name: "timeout",
			mutate: func(request *MockRequest) {
				request.Connection = "probe"
				request.Timeout += time.Nanosecond
			},
			want: "timeout differs",
		},
		{
			name: "progress descriptor",
			mutate: func(request *MockRequest) {
				request.Connection, request.ProgressFD = "probe", 3
			},
			want: "progress descriptor differs",
		},
		{
			name: "native arguments",
			mutate: func(request *MockRequest) {
				request.Connection, request.NativeArgs = "probe", []string{"different"}
			},
			want: "native arguments differ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := request
			if tc.mutate != nil {
				tc.mutate(&probe)
			}
			_, err := RunMock(t.Context(), probe, mockPolicyFunc(func(context.Context, MockContext) error {
				t.Fatal("rejected join invoked replay")
				return nil
			}))
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, indexBefore, readIndexBytes(t, fixtureDir))
		})
	}
	assertRecordLockAvailable(t, fixtureDir)
}

func executionDirForTest(fixtureDir string, execution Execution) string {
	return PackageExecutionRoot(fixtureDir, execution.Artifact.ID, execution.ExecutionID)
}
