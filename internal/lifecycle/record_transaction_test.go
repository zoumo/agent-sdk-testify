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

func prepareRecordTransactionFixture(t *testing.T) string {
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
	fixtureDir := filepath.Join(root, "fixture")
	_, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Plan: plan})
	require.NoError(t, err)
	return fixtureDir
}

func prepareMultiRecordTransactionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(config, "auth.json"), []byte("{\"syntheticToken\":\"private\"}\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	fixtureDir := filepath.Join(root, "fixture")
	_, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Agent: "codex", Case: "SDK-CASE-032"})
	require.NoError(t, err)
	return fixtureDir
}

func prepareOfficialRecordTransactionFixture(t *testing.T) (fixtureDir, mockTemplate, mockOverlay string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	fixtureDir = filepath.Join(root, "fixture")
	_, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)

	activity, err := BeginOfficial(fixtureDir, "record")
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

	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	mockTemplate = filepath.Join(fixture.ResourceRoot, "templates", "mock")
	mockOverlay = filepath.Join(root, "mock-route-overlay")
	require.NoError(t, tool.CopyTree(mockTemplate, mockOverlay))
	require.NoError(t, bindRouteSlots(filepath.Join(fixture.ResourceRoot, "routes", "mock"), mockOverlay))
	return fixtureDir, mockTemplate, mockOverlay
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
				sourcePlan, err := os.ReadFile(run.Fixture().Source.Path)
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

func recordTransactionRequest(fixtureDir string, resultFD int) RecordRequest {
	return RecordRequest{
		FixtureDir: fixtureDir,
		Connection: "main",
		Args:       []string{"--fixture-dir", fixtureDir, "--cmd", "synthetic", "--"},
		Timeout:    time.Second,
		Target:     "synthetic-target",
		ResultFD:   resultFD,
	}
}

func runRecordTransaction(ctx context.Context, request RecordRequest, policy RecordPolicy) error {
	_, err := RunRecord(ctx, request, policy)
	return err
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

func assertRecordLockAvailable(t *testing.T, fixtureDir string) {
	t.Helper()
	lock, err := actionLock(fixtureDir, true)
	require.NoError(t, err, "record transaction retained the action lock")
	require.NoError(t, lock.Close())
}

type currentRecordingSnapshot struct {
	current   []byte
	materials []byte
}

func snapshotCurrentRecording(t *testing.T, fixtureDir string) currentRecordingSnapshot {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	materials, err := os.ReadFile(MaterialIndexPath(fixtureDir))
	require.NoError(t, err)
	return currentRecordingSnapshot{current: current, materials: materials}
}

func (s currentRecordingSnapshot) requireUnchanged(t *testing.T, fixtureDir string) {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, s.current, current)
	materials, err := os.ReadFile(MaterialIndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, s.materials, materials)
}

func bindRecordMockRoute(t *testing.T, fixtureDir string) string {
	t.Helper()
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	template := filepath.Join(fixture.ResourceRoot, "templates", "mock")
	overlay := filepath.Join(t.TempDir(), "mock-overlay")
	require.NoError(t, tool.CopyTree(template, overlay))
	require.NoError(t, bindRouteSlots(filepath.Join(fixture.ResourceRoot, "routes", "mock"), overlay))
	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(overlay, "workspace"), target)
	return template
}

func readRecordExecution(t *testing.T, fixtureDir, executionID string) Execution {
	t.Helper()
	var execution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, executionID), "execution.json"), &execution))
	return execution
}

