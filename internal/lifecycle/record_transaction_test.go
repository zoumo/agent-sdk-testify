package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type recordPolicyFunc struct {
	prepare  func(context.Context, RecordContext, RecordProbeTracker) (tool.Object, error)
	capture  func(context.Context, RecordContext) error
	finalize func(context.Context, RecordContext) (*corpus.Material, error)
}

func (p recordPolicyFunc) Prepare(ctx context.Context, run RecordContext, trackProbe RecordProbeTracker) (tool.Object, error) {
	return p.prepare(ctx, run, trackProbe)
}

func (p recordPolicyFunc) Capture(ctx context.Context, run RecordContext) error {
	return p.capture(ctx, run)
}

func (p recordPolicyFunc) Package(ctx context.Context, run RecordContext) (*corpus.Material, error) {
	return p.finalize(ctx, run)
}

func prepareRecordTransactionSession(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "auth.json"), []byte("{\"syntheticToken\":\"private\"}\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	plan := filepath.Join(root, "plan.json")
	require.NoError(t, tool.WriteJSON(plan, tool.Object{
		"version":       1,
		"agentType":     "codex",
		"caseId":        "synthetic-record-transaction",
		"driverProfile": "consumer",
		"project":       filepath.Join(root, "project"),
		"model":         "synthetic-model",
		"cliVersion":    "synthetic-native",
		"scenario": tool.Object{
			"files":      tool.Object{"fixture.txt": "fixture\n"},
			"parameters": tool.Object{"keep": 9007199254740993},
		},
	}))
	sessionDir := filepath.Join(root, "session")
	_, err := Prepare(PrepareOptions{SessionDir: sessionDir, Plan: plan})
	require.NoError(t, err)
	return sessionDir
}

func prepareMultiRecordTransactionSession(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "auth.json"), []byte("{\"syntheticToken\":\"private\"}\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	sessionDir := filepath.Join(root, "session")
	_, err := Prepare(PrepareOptions{SessionDir: sessionDir, Agent: "codex", Case: "SDK-CASE-032"})
	require.NoError(t, err)
	return sessionDir
}

func prepareOfficialRecordTransactionSession(t *testing.T) (sessionDir, mockTemplate, mockOverlay string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	sessionDir = filepath.Join(root, "session")
	_, err := Prepare(PrepareOptions{SessionDir: sessionDir, Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)

	activity, err := BeginOfficial(sessionDir, "record")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = activity.Complete(tool.Object{"mode": "record", "status": "passed", "cleanupComplete": true})
	})
	launch, err := activity.AuthorizeRecord(strings.Repeat("a", 64), []string{"main"})
	require.NoError(t, err)
	require.NoError(t, tool.WriteJSON(filepath.Join(activity.Dir, "driver", "peer-main.process.json"), tool.Object{
		"supervisorPid": os.Getppid(),
	}))
	for key, value := range launch {
		t.Setenv(key, value)
	}

	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	mockTemplate = filepath.Join(session.ResourceRoot, "templates", "mock")
	mockOverlay = filepath.Join(root, "mock-route-overlay")
	require.NoError(t, tool.CopyTree(mockTemplate, mockOverlay))
	require.NoError(t, bindRouteSlots(filepath.Join(session.ResourceRoot, "routes", "mock"), mockOverlay))
	return sessionDir, mockTemplate, mockOverlay
}

