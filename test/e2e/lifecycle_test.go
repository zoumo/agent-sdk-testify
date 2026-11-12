package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConsumerLifecycleConformance(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "consumer-conformance")
	fixtureDir := prepareFixture(t, env, "--plan", plan)

	descriptor := readJSON(t, filepath.Join(fixtureDir, "fixture.json"))
	require.EqualValues(t, 6, descriptor["schemaVersion"])
	require.Equal(t, "codex", descriptor["agentType"])
	require.Equal(t, "plan", obj(descriptor["source"])["kind"])
	sourceRoot := resolveFixtureReference(t, fixtureDir, str(obj(descriptor["source"])["path"]))
	require.Contains(t, sourceRoot, filepath.Join(fixtureDir, "source"))
	baselineWorkspace := filepath.Join(fixtureRuntimeRoot(fixtureDir), "baseline", "workspace")
	require.FileExists(t, filepath.Join(baselineWorkspace, "fixture.txt"))
	require.Equal(t, "external-sdk", obj(obj(descriptor["routes"])["record"])["executor"])
	require.Equal(t, "consumer", obj(obj(descriptor["routes"])["record"])["provenance"])
	require.NotContains(t, obj(descriptor["routes"]), "verify")
	require.Equal(t, true, obj(obj(descriptor["routes"])["mock"])["enabled"])
	require.Equal(t, true, obj(obj(descriptor["routes"])["record"])["enabled"])
	require.NotEqual(t, baselineWorkspace, fixtureRouteWorkspace(fixtureDir, "record"))
	require.NotEqual(t, baselineWorkspace, fixtureRouteWorkspace(fixtureDir, "mock"))
	recordRoute := obj(obj(descriptor["routes"])["record"])
	recordConnection := obj(array(recordRoute["connections"])[0])
	require.Equal(t, "main", recordConnection["id"])
	require.NotContains(t, recordConnection, "command")
	require.NotContains(t, recordConnection, "args")

	firstRecord := recordConsumer(t, env, fixtureDir, agent, requestOne)
	require.Equal(t, "record", firstRecord["command"])
	require.Equal(t, "external-sdk", firstRecord["executor"])
	require.Equal(t, "native-agent", firstRecord["target"])
	require.Equal(t, "consumer", firstRecord["provenance"])
	firstExecution := firstRecord
	firstWorkspace := executionOverlayPath(t, fixtureDir, firstExecution, "workspace")
	require.DirExists(t, firstWorkspace)
	firstConnection := obj(array(firstRecord["connections"])[0])
	agent, err := filepath.EvalSymlinks(agent)
	require.NoError(t, err)
	require.Equal(t, []string{"--cmd", agent, "--", "app-server", "--listen", "stdio://"}, stringsOf(firstExecution["args"]))
	require.NotContains(t, stringsOf(firstExecution["args"]), "--fixture-dir")
	firstConfig := executionOverlayPath(t, fixtureDir, firstExecution, "config")
	require.NoError(t, os.WriteFile(filepath.Join(firstWorkspace, "only-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(firstConfig, "sessions", "only-first.txt"), []byte("first\n"), 0o600))

	firstIndex := materialIndex(t, fixtureDir)
	require.Len(t, array(firstIndex["materials"]), 1)
	firstCapture := artifactByID(t, fixtureDir, currentMaterialID(t, fixtureDir))
	require.Equal(t, firstCapture["id"], firstConnection["materialId"])
	require.Equal(t, firstCapture["planSHA256"], firstConnection["planSHA256"])
	require.Equal(t, firstCapture["manifestSHA256"], firstConnection["manifestSHA256"])
	require.Equal(t, firstCapture["recordingSHA256"], firstConnection["recordingSHA256"])
	require.Equal(t, "capture", firstCapture["kind"])
	require.Equal(t, "record", firstCapture["action"])
	require.Equal(t, "consumer", firstCapture["provenance"])
	require.NotContains(t, firstCapture, "path")
	firstCaptureRoot := fixtureMaterialRoot(t, fixtureDir, firstCapture)
	require.Equal(t, firstCapture["planSHA256"], digestFile(t, filepath.Join(firstCaptureRoot, "plan.json")))
	require.Equal(t, firstCapture["manifestSHA256"], digestFile(t, filepath.Join(firstCaptureRoot, "manifest.json")))
	require.Equal(t, firstCapture["recordingSHA256"], digestFile(t, filepath.Join(firstCaptureRoot, "raw", "raw.jsonl")))

	manifest := readJSON(t, filepath.Join(firstCaptureRoot, "manifest.json"))
	require.Equal(t, "consumer", manifest["provenance"])
	require.Equal(t, "captured", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.Equal(t, "pass", manifest["credentialScan"])
	const history = "config/sessions/conformance-history.jsonl"
	require.Equal(t, digestBytes([]byte("{\"type\":\"conformance-history\"}\n")), obj(manifest["savedFiles"])[history])

	secondRecord := recordConsumer(t, env, fixtureDir, agent, requestTwo)
	require.NotEqual(t, firstRecord["executionId"], secondRecord["executionId"])
	secondWorkspace := executionOverlayPath(t, fixtureDir, secondRecord, "workspace")
	secondHome := executionOverlayPath(t, fixtureDir, secondRecord, "home")
	secondConfig := executionOverlayPath(t, fixtureDir, secondRecord, "config")
	secondTmp := executionOverlayPath(t, fixtureDir, secondRecord, "tmp")
	firstHome := executionOverlayPath(t, fixtureDir, firstRecord, "home")
	firstTmp := executionOverlayPath(t, fixtureDir, firstRecord, "tmp")
	firstArtifactRoot := executionArtifactRoot(t, fixtureDir, firstRecord)
	secondArtifactRoot := executionArtifactRoot(t, fixtureDir, secondRecord)
	requireDistinct(t,
		firstWorkspace, secondWorkspace,
		firstHome, secondHome,
		firstConfig, secondConfig,
		firstTmp, secondTmp,
		firstArtifactRoot, secondArtifactRoot,
	)
	require.NoFileExists(t, filepath.Join(secondWorkspace, "only-first.txt"))
	require.NoFileExists(t, filepath.Join(secondConfig, "sessions", "only-first.txt"))
	require.FileExists(t, filepath.Join(secondWorkspace, "fixture.txt"))
	require.NoFileExists(t, filepath.Join(baselineWorkspace, "only-first.txt"))

	secondIndex := materialIndex(t, fixtureDir)
	require.Len(t, array(secondIndex["materials"]), 2)
	secondCapture := artifactByID(t, fixtureDir, currentMaterialID(t, fixtureDir))
	require.NotEqual(t, firstCapture["id"], secondCapture["id"])
	require.NotContains(t, secondCapture, "path")

	latestMock := runPreparedMock(t, env, fixtureDir, "", requestTwo)
	require.JSONEq(t, `{"id":2,"result":{"synthetic":true}}`, latestMock.stdout)
	latestMockExecution := latestExecution(t, fixtureDir, "mock")
	require.Equal(t, "consumer", latestMockExecution["provenance"])
	require.Equal(t, secondCapture["id"], obj(latestMockExecution["artifact"])["id"])
	latestConnection := obj(array(latestMockExecution["connections"])[0])
	require.NotContains(t, latestConnection, "command")
	require.NotContains(t, latestConnection, "args")
	require.NotContains(t, latestConnection, "launcher")
	require.NotContains(t, latestConnection, "material")
	require.Equal(t, secondCapture["id"], latestConnection["materialId"])
	require.Equal(t, secondCapture["planSHA256"], latestConnection["planSHA256"])
	require.Equal(t, secondCapture["manifestSHA256"], latestConnection["manifestSHA256"])
	require.Equal(t, secondCapture["recordingSHA256"], latestConnection["recordingSHA256"])
	latestWorkspace := executionOverlayPath(t, fixtureDir, latestMockExecution, "workspace")
	latestConfig := executionOverlayPath(t, fixtureDir, latestMockExecution, "config")
	require.NoFileExists(t, filepath.Join(latestWorkspace, "only-first.txt"))
	historyBytes := readFile(t, filepath.Join(latestConfig, "sessions", "conformance-history.jsonl"))
	require.True(t, bytes.Equal([]byte("{\"type\":\"conformance-history\"}\n"), historyBytes), "unexpected history bytes: %q", historyBytes)

	rejectedOverride := runTool(t, env, t.TempDir(), requestOne,
		"agent", "mock", "--fixture-dir", fixtureDir, "--recording", fixtureMaterialRoot(t, fixtureDir, firstCapture))
	require.NotZero(t, rejectedOverride.code)
	require.Contains(t, rejectedOverride.stderr, "unsupported native startup argument")
	afterMocks := materialIndex(t, fixtureDir)
	require.Equal(t, secondCapture["id"], currentMaterialID(t, fixtureDir))
	require.Len(t, array(afterMocks["materials"]), 2)

	rejected := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
	require.NotZero(t, rejected.code)
	require.Contains(t, strings.ToLower(rejected.stderr), "no applicable official verifier")
	for _, row := range array(fixtureIndex(t, fixtureDir)["executions"]) {
		require.NotEqual(t, "verify", obj(row)["command"])
	}

	require.NotContains(t, descriptor, "resourceRoot")
	require.NotContains(t, obj(obj(descriptor["routes"])["mock"]), "workspace")
	require.NotContains(t, descriptor, "finish")
	for _, value := range array(fixtureIndex(t, fixtureDir)["executions"]) {
		require.Equal(t, "passed", obj(value)["status"])
	}
	exported := filepath.Join(root, "exported")
	mustRun(t, env, root, "", "export", "--fixture-dir", fixtureDir, "--output", exported)
	require.NoError(t, os.RemoveAll(fixtureDir))
	require.NoDirExists(t, fixtureRuntimeRoot(fixtureDir))
	require.FileExists(t, filepath.Join(exported, "manifest.json"))
}

func TestOfficialLifecycleConformance(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	setControlledOfficialDriver(t, drivers, "local")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	recordControlledOfficial(t, env, fixtureDir, agent)
	execution := latestExecution(t, fixtureDir, "record")
	require.Equal(t, "external-sdk", execution["executor"])
	require.Equal(t, "native-agent", execution["target"])
	require.Equal(t, "official", execution["provenance"])
	capture := latestCapture(t, fixtureDir)
	require.Equal(t, "capture", capture["kind"])
	require.Equal(t, "official", capture["provenance"])
	captureRoot := fixtureMaterialRoot(t, fixtureDir, capture)
	assertMaterialHashes(t, captureRoot, capture)
	plan := readJSON(t, filepath.Join(captureRoot, "plan.json"))
	require.Equal(t, "SDK-CASE-001", plan["caseId"])
	require.Equal(t, "continuation", plan["driverProfile"])
	require.Equal(t, "personal-skills-v1", plan["recordingIsolation"])
	require.Equal(t, fixtureRouteWorkspace(resolvedPath(t, fixtureDir), "record"), plan["project"])
	manifest := readJSON(t, filepath.Join(captureRoot, "manifest.json"))
	require.Equal(t, "capture", manifest["provenance"])
	require.Equal(t, "captured", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.Equal(t, "pass", manifest["credentialScan"])
	report := readJSON(t, filepath.Join(captureRoot, "report.json"))
	require.Equal(t, "captured", report["status"])
	require.NotContains(t, report, "officialSDK")
	corpus := newSyntheticCorpus(t)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	verificationRoot, evidence := latestOfficialVerification(t, fixtureDir)
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, capture["id"], evidence["materialId"])
	require.NotContains(t, evidence, "admission")
	require.Equal(t, "synthetic-official-driver", obj(readJSON(t, filepath.Join(verificationRoot, "replay-report.json"))["officialSDK"])["version"])
	assertFileHashes(t, filepath.Dir(captureRoot), obj(evidence["files"]))
	admitted := admissionSummary(t, fixtureDir, verificationRoot)
	require.Equal(t, "admitted", admitted["status"])
	require.FileExists(t, filepath.Join(str(admitted["materials"]), "adoption.json"))
	adopted := object(nil)
	for _, item := range array(readJSON(t, corpus.index)["cases"]) {
		row := obj(item)
		if row["provider"] == "codex" && row["caseId"] == "SDK-CASE-001" {
			adopted = row
		}
	}
	require.NotNil(t, adopted)
	require.Equal(t, "admitted", adopted["adoption"])
	require.Equal(t, "passed", adopted["verification"])
	runPreparedMock(t, env, fixtureDir, "", conformancePayload(t, fixtureDir, capture))
	replay := latestExecution(t, fixtureDir, "mock")
	require.Equal(t, capture["id"], obj(replay["artifact"])["id"])
	require.Equal(t, "official", replay["provenance"])
	rows := array(fixtureIndex(t, fixtureDir)["executions"])
	require.Len(t, rows, 3)
	for _, item := range rows {
		require.Equal(t, "passed", obj(item)["status"])
	}
}

func conformancePayload(t *testing.T, fixture string, material object) string {
	t.Helper()
	plan := readJSON(t, filepath.Join(fixtureMaterialRoot(t, fixture, material), "plan.json"))
	encoded, err := json.Marshal(object{"id": 1, "method": "conformance", "params": object{"cwd": plan["project"]}})
	require.NoError(t, err)
	return string(encoded) + "\n"
}

func TestMultiConnectionBuiltinConformance(t *testing.T) {
	inputs := builtinInputs(t, "codex", "SDK-CASE-032")
	require.Contains(t, inputs, "main")
	require.Contains(t, inputs, "probe")

	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-032")

	mainRun, mainResult := runAgentResult(t, env, fixtureDir, inputs["main"], "mock", "--connection", "main")
	require.Zero(t, mainRun.code, mainRun.stderr)
	execution := readJSON(t, resolveFixtureReference(t, fixtureDir, str(mainResult["execution"])))
	connections := array(execution["connections"])
	require.Len(t, connections, 2)
	receipts := map[string]object{}
	for _, value := range connections {
		connection := obj(value)
		receipts[str(connection["id"])] = connection
	}
	mainReceipt := receipts["main"]
	probeReceipt := receipts["probe"]
	require.NotNil(t, mainReceipt)
	require.NotNil(t, probeReceipt)
	require.Equal(t, "main", mainReceipt["id"])
	require.Equal(t, "probe", probeReceipt["id"])
	require.FileExists(t, resolveFixtureReference(t, fixtureDir, str(mainReceipt["receipt"])))
	require.FileExists(t, resolveFixtureReference(t, fixtureDir, str(mainReceipt["processReceipt"])))
	require.NoFileExists(t, resolveFixtureReference(t, fixtureDir, str(probeReceipt["receipt"])))
	require.NoFileExists(t, resolveFixtureReference(t, fixtureDir, str(probeReceipt["processReceipt"])))

	openIndex := fixtureIndex(t, fixtureDir)
	require.Len(t, array(openIndex["executions"]), 1)
	require.Equal(t, "open", obj(array(openIndex["executions"])[0])["status"])

	probeRun, probeResult := runAgentResult(t, env, fixtureDir, inputs["probe"], "mock", "--connection", "probe")
	require.Zero(t, probeRun.code, probeRun.stderr)
	require.Equal(t, mainResult["executionId"], probeResult["executionId"])
	require.Equal(t, "main", mainResult["connectionId"])
	require.Equal(t, "probe", probeResult["connectionId"])
	require.NotEqual(t, mainResult["result"], probeResult["result"])
	require.FileExists(t, resolveFixtureReference(t, fixtureDir, str(probeReceipt["receipt"])))
	require.FileExists(t, resolveFixtureReference(t, fixtureDir, str(probeReceipt["processReceipt"])))
	closedIndex := fixtureIndex(t, fixtureDir)
	require.Equal(t, "passed", obj(array(closedIndex["executions"])[0])["status"])

	for _, value := range array(obj(array(closedIndex["executions"])[0])["connections"]) {
		require.Equal(t, "passed", obj(value)["status"])
	}
}

func TestPlanSeedHistoryConformance(t *testing.T) {
	inputs := builtinInputs(t, "codex", "SDK-CASE-035")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-035")

	index := materialIndex(t, fixtureDir)
	require.Len(t, array(index["materials"]), 1)
	artifact := obj(array(index["materials"])[0])
	material := fixtureMaterialRoot(t, fixtureDir, artifact)
	manifest := readJSON(t, filepath.Join(material, "manifest.json"))
	plan := readJSON(t, filepath.Join(material, "plan.json"))
	seedFiles := obj(obj(plan["historySeed"])["files"])
	require.NotEmpty(t, seedFiles)

	runPreparedMock(t, env, fixtureDir, "", inputs["main"])
	execution := latestExecution(t, fixtureDir, "mock")
	historyFiles := obj(execution["historyFiles"])
	for name, value := range seedFiles {
		digest := str(obj(value)["sha256"])
		require.Equal(t, digest, historyFiles[name], name)
		require.Equal(t, digest, digestFile(t, filepath.Join(executionOverlayRoot(t, fixtureDir, execution), filepath.FromSlash(name))), name)
		if declared := obj(manifest["savedFiles"])[name]; declared != nil {
			require.Equal(t, digest, declared, "manifest and independent plan seed bindings must agree")
		}
	}
}

func TestMockJoinInputConformance(t *testing.T) {
	inputs := builtinInputs(t, "codex", "SDK-CASE-032")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")

	t.Run("timeout nanoseconds", func(t *testing.T) {
		fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-032")
		main := runTool(t, env, t.TempDir(), inputs["main"],
			"agent", "mock", "--fixture-dir", fixtureDir, "--timeout", "1.000",
			"--", "app-server", "--listen", "stdio://",
		)
		require.Zero(t, main.code, main.stderr)

		probe := runTool(t, env, t.TempDir(), inputs["probe"],
			"agent", "mock", "--fixture-dir", fixtureDir, "--connection", "probe", "--timeout", "1.000001",
			"--", "app-server", "--listen", "stdio://",
		)
		require.NotZero(t, probe.code)
		require.Contains(t, probe.stderr, "timeout differs")
		execution := latestExecution(t, fixtureDir, "mock")
		require.Equal(t, "pending", connectionStatus(t, fixtureDir, execution, "probe"))
	})

	t.Run("progress descriptor", func(t *testing.T) {
		fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-032")
		notification, err := os.CreateTemp(t.TempDir(), "progress")
		require.NoError(t, err)
		defer notification.Close()
		main := runToolWithFiles(t, env, t.TempDir(), inputs["main"], []*os.File{notification},
			"agent", "mock", "--fixture-dir", fixtureDir, "--progress-fd", "3",
			"--", "app-server", "--listen", "stdio://",
		)
		require.Zero(t, main.code, main.stderr)

		probe := runTool(t, env, t.TempDir(), inputs["probe"],
			"agent", "mock", "--fixture-dir", fixtureDir, "--connection", "probe",
			"--", "app-server", "--listen", "stdio://",
		)
		require.NotZero(t, probe.code)
		require.Contains(t, probe.stderr, "progress descriptor differs")
		execution := latestExecution(t, fixtureDir, "mock")
		require.Equal(t, "pending", connectionStatus(t, fixtureDir, execution, "probe"))
	})
}

func TestStrictReplayFailureRetainsEvidence(t *testing.T) {
	t.Run("strict replay failure", func(t *testing.T) {
		root := t.TempDir()
		agent, _, env := isolatedCommands(t, root, "codex")
		plan := writeConsumerPlan(t, root, "strict-replay-failure")
		fixtureDir := prepareFixture(t, env, "--plan", plan)
		recordConsumer(t, env, fixtureDir, agent, requestOne)

		mock := runTool(t, env, t.TempDir(), `{"id":99,"method":"wrong"}`+"\n",
			"agent", "mock", "--fixture-dir", fixtureDir, "--", "app-server", "--listen", "stdio://",
		)
		require.NotZero(t, mock.code)
		execution := latestExecution(t, fixtureDir, "mock")
		connection := obj(array(execution["connections"])[0])
		receipt := resolveFixtureReference(t, fixtureDir, str(connection["receipt"]))
		processReceipt := resolveFixtureReference(t, fixtureDir, str(connection["processReceipt"]))
		require.FileExists(t, receipt)
		require.FileExists(t, processReceipt)
		require.Equal(t, "failed", connectionStatus(t, fixtureDir, execution, "main"))
		require.Equal(t, "failed", readJSON(t, receipt)["status"])
	})
}

func TestConformanceDescriptorsAreStableJSON(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "json-descriptors")
	fixtureDir := prepareFixture(t, env, "--plan", plan)
	recordConsumer(t, env, fixtureDir, agent, requestOne)
	runPreparedMock(t, env, fixtureDir, "", requestOne)

	descriptor := readJSON(t, filepath.Join(fixtureDir, "fixture.json"))
	index := fixtureIndex(t, fixtureDir)
	require.Equal(t, descriptor["fixtureId"], index["fixtureId"])
	require.Len(t, array(index["executions"]), 2)
	for _, value := range array(index["executions"]) {
		row := obj(value)
		require.Equal(t, row["descriptorSHA256"], digestFile(t, filepath.Join(fixtureDir, "executions", str(row["id"]), "execution.json")))
	}
}

func TestPreparedDescriptorsExcludeRuntimeAuthentication(t *testing.T) {
	const secret = "conformance-runtime-secret"
	const fileSecret = "conformance-auth-file-secret"
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	env = append(env, "OPENAI_API_KEY="+secret)
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "host-config", "auth.json"),
		[]byte(`{"syntheticToken":"`+fileSecret+`"}`+"\n"),
		0o600,
	))
	plan := writeConsumerPlan(t, root, "runtime-authentication")
	fixtureDir := prepareFixture(t, env, "--plan", plan)

	descriptor := readJSON(t, filepath.Join(fixtureDir, "fixture.json"))
	recordRoute := obj(obj(descriptor["routes"])["record"])
	require.Contains(t, array(recordRoute["runtimeEnv"]), "OPENAI_API_KEY")
	require.NotContains(t, readFile(t, filepath.Join(fixtureDir, "fixture.json")), secret)
	require.NotContains(t, readFile(t, filepath.Join(fixtureDir, "source", "plan.json")), secret)

	record := recordConsumer(t, env, fixtureDir, agent, requestOne)
	recordHome := executionOverlayPath(t, fixtureDir, record, "home")
	recordConfig := executionOverlayPath(t, fixtureDir, record, "config")
	observed := readFile(t, filepath.Join(recordHome, "runtime-secret-observed"))
	require.Equal(t, "matched\n", string(observed))
	require.NoFileExists(t, filepath.Join(recordConfig, "auth.json"))
	require.NotContains(t, readFile(t, filepath.Join(fixtureDir, "executions", str(record["executionId"]), "execution.json")), secret)
	require.NotContains(t, string(readFixtureEvidence(t, fixtureDir)), secret)
	require.NotContains(t, string(readFixtureEvidence(t, fixtureDir)), fileSecret)

	runPreparedMock(t, env, fixtureDir, "", requestOne)
	require.NotContains(t, string(readFixtureEvidence(t, fixtureDir)), secret)

	exported := filepath.Join(root, "exported")
	mustRun(t, env, root, "", "export", "--fixture-dir", fixtureDir, "--output", exported)
	require.NotContains(t, string(readAllFiles(t, exported)), secret)
	require.NotContains(t, string(readAllFiles(t, exported)), fileSecret)
	require.NoDirExists(t, filepath.Join(exported, "runtime"))
}