func TestRecordTransactionKeepsOfficialRecordRouteAndRestoresMockRoute(t *testing.T) {
	fixtureDir, mockTemplate, mockOverlay := prepareOfficialRecordTransactionFixture(t)
	var packages int
	result, fd := recordResultFile(t)
	err := runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, fd), successfulRecordPolicy(t, &packages))
	require.NoError(t, err)
	require.Equal(t, 1, packages)
	require.Equal(t, "passed", readRecordResults(t, result)[1]["status"])

	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	mockWorkspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(mockTemplate, "workspace"), mockWorkspace)
	require.NotEqual(t, filepath.Join(mockOverlay, "workspace"), mockWorkspace)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "passed", index.Executions[0].Status)
	execution := readRecordExecution(t, fixtureDir, index.Executions[0].ID)
	require.Equal(t, "official", execution.Provenance)
	recordWorkspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, execution.Workspace, recordWorkspace)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionBeginAbortClosesAllocatedExecution(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.ResourceRoot, "baseline", "record-target.private"), []byte("different-target"), 0o600))
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
	err = runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, fd), policy)
	require.ErrorContains(t, err, "targets differ")
	require.Empty(t, readRecordResultsIfAny(t, result))

	stateBefore.requireUnchanged(t, fixtureDir)
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Equal(t, "failed", index.Executions[0].Connections[0].Status)
	aborted := readRecordExecution(t, fixtureDir, index.Executions[0].ID)
	require.NoFileExists(t, filepath.Join(aborted.Config, "auth.json"))
	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionCommitsFinalCapture(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	var packages int
	result, fd := recordResultFile(t)
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)

	err = runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, fd), successfulRecordPolicy(t, &packages))
	require.NoError(t, err)
	require.Equal(t, 1, packages)

	results := readRecordResults(t, result)
	require.Equal(t, "running", results[0]["status"])
	require.Equal(t, "passed", results[1]["status"])
	require.Equal(t, results[0]["executionId"], results[1]["executionId"])
	require.NotNil(t, results[1]["material"])

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "passed", index.Executions[0].Status)
	require.Len(t, index.Artifacts, 1)
	material := index.Artifacts[0]
	require.Equal(t, material.ID, tool.Obj(results[1]["material"])["id"])

	var current Current
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "current.json"), &current))
	require.Equal(t, material.ID, current.MaterialID)
	_, err = inspectMaterial(fixture, material)
	require.NoError(t, err)

	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "record", "workspace"), target)
	execution := Execution{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[0].ID), "execution.json"), &execution))
	require.Equal(t, material, execution.Artifact)
	require.NoFileExists(t, filepath.Join(execution.Config, "auth.json"))
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionJoinsDeclaredConnectionAndPublishesAfterFinalPeer(t *testing.T) {
	fixtureDir := prepareMultiRecordTransactionFixture(t)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	request := recordTransactionRequest(fixtureDir, 0)

	mainResult, mainFD := recordResultFile(t)
	mainRequest := request
	mainRequest.Connection, mainRequest.ResultFD = "main", mainFD
	require.NoError(t, runRecordTransaction(t.Context(), mainRequest, policy))
	mainRows := readRecordResults(t, mainResult)
	require.Equal(t, "passed", mainRows[1]["status"])
	require.NotContains(t, mainRows[1], "material")
	require.Zero(t, packages)

	openIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, openIndex.Executions, 1)
	require.Equal(t, "open", openIndex.Executions[0].Status)
	currentBeforeFinal, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	var openExecution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, openIndex.Executions[0].ID), "execution.json"), &openExecution))
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	openRoute, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.NotEqual(t, openExecution.ArtifactRoot, openRoute)

	probeResult, probeFD := recordResultFile(t)
	probeRequest := request
	probeRequest.Connection, probeRequest.ResultFD = "probe", probeFD
	require.NoError(t, runRecordTransaction(t.Context(), probeRequest, policy))
	probeRows := readRecordResults(t, probeResult)
	require.Equal(t, "passed", probeRows[1]["status"])
	require.NotNil(t, probeRows[1]["material"])
	require.Equal(t, mainRows[0]["executionId"], probeRows[0]["executionId"])
	require.Equal(t, 1, packages)

	closedIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, closedIndex.Executions, 1)
	require.Equal(t, "passed", closedIndex.Executions[0].Status)
	require.Len(t, closedIndex.Artifacts, len(openIndex.Artifacts)+1)
	currentAfterFinal, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	require.NotEqual(t, currentBeforeFinal, currentAfterFinal)

	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionFailurePreservesCurrentAndDurableEvidence(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	var packages int
	initialPolicy := successfulRecordPolicy(t, &packages)
	initialResult, initialFD := recordResultFile(t)
	request := recordTransactionRequest(fixtureDir, initialFD)
	require.NoError(t, runRecordTransaction(t.Context(), request, initialPolicy))
	require.Equal(t, 1, packages)
	require.Equal(t, "passed", readRecordResults(t, initialResult)[1]["status"])
	stateBefore := snapshotCurrentRecording(t, fixtureDir)
	mockTemplate := bindRecordMockRoute(t, fixtureDir)

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
	err := runRecordTransaction(t.Context(), request, failing)
	require.ErrorIs(t, err, context.Canceled)
	failureRows := readRecordResults(t, failureResult)
	require.Equal(t, "failed", failureRows[1]["status"])
	require.NotContains(t, failureRows[1], "material")

	stateBefore.requireUnchanged(t, fixtureDir)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 2)
	require.Equal(t, "failed", index.Executions[1].Status)
	var failedExecution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[1].ID), "execution.json"), &failedExecution))
	require.NoFileExists(t, filepath.Join(failedExecution.Config, "auth.json"))
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[1].ID), "receipts", "main.failure.json"), &failure))
	require.Equal(t, "failed", failure["status"])
	require.Contains(t, failure["error"], "context canceled")

	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "record", "workspace"), target)
	mockWorkspace, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(mockTemplate, "workspace"), mockWorkspace)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionFinalizationFailureWritesResultAndPreservesCurrent(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	var packages int
	initialResult, initialFD := recordResultFile(t)
	request := recordTransactionRequest(fixtureDir, initialFD)
	require.NoError(t, runRecordTransaction(t.Context(), request, successfulRecordPolicy(t, &packages)))
	require.Equal(t, "passed", readRecordResults(t, initialResult)[1]["status"])
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	finalizationErr := errors.New("synthetic package failure")
	policy := successfulRecordPolicy(t, &packages)
	policy.finalize = func(context.Context, RecordContext) (*corpus.Material, error) {
		return nil, finalizationErr
	}
	result, fd := recordResultFile(t)
	request.ResultFD = fd
	err := runRecordTransaction(t.Context(), request, policy)
	require.ErrorIs(t, err, finalizationErr)
	failureRows := readRecordResults(t, result)
	require.Equal(t, "failed", failureRows[1]["status"])
	require.NotContains(t, failureRows[1], "material")

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 2)
	require.Equal(t, "failed", index.Executions[1].Status)
	executionDir := ExecutionDir(fixtureDir, index.Executions[1].ID)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], finalizationErr.Error())
	assertRecordLockAvailable(t, fixtureDir)
	stateBefore.requireUnchanged(t, fixtureDir)
}