func successfulRecordPolicy(t *testing.T, packages *int) recordPolicyFunc {
	t.Helper()
	return recordPolicyFunc{
		prepare: func(_ context.Context, run RecordContext, _ RecordProbeTracker) (tool.Object, error) {
			return run.Plan(), nil
		},
		capture: func(_ context.Context, run RecordContext) error {
			require.NoError(t, os.MkdirAll(filepath.Join(run.ArtifactRoot(), "raw"), 0o700))
			if run.Connection() == "main" {
				require.NoError(t, os.WriteFile(filepath.Join(run.ArtifactRoot(), "raw", "raw.jsonl"), []byte("{\"frame\":1}\n"), 0o600))
			} else {
				require.NoError(t, os.MkdirAll(filepath.Join(run.ArtifactRoot(), "remote-probe", "raw"), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(run.ArtifactRoot(), "remote-probe", "raw", "raw.jsonl"), []byte("{\"probe\":1}\n"), 0o600))
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(run.ConnectionReceipt()), 0o700))
			return tool.WriteJSON(run.ConnectionReceipt(), tool.Object{
				"captureStatus": "complete", "cleanupComplete": true, "clientEOF": true,
				"nativeLaunched": true, "reaped": true, "nativeExitCode": 0, "frames": 0,
			})
		},
		finalize: func(_ context.Context, run RecordContext) (*corpus.Material, error) {
			*packages++
			plan := run.Plan()
			sourcePlanSHA256 := ""
			execution := run.Execution()
			if execution.Artifact.OfficialOrigin != nil {
				sourcePlan, err := os.ReadFile(run.Session().Source.Path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(run.ArtifactRoot(), "source-plan.json"), sourcePlan, 0o600))
				sourcePlanSHA256 = tool.DigestBytes(sourcePlan)
				plan["sourcePlanSHA256"] = sourcePlanSHA256
				plan["caseId"] = execution.Artifact.OfficialOrigin.CaseID
				plan["provider"] = execution.Artifact.OfficialOrigin.AgentType
				require.NoError(t, tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "official-origin.json"), execution.Artifact.OfficialOrigin))
				for _, connection := range execution.Connections {
					require.NoError(t, tool.CopyFile(
						filepath.Join(run.ExecutionDir(), "receipts", connection.ID+".origin.json"),
						filepath.Join(run.ArtifactRoot(), "origin", connection.ID+".json"),
					))
				}
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "plan.json"), plan))
			planHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "plan.json"))
			require.NoError(t, err)
			rawHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "raw", "raw.jsonl"))
			require.NoError(t, err)
			transportHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "raw", "transport.json"))
			require.NoError(t, err)
			connections := make([]string, 0, len(execution.Connections))
			for _, connection := range execution.Connections {
				connections = append(connections, connection.ID)
			}
			manifestProvenance := execution.Provenance
			if execution.Artifact.OfficialOrigin != nil {
				manifestProvenance = "capture"
			}
			manifest := tool.Object{
				"schemaVersion": 1, "provenance": manifestProvenance, "status": "captured", "captureStatus": "complete",
				"planSHA256": planHash,
				"files":      tool.Object{"raw.jsonl": rawHash, "transport.json": transportHash},
			}
			if sourcePlanSHA256 != "" {
				manifest["sourcePlanSHA256"] = sourcePlanSHA256
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "manifest.json"), manifest))
			require.NoError(t, tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "report.json"), tool.Object{
				"schemaVersion": 1, "status": "captured", "captureStatus": "complete",
			}))
			artifact := execution.Artifact
			artifact.Kind, artifact.Action = "capture", "record"
			artifact.Provenance = execution.Provenance
			artifact.Path = run.ArtifactRoot()
			artifact.Connections = connections
			artifact.PlanSHA256 = planHash
			artifact.ManifestSHA256, err = tool.FileDigest(filepath.Join(run.ArtifactRoot(), "manifest.json"))
			require.NoError(t, err)
			artifact.RecordingSHA256 = rawHash
			return &artifact, nil
		},
	}
}

func recordResultFile(t *testing.T) (*os.File, int) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "record-result")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	return file, int(file.Fd())
}

func readRecordResults(t *testing.T, file *os.File) []tool.Object {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	results := make([]tool.Object, 0, len(lines))
	for _, line := range lines {
		var value tool.Object
		require.NoError(t, json.Unmarshal([]byte(line), &value))
		results = append(results, value)
	}
	return results
}

func readRecordResultsIfAny(t *testing.T, file *os.File) []tool.Object {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	results := make([]tool.Object, 0, len(lines))
	for _, line := range lines {
		var value tool.Object
		require.NoError(t, json.Unmarshal([]byte(line), &value))
		results = append(results, value)
	}
	return results
}