func TestPreparedMockRejectsIgnoredInputsBeforeClaim(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "agent",
			args: []string{"--agent", "claude"},
			want: "unsupported native startup argument",
		},
		{
			name: "report",
			args: []string{"--report", filepath.Join(t.TempDir(), "prepared-report.json")},
			want: "unsupported native startup argument",
		},
		{
			name: "json",
			args: []string{"--json"},
			want: "unsupported native startup argument",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := writeConsumerPlan(t, root, "reject-"+test.name)
			fixtureDir := prepareFixture(t, env, "--plan", plan)
			args := append([]string{"agent", "mock", "--fixture-dir", fixtureDir}, test.args...)
			args = append(args, "--", "app-server", "--listen", "stdio://")
			result := runTool(t, env, t.TempDir(), requestOne, args...)
			require.NotZero(t, result.code)
			require.Contains(t, result.stderr, test.want)
			require.Empty(t, array(fixtureIndex(t, fixtureDir)["executions"]))
		})
	}
}

func TestMockRequiresFixtureBeforeLegacyReport(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	reportPath := filepath.Join(t.TempDir(), "managed-version-report.json")
	result := runTool(t, env, t.TempDir(), "",
		"agent", "mock", "--agent", "codex", "--case", "SDK-CASE-001", "--report", reportPath,
		"--", "--version",
	)
	require.NotZero(t, result.code)
	require.Contains(t, result.stderr, "mock requires --fixture-dir")
	require.NoFileExists(t, reportPath)
}