func TestRecordTransactionStartedResultDeliveryFailurePreservesCurrentAndReleasesLock(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	defer writer.Close()
	policy := recordPolicyFunc{
		prepare: func(context.Context, RecordContext, RecordProbeTracker) (tool.Object, error) {
			t.Fatal("result delivery failure must precede policy preparation")
			return nil, nil
		},
		capture: func(context.Context, RecordContext) error {
			t.Fatal("result delivery failure must precede capture")
			return nil
		},
		finalize: func(context.Context, RecordContext) (*corpus.Material, error) {
			t.Fatal("result delivery failure must not package a material")
			return nil, nil
		},
	}
	err = runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, int(writer.Fd())), policy)
	require.Error(t, err)

	stateBefore.requireUnchanged(t, fixtureDir)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Empty(t, index.Executions[0].ArtifactID)
	require.Equal(t, "failed", index.Executions[0].Connections[0].Status)
	executionDir := ExecutionDir(fixtureDir, index.Executions[0].ID)
	result := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.result.json"), &result))
	require.Equal(t, "failed", result["status"])
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Equal(t, "failed", failure["status"])
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionFinalResultDeliveryFailurePreservesCurrentAndReleasesLock(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	var packages int
	initialResult, initialFD := recordResultFile(t)
	request := recordTransactionRequest(fixtureDir, initialFD)
	require.NoError(t, runRecordTransaction(t.Context(), request, successfulRecordPolicy(t, &packages)))
	require.Equal(t, "passed", readRecordResults(t, initialResult)[1]["status"])
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer writer.Close()
	policy := successfulRecordPolicy(t, &packages)
	capture := policy.capture
	policy.capture = func(ctx context.Context, run RecordContext) error {
		require.NoError(t, capture(ctx, run))
		return reader.Close()
	}
	request.ResultFD = int(writer.Fd())
	err = runRecordTransaction(t.Context(), request, policy)
	require.ErrorContains(t, err, "broken pipe")
	require.Equal(t, 2, packages)

	currentAfter, err := os.ReadFile(filepath.Join(fixtureDir, "current.json"))
	require.NoError(t, err)
	require.Equal(t, stateBefore.current, currentAfter)
	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 2)
	require.Equal(t, "failed", index.Executions[1].Status)
	require.Empty(t, index.Executions[1].ArtifactID)
	executionDir := ExecutionDir(fixtureDir, index.Executions[1].ID)
	result := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.result.json"), &result))
	require.Equal(t, "failed", result["status"])
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], "broken pipe")
	execution := readRecordExecution(t, fixtureDir, index.Executions[1].ID)
	require.NoFileExists(t, filepath.Join(execution.Config, "auth.json"))
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionPublicationFailurePreservesCurrentAndReleasesLock(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	var packages int
	policy := successfulRecordPolicy(t, &packages)
	packageMaterial := policy.finalize
	policy.finalize = func(ctx context.Context, run RecordContext) (*corpus.Material, error) {
		artifact, err := packageMaterial(ctx, run)
		if err != nil {
			return nil, err
		}
		artifact.PlanSHA256 = strings.Repeat("0", 64)
		return artifact, nil
	}
	result, fd := recordResultFile(t)
	err := runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, fd), policy)
	require.ErrorContains(t, err, "artifact hash mismatch: plan.json")
	require.Equal(t, 1, packages)
	require.Equal(t, "failed", readRecordResults(t, result)[1]["status"])

	stateBefore.requireUnchanged(t, fixtureDir)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Empty(t, index.Executions[0].ArtifactID)
	executionDir := ExecutionDir(fixtureDir, index.Executions[0].ID)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], "artifact hash mismatch: plan.json")
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionLatePublicationFailurePreservesCurrentAndReleasesLock(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	var packages int
	policy := successfulRecordPolicy(t, &packages)
	packageMaterial := policy.finalize
	policy.finalize = func(ctx context.Context, run RecordContext) (*corpus.Material, error) {
		artifact, err := packageMaterial(ctx, run)
		if err != nil {
			return nil, err
		}
		materialRoot := filepath.Join(fixtureDir, "materials", artifact.ID)
		require.NoError(t, os.Mkdir(materialRoot, 0o700))
		t.Cleanup(func() { _ = os.RemoveAll(materialRoot) })
		return artifact, nil
	}
	result, fd := recordResultFile(t)
	err := runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, fd), policy)
	require.ErrorContains(t, err, "material identity already exists")
	require.Equal(t, 1, packages)
	require.Equal(t, "failed", readRecordResults(t, result)[1]["status"])
	stateBefore.requireUnchanged(t, fixtureDir)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Empty(t, index.Executions[0].ArtifactID)
	executionDir := ExecutionDir(fixtureDir, index.Executions[0].ID)
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(executionDir, "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], "material identity already exists")
	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionClosesWhenFinalizationLockCannotOpen(t *testing.T) {
	fixtureDir := prepareRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	var packages int
	policy := successfulRecordPolicy(t, &packages)
	capture := policy.capture
	policy.capture = func(ctx context.Context, run RecordContext) error {
		require.NoError(t, capture(ctx, run))
		require.NoError(t, os.Remove(filepath.Join(run.ExecutionDir(), "record-finalize.lock")))
		return os.Mkdir(filepath.Join(run.ExecutionDir(), "record-finalize.lock"), 0o700)
	}
	result, fd := recordResultFile(t)
	err := runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, fd), policy)
	require.ErrorContains(t, err, "is a directory")
	require.Zero(t, packages)
	require.Equal(t, "failed", readRecordResults(t, result)[1]["status"])

	stateBefore.requireUnchanged(t, fixtureDir)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "failed", index.Executions[0].Status)
	require.Equal(t, "failed", index.Executions[0].Connections[0].Status)
	execution := readRecordExecution(t, fixtureDir, index.Executions[0].ID)
	require.NoFileExists(t, filepath.Join(execution.Config, "auth.json"))
	failure := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[0].ID), "receipts", "main.failure.json"), &failure))
	require.Contains(t, failure["error"], "is a directory")
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionNonFinalResultDeliveryFailurePreservesCurrentAndReleasesLock(t *testing.T) {
	fixtureDir := prepareMultiRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer writer.Close()
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	capture := policy.capture
	policy.capture = func(ctx context.Context, run RecordContext) error {
		require.NoError(t, capture(ctx, run))
		return reader.Close()
	}
	err = runRecordTransaction(t.Context(), recordTransactionRequest(fixtureDir, int(writer.Fd())), policy)
	require.ErrorContains(t, err, "broken pipe")
	require.Zero(t, packages)
	stateBefore.requireUnchanged(t, fixtureDir)

	index, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, index.Executions, 1)
	require.Equal(t, "open", index.Executions[0].Status)
	require.Equal(t, "passed", index.Executions[0].Connections[0].Status)
	require.Equal(t, "pending", index.Executions[0].Connections[1].Status)
	result := tool.Object{}
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, index.Executions[0].ID), "receipts", "main.result.json"), &result))
	require.Equal(t, "passed", result["status"])
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionNonFinalFailureWaitsForFinalConnection(t *testing.T) {
	fixtureDir := prepareMultiRecordTransactionFixture(t)
	stateBefore := snapshotCurrentRecording(t, fixtureDir)
	request := recordTransactionRequest(fixtureDir, 0)

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
	err := runRecordTransaction(t.Context(), mainRequest, failing)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "failed", readRecordResults(t, failingResult)[1]["status"])

	openIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Len(t, openIndex.Executions, 1)
	require.Equal(t, "open", openIndex.Executions[0].Status)
	require.Equal(t, "failed", openIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "pending", openIndex.Executions[0].Connections[1].Status)
	require.Len(t, openIndex.Artifacts, 1)
	stateBefore.requireUnchanged(t, fixtureDir)
	var openExecution Execution
	require.NoError(t, tool.ReadJSON(filepath.Join(ExecutionDir(fixtureDir, openIndex.Executions[0].ID), "execution.json"), &openExecution))
	require.FileExists(t, filepath.Join(openExecution.Config, "auth.json"))

	var packages int
	probeResult, probeFD := recordResultFile(t)
	probeRequest := request
	probeRequest.Connection, probeRequest.ResultFD = "probe", probeFD
	err = runRecordTransaction(t.Context(), probeRequest, successfulRecordPolicy(t, &packages))
	require.EqualError(t, err, "record execution has failed connections")
	require.Zero(t, packages)
	require.Equal(t, "failed", readRecordResults(t, probeResult)[1]["status"])

	closedIndex, err := readIndex(IndexPath(fixtureDir))
	require.NoError(t, err)
	require.Equal(t, "failed", closedIndex.Executions[0].Status)
	require.Equal(t, "failed", closedIndex.Executions[0].Connections[0].Status)
	require.Equal(t, "passed", closedIndex.Executions[0].Connections[1].Status)
	require.Len(t, closedIndex.Artifacts, 1)
	stateBefore.requireUnchanged(t, fixtureDir)
	require.NoFileExists(t, filepath.Join(openExecution.Config, "auth.json"))

	fixture, err := OpenFixture(fixtureDir)
	require.NoError(t, err)
	target, err := os.Readlink(filepath.Join(fixture.ResourceRoot, "routes", "record", "workspace"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(fixture.ResourceRoot, "templates", "record", "workspace"), target)
	assertRecordLockAvailable(t, fixtureDir)
}