func assertRecordLockAvailable(t *testing.T, sessionDir string) {
	t.Helper()
	lock, err := actionLock(sessionDir, true)
	require.NoError(t, err, "record transaction retained the action lock")
	require.NoError(t, lock.Close())
}

func bindRecordMockRoute(t *testing.T, sessionDir string) string {
	t.Helper()
	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	template := filepath.Join(session.ResourceRoot, "templates", "mock")
	overlay := filepath.Join(t.TempDir(), "mock-overlay")
	require.NoError(t, tool.CopyTree(template, overlay))
	require.NoError(t, bindRouteSlots(filepath.Join(session.ResourceRoot, "routes", "mock"), overlay))
	target, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(overlay, "workspace"), target)
	return template
}

func readRecordExecution(t *testing.T, sessionDir, executionID string) Execution {
	t.Helper()
	var execution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(sessionDir, executionID), "execution.json"), &execution))
	return execution
}

func TestRecordTransactionKeepsOfficialRecordRouteAndRestoresMockRoute(t *testing.T) {
	sessionDir, mockTemplate, mockOverlay := prepareOfficialRecordTransactionSession(t)
	var packages int
	result, fd := recordResultFile(t)
	err := RunRecord(t.Context(), RecordRequest{
		SessionDir: sessionDir,
		Connection: "main",
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
		ResultFD:   fd,
	}, successfulRecordPolicy(t, &packages))
	require.NoError(t, err)
	require.Equal(t, 1, packages)
	require.Equal(t, "passed", readRecordResults(t, result)[1]["status"])

	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	mockWorkspace, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(mockTemplate, "workspace"), mockWorkspace)
	require.NotEqual(t, filepath.Join(mockOverlay, "workspace"), mockWorkspace)

	index, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "passed", index.Executions[0].Status)
	execution := readRecordExecution(t, sessionDir, index.Executions[0].ID)
	require.Equal(t, "official", execution.Provenance)
	recordWorkspace, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, execution.Workspace, recordWorkspace)
	assertRecordLockAvailable(t, sessionDir)
}

func TestRecordTransactionBeginAbortClosesAllocatedExecution(t *testing.T) {
	sessionDir := prepareRecordTransactionSession(t)
	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(session.ResourceRoot, "baseline", "record-target.private"), []byte("different-target"), 0o600))
	result, fd := recordResultFile(t)
	policy := recordPolicyFunc{
		prepare: func(context.Context, RecordContext, RecordProbeTracker) (tool.Object, error) {
			t.Fatal("aborted begin must not invoke policy preparation")
			return nil, nil
		},
		capture: func(context.Context, RecordContext) error {
			t.Fatal("aborted begin must not invoke policy capture")
			return nil
		},
		finalize: func(context.Context, RecordContext) (*corpus.Material, error) {
			t.Fatal("aborted begin must not invoke policy packaging")
			return nil, nil
		},
	}
	err = RunRecord(t.Context(), RecordRequest{
		SessionDir: sessionDir,
		Connection: "main",
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
		ResultFD:   fd,
	}, policy)
	require.ErrorContains(t, err, "targets differ")
	require.Empty(t, readRecordResultsIfAny(t, result))

	index, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Equal(t, "failed", index.Executions[0].Connections[0].Status)
	aborted := readRecordExecution(t, sessionDir, index.Executions[0].ID)
	require.NoFileExists(t, filepath.Join(aborted.Config, "auth.json"))
	target, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(session.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, sessionDir)
}