func TestCurrentRejectsUnknownMaterialIdentity(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	current := readJSON(t, filepath.Join(fixtureDir, "current.json"))
	current["materialId"] = "missing-material"
	writeJSON(t, filepath.Join(fixtureDir, "current.json"), current)
	index := readFile(t, filepath.Join(fixtureDir, "executions", "index.json"))
	for _, action := range []string{"mock", "verify"} {
		result := runTool(t, env, t.TempDir(), "", append(publicAction(action), "--fixture-dir", fixtureDir)...)
		require.NotZero(t, result.code)
		require.Contains(t, result.stderr, "current recording is absent from material index")
		require.Equal(t, index, readFile(t, filepath.Join(fixtureDir, "executions", "index.json")))
	}
}

func TestFailedRecordKeepsLatestPublishedCapture(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "failed-record")
	fixtureDir := prepareFixture(t, env, "--plan", plan)
	recordConsumer(t, env, fixtureDir, agent, requestOne)

	before := materialIndex(t, fixtureDir)
	require.Len(t, array(before["materials"]), 1)
	latest := currentMaterialID(t, fixtureDir)

	failed := runTool(t, env, t.TempDir(), requestTwo,
		"agent", "record", "--fixture-dir", fixtureDir, "--cmd", "/usr/bin/false", "--",
		"app-server", "--listen", "stdio://",
	)
	require.NotZero(t, failed.code)
	after := materialIndex(t, fixtureDir)
	require.Equal(t, latest, currentMaterialID(t, fixtureDir))
	require.Len(t, array(after["materials"]), 1)

	require.Equal(t, "failed", obj(array(fixtureIndex(t, fixtureDir)["executions"])[1])["status"])
}