func TestRecordTransactionRejectsDuplicateAndIncompatibleJoins(t *testing.T) {
	fixtureDir := prepareMultiRecordTransactionFixture(t)
	var packages int
	policy := successfulRecordPolicy(t, &packages)
	result, fd := recordResultFile(t)
	request := recordTransactionRequest(fixtureDir, fd)
	require.NoError(t, runRecordTransaction(t.Context(), request, policy))
	require.Equal(t, "passed", readRecordResults(t, result)[1]["status"])
	indexBefore, err := os.ReadFile(IndexPath(fixtureDir))
	require.NoError(t, err)

	duplicate := request
	duplicateFD := fd
	duplicate.ResultFD = duplicateFD
	require.ErrorIs(t, runRecordTransaction(t.Context(), duplicate, policy), ErrBusy)
	require.Equal(t, indexBefore, readIndexBytes(t, fixtureDir))

	incompatibleTimeout := request
	incompatibleTimeout.Connection, incompatibleTimeout.Timeout = "probe", 2*time.Second
	require.ErrorContains(t, runRecordTransaction(t.Context(), incompatibleTimeout, policy), "target or timeout")
	require.Equal(t, indexBefore, readIndexBytes(t, fixtureDir))

	incompatibleTarget := request
	incompatibleTarget.Connection, incompatibleTarget.Target = "probe", "different-target"
	require.ErrorContains(t, runRecordTransaction(t.Context(), incompatibleTarget, policy), "targets differ")
	require.Equal(t, indexBefore, readIndexBytes(t, fixtureDir))
	assertRecordLockAvailable(t, fixtureDir)
}

func readIndexBytes(t *testing.T, fixtureDir string) []byte {
	t.Helper()
	data, err := os.ReadFile(IndexPath(fixtureDir))
	require.NoError(t, err)
	return data
}