func TestRecordTransactionCommitsFinalCapture(t *testing.T) {
	sessionDir := prepareRecordTransactionSession(t)
	var packages int
	result, fd := recordResultFile(t)
	session, err := OpenSession(sessionDir)
	require.NoError(t, err)

	err = RunRecord(t.Context(), RecordRequest{
		SessionDir: sessionDir,
		Connection: "main",
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
		ResultFD:   fd,
	}, successfulRecordPolicy(t, &packages))
	require.NoError(t, err)
	require.Equal(t, 1, packages)

	results := readRecordResults(t, result)
	require.Equal(t, "running", results[0]["status"])
	require.Equal(t, "passed", results[1]["status"])
	require.Equal(t, results[0]["executionId"], results[1]["executionId"])
	require.NotNil(t, results[1]["material"])

	index, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "passed", index.Executions[0].Status)
	require.Len(t, index.Artifacts, 1)
	material := index.Artifacts[0]
	require.Equal(t, material.ID, tool.Obj(results[1]["material"])["id"])

	var current Current
	require.NoError(t, tool.ReadJSON(filepath.Join(sessionDir, "current.json"), &current))
	require.Equal(t, material.ID, current.MaterialID)
	_, err = inspectMaterial(session, material)
	require.NoError(t, err)

	target, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(session.ResourceRoot, "templates", "record", "workspace"), target)
	execution := Execution{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(sessionDir, index.Executions[0].ID), "execution.json"), &execution))
	require.Equal(t, material, execution.Artifact)
	require.NoFileExists(t, filepath.Join(execution.Config, "auth.json"))
	assertRecordLockAvailable(t, sessionDir)
}

func TestRecordTransactionJoinsDeclaredConnectionAndPublishesAfterFinalPeer(t *testing.T) {
	sessionDir := prepareMultiRecordTransactionSession(t)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	request := RecordRequest{
		SessionDir: sessionDir,
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
	}

	mainResult, mainFD := recordResultFile(t)
	mainRequest := request
	mainRequest.Connection, mainRequest.ResultFD = "main", mainFD
	require.NoError(t, RunRecord(t.Context(), mainRequest, policy))
	mainRows := readRecordResults(t, mainResult)
	require.Equal(t, "passed", mainRows[1]["status"])
	require.NotContains(t, mainRows[1], "material")
	require.Zero(t, packages)

	openIndex, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, openIndex.Executions, 1)
	require.Equal(t, "open", openIndex.Executions[0].Status)
	currentBeforeFinal, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	var openExecution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(sessionDir, openIndex.Executions[0].ID), "execution.json"), &openExecution))
	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	openRoute, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.NotEqual(t, openExecution.ArtifactRoot, openRoute)

	probeResult, probeFD := recordResultFile(t)
	probeRequest := request
	probeRequest.Connection, probeRequest.ResultFD = "probe", probeFD
	require.NoError(t, RunRecord(t.Context(), probeRequest, policy))
	probeRows := readRecordResults(t, probeResult)
	require.Equal(t, "passed", probeRows[1]["status"])
	require.NotNil(t, probeRows[1]["material"])
	require.Equal(t, mainRows[0]["executionId"], probeRows[0]["executionId"])
	require.Equal(t, 1, packages)

	closedIndex, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, closedIndex.Executions, 1)
	require.Equal(t, "passed", closedIndex.Executions[0].Status)
	require.Len(t, closedIndex.Artifacts, len(openIndex.Artifacts)+1)
	currentAfterFinal, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	require.NotEqual(t, currentBeforeFinal, currentAfterFinal)

	target, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(session.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, sessionDir)
}

func TestRecordTransactionFailurePreservesCurrentAndDurableEvidence(t *testing.T) {
	sessionDir := prepareRecordTransactionSession(t)
	var packages int
	initialPolicy := successfulRecordPolicy(t, &packages)
	initialResult, initialFD := recordResultFile(t)
	request := RecordRequest{
		SessionDir: sessionDir,
		Connection: "main",
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
		ResultFD:   initialFD,
	}
	require.NoError(t, RunRecord(t.Context(), request, initialPolicy))
	require.Equal(t, 1, packages)
	require.Equal(t, "passed", readRecordResults(t, initialResult)[1]["status"])
	currentBefore, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	materialsBefore, err := os.ReadFile(MaterialIndexPath(sessionDir))
	require.NoError(t, err)
	mockTemplate := bindRecordMockRoute(t, sessionDir)

	failing := recordPolicyFunc{
		prepare: func(_ context.Context, run RecordContext, _ RecordProbeTracker) (tool.Object, error) {
			return run.Plan(), nil
		},
		capture: func(context.Context, RecordContext) error {
			return context.Canceled
		},
		finalize: func(context.Context, RecordContext) (*corpus.Material, error) {
			t.Fatal("failed connection attempted material publication")
			return nil, nil
		},
	}
	failureResult, failureFD := recordResultFile(t)
	request.ResultFD = failureFD
	err = RunRecord(t.Context(), request, failing)
	require.ErrorIs(t, err, context.Canceled)
	failureRows := readRecordResults(t, failureResult)
	require.Equal(t, "failed", failureRows[1]["status"])
	require.NotContains(t, failureRows[1], "material")

	currentAfter, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, currentBefore, currentAfter)
	materialsAfter, err := os.ReadFile(MaterialIndexPath(sessionDir))
	require.NoError(t, err)
	require.Equal(t, materialsBefore, materialsAfter)

	index, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 2)
	require.Equal(t, "failed", index.Executions[1].Status)
	var failedExecution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(sessionDir, index.Executions[1].ID), "execution.json"), &failedExecution))
	require.NoFileExists(t, filepath.Join(failedExecution.Config, "auth.json"))
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(sessionDir, index.Executions[1].ID), "receipts", "main.failure.json"), &failure))
	require.Equal(t, "failed", failure["status"])
	require.Contains(t, failure["error"], "context canceled")

	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	target, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(session.ResourceRoot, "templates", "record", "workspace"), target)
	mockWorkspace, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(mockTemplate, "workspace"), mockWorkspace)
	assertRecordLockAvailable(t, sessionDir)
}

func TestRecordTransactionFinalizationFailureWritesResultAndPreservesCurrent(t *testing.T) {
	sessionDir := prepareRecordTransactionSession(t)
	var packages int
	initialResult, initialFD := recordResultFile(t)
	request := RecordRequest{
		SessionDir: sessionDir,
		Connection: "main",
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
		ResultFD:   initialFD,
	}
	require.NoError(t, RunRecord(t.Context(), request, successfulRecordPolicy(t, &packages)))
	require.Equal(t, "passed", readRecordResults(t, initialResult)[1]["status"])
	currentBefore, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	materialsBefore, err := os.ReadFile(MaterialIndexPath(sessionDir))
	require.NoError(t, err)

	finalizationErr := errors.New("synthetic package failure")
	policy := successfulRecordPolicy(t, &packages)
	policy.finalize = func(context.Context, RecordContext) (*corpus.Material, error) {
		return nil, finalizationErr
	}
	result, fd := recordResultFile(t)
	request.ResultFD = fd
	err = RunRecord(t.Context(), request, policy)
	require.ErrorIs(t, err, finalizationErr)
	failureRows := readRecordResults(t, result)
	require.Equal(t, "failed", failureRows[1]["status"])
	require.NotContains(t, failureRows[1], "material")

	index, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 2)
	require.Equal(t, "failed", index.Executions[1].Status)
	executionDir := ExecutionDir(sessionDir, index.Executions[1].ID)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], finalizationErr.Error())
	assertRecordLockAvailable(t, sessionDir)

	currentAfter, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, currentBefore, currentAfter)
	materialsAfter, err := os.ReadFile(MaterialIndexPath(sessionDir))
	require.NoError(t, err)
	require.Equal(t, materialsBefore, materialsAfter)
}

func TestRecordTransactionNonFinalFailureWaitsForFinalConnection(t *testing.T) {
	sessionDir := prepareMultiRecordTransactionSession(t)
	currentBefore, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	materialsBefore, err := os.ReadFile(MaterialIndexPath(sessionDir))
	require.NoError(t, err)
	request := RecordRequest{
		SessionDir: sessionDir,
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
	}

	failingResult, failingFD := recordResultFile(t)
	mainRequest := request
	mainRequest.Connection, mainRequest.ResultFD = "main", failingFD
	failing := recordPolicyFunc{
		prepare: func(_ context.Context, run RecordContext, _ RecordProbeTracker) (tool.Object, error) {
			return run.Plan(), nil
		},
		capture: func(context.Context, RecordContext) error {
			return context.DeadlineExceeded
		},
		finalize: func(context.Context, RecordContext) (*corpus.Material, error) {
			t.Fatal("non-final failure attempted material publication")
			return nil, nil
		},
	}
	err = RunRecord(t.Context(), mainRequest, failing)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "failed", readRecordResults(t, failingResult)[1]["status"])

	openIndex, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Len(t, openIndex.Executions, 1)
	require.Equal(t, "open", openIndex.Executions[0].Status)
	require.Equal(t, "failed", openIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "pending", openIndex.Executions[0].Connections[1].Status)
	require.Len(t, openIndex.Artifacts, 1)
	currentOpen, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, currentBefore, currentOpen)
	var openExecution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(sessionDir, openIndex.Executions[0].ID), "execution.json"), &openExecution))
	require.FileExists(t, filepath.Join(openExecution.Config, "auth.json"))

	var packages int
	probeResult, probeFD := recordResultFile(t)
	probeRequest := request
	probeRequest.Connection, probeRequest.ResultFD = "probe", probeFD
	err = RunRecord(t.Context(), probeRequest, successfulRecordPolicy(t, &packages))
	require.EqualError(t, err, "record execution has failed connections")
	require.Zero(t, packages)
	require.Equal(t, "failed", readRecordResults(t, probeResult)[1]["status"])

	closedIndex, err := readIndex(IndexPath(sessionDir))
	require.NoError(t, err)
	require.Equal(t, "failed", closedIndex.Executions[0].Status)
	require.Equal(t, "failed", closedIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "passed", closedIndex.Executions[0].Connections[1].Status)
	require.Len(t, closedIndex.Artifacts, 1)
	materialsAfter, err := os.ReadFile(MaterialIndexPath(sessionDir))
	require.NoError(t, err)
	require.Equal(t, materialsBefore, materialsAfter)
	currentAfter, err := os.ReadFile(filepath.Join(sessionDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, currentBefore, currentAfter)
	require.NoFileExists(t, filepath.Join(openExecution.Config, "auth.json"))

	session, err := OpenSession(sessionDir)
	require.NoError(t, err)
	target, err := os.Readlink(filepath.Join(session.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(session.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, sessionDir)
}

func TestRecordTransactionRejectsDuplicateAndIncompatibleJoins(t *testing.T) {
	sessionDir := prepareMultiRecordTransactionSession(t)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	request := RecordRequest{
		SessionDir: sessionDir,
		Connection: "main",
		Args:       []string{"--session-dir", sessionDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
	}
	result, fd := recordResultFile(t)
	request.ResultFD = fd
	require.NoError(t, RunRecord(t.Context(), request, policy))
	require.Equal(t, "passed", readRecordResults(t, result)[1]["status"])
	indexBefore, err := os.ReadFile(IndexPath(sessionDir))
	require.NoError(t, err)

	duplicate := request
	duplicateFD := fd
	duplicate.ResultFD = duplicateFD
	require.ErrorIs(t, RunRecord(t.Context(), duplicate, policy), ErrBusy)
	require.Equal(t, indexBefore, readIndexBytes(t, sessionDir))

	incompatibleTimeout := request
	incompatibleTimeout.Connection, incompatibleTimeout.Timeout = "probe", 2*time.Second
	require.ErrorContains(t, RunRecord(t.Context(), incompatibleTimeout, policy), "target or timeout")
	require.Equal(t, indexBefore, readIndexBytes(t, sessionDir))

	incompatibleTarget := request
	incompatibleTarget.Connection, incompatibleTarget.Target = "probe", "different-target"
	require.ErrorContains(t, RunRecord(t.Context(), incompatibleTarget, policy), "targets differ")
	require.Equal(t, indexBefore, readIndexBytes(t, sessionDir))
	assertRecordLockAvailable(t, sessionDir)
}

func readIndexBytes(t *testing.T, sessionDir string) []byte {
	t.Helper()
	data, err := os.ReadFile(IndexPath(sessionDir))
	require.NoError(t, err)
	return data
}
