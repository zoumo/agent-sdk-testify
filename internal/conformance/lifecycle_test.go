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
	session := prepareSession(t, env, "--plan", plan)

	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	require.InEpsilon(t, 2, descriptor["schemaVersion"], 0)
	require.Equal(t, "codex", descriptor["agentType"])
	require.Equal(t, "plan", obj(descriptor["source"])["kind"])
	require.Contains(t, str(obj(descriptor["source"])["path"]), filepath.Join(session, "source"))
	require.FileExists(t, filepath.Join(str(descriptor["workspace"]), "fixture.txt"))
	require.Equal(t, "external-sdk", obj(obj(descriptor["routes"])["record"])["executor"])
	require.Equal(t, "consumer", obj(obj(descriptor["routes"])["record"])["provenance"])
	require.NotContains(t, obj(descriptor["routes"]), "verify")
	require.Equal(t, true, obj(obj(descriptor["routes"])["mock"])["enabled"])
	require.Equal(t, true, obj(obj(descriptor["routes"])["record"])["enabled"])
	require.NotEqual(t, str(descriptor["workspace"]), str(obj(obj(descriptor["routes"])["record"])["workspace"]))
	require.NotEqual(t, str(descriptor["workspace"]), str(obj(obj(descriptor["routes"])["mock"])["workspace"]))
	recordRoute := obj(obj(descriptor["routes"])["record"])
	recordConnection := obj(array(recordRoute["connections"])[0])
	require.Equal(t, "--cmd", array(recordConnection["args"])[len(array(recordConnection["args"]))-1])

	firstRecord := recordConsumer(t, env, session, agent, requestOne)
	require.Equal(t, "record", firstRecord["command"])
	require.Equal(t, "external-sdk", firstRecord["executor"])
	require.Equal(t, "native-agent", firstRecord["target"])
	require.Equal(t, "consumer", firstRecord["provenance"])
	require.DirExists(t, str(firstRecord["workspace"]))
	firstConnection := obj(array(firstRecord["connections"])[0])
	require.Equal(t, recordConnection["command"], firstConnection["command"])
	require.Equal(t, recordConnection["args"], firstConnection["args"])
	agent, err := filepath.EvalSymlinks(agent)
	require.NoError(t, err)
	require.Equal(t,
		append(stringsOf(recordConnection["args"])[2:], agent, "--", "app-server", "--listen", "stdio://"),
		stringsOf(firstRecord["args"]),
	)
	require.NoError(t, os.WriteFile(filepath.Join(str(firstRecord["workspace"]), "only-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(str(firstRecord["config"]), "sessions", "only-first.txt"), []byte("first\n"), 0o600))

	firstIndex := materialIndex(t, session)
	require.Len(t, array(firstIndex["materials"]), 1)
	firstCapture := artifactByID(t, session, currentMaterialID(t, session))
	require.Equal(t, firstCapture["path"], firstConnection["material"])
	require.Equal(t, firstCapture["planSHA256"], firstConnection["planSHA256"])
	require.Equal(t, firstCapture["manifestSHA256"], firstConnection["manifestSHA256"])
	require.Equal(t, firstCapture["recordingSHA256"], firstConnection["recordingSHA256"])
	require.Equal(t, "capture", firstCapture["kind"])
	require.Equal(t, "record", firstCapture["action"])
	require.Equal(t, "consumer", firstCapture["provenance"])
	require.Equal(t, filepath.Join(filepath.Dir(filepath.Dir(str(firstRecord["artifactRoot"]))), "..", "materials", str(firstCapture["id"])), firstCapture["path"])
	require.Equal(t, firstCapture["planSHA256"], digestFile(t, filepath.Join(str(firstCapture["path"]), "plan.json")))
	require.Equal(t, firstCapture["manifestSHA256"], digestFile(t, filepath.Join(str(firstCapture["path"]), "manifest.json")))
	require.Equal(t, firstCapture["recordingSHA256"], digestFile(t, filepath.Join(str(firstCapture["path"]), "raw", "raw.jsonl")))

	manifest := readJSON(t, filepath.Join(str(firstCapture["path"]), "manifest.json"))
	require.Equal(t, "consumer", manifest["provenance"])
	require.Equal(t, "captured", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.Equal(t, "pass", manifest["credentialScan"])
	const history = "config/sessions/conformance-history.jsonl"
	require.Equal(t, digestBytes([]byte("{\"type\":\"conformance-history\"}\n")), obj(manifest["savedFiles"])[history])

	secondRecord := recordConsumer(t, env, session, agent, requestTwo)
	require.NotEqual(t, firstRecord["executionId"], secondRecord["executionId"])
	requireDistinct(t,
		str(firstRecord["workspace"]), str(secondRecord["workspace"]),
		str(firstRecord["home"]), str(secondRecord["home"]),
		str(firstRecord["config"]), str(secondRecord["config"]),
		str(firstRecord["tmp"]), str(secondRecord["tmp"]),
		str(firstRecord["artifactRoot"]), str(secondRecord["artifactRoot"]),
	)
	require.NoFileExists(t, filepath.Join(str(secondRecord["workspace"]), "only-first.txt"))
	require.NoFileExists(t, filepath.Join(str(secondRecord["config"]), "sessions", "only-first.txt"))
	require.FileExists(t, filepath.Join(str(secondRecord["workspace"]), "fixture.txt"))
	require.NoFileExists(t, filepath.Join(str(descriptor["workspace"]), "only-first.txt"))

	secondIndex := materialIndex(t, session)
	require.Len(t, array(secondIndex["materials"]), 2)
	secondCapture := artifactByID(t, session, currentMaterialID(t, session))
	require.NotEqual(t, firstCapture["id"], secondCapture["id"])
	require.Equal(t, filepath.Join(filepath.Dir(filepath.Dir(str(secondRecord["artifactRoot"]))), "..", "materials", str(secondCapture["id"])), secondCapture["path"])

	latestMock := runPreparedMock(t, env, session, "", requestTwo)
	require.JSONEq(t, `{"id":2,"result":{"synthetic":true}}`, latestMock.stdout)
	latestMockExecution := latestExecution(t, session, "mock")
	require.Equal(t, "consumer", latestMockExecution["provenance"])
	require.Equal(t, str(secondCapture["path"]), obj(latestMockExecution["artifact"])["path"])
	latestConnection := obj(array(latestMockExecution["connections"])[0])
	routeConnection := obj(array(obj(obj(descriptor["routes"])["mock"])["connections"])[0])
	require.Equal(t, routeConnection["command"], latestConnection["command"])
	require.Equal(t, routeConnection["args"], latestConnection["args"])
	require.Equal(t, routeConnection["launcher"], latestConnection["launcher"])
	require.Equal(t, str(secondCapture["path"]), latestConnection["material"])
	require.Equal(t, secondCapture["planSHA256"], latestConnection["planSHA256"])
	require.Equal(t, secondCapture["manifestSHA256"], latestConnection["manifestSHA256"])
	require.Equal(t, secondCapture["recordingSHA256"], latestConnection["recordingSHA256"])
	require.NoFileExists(t, filepath.Join(str(latestMockExecution["workspace"]), "only-first.txt"))
	historyBytes := readFile(t, filepath.Join(str(latestMockExecution["config"]), "sessions", "conformance-history.jsonl"))
	require.True(t, bytes.Equal([]byte("{\"type\":\"conformance-history\"}\n"), historyBytes), "unexpected history bytes: %q", historyBytes)

	rejectedOverride := runTool(t, env, t.TempDir(), requestOne,
		"agent", "mock", "--session-dir", session, "--recording", str(firstCapture["path"]))
	require.NotZero(t, rejectedOverride.code)
	require.Contains(t, rejectedOverride.stderr, "unsupported native startup argument")
	afterMocks := materialIndex(t, session)
	require.Equal(t, secondCapture["id"], currentMaterialID(t, session))
	require.Len(t, array(afterMocks["materials"]), 2)

	rejected := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
	require.NotZero(t, rejected.code)
	require.Contains(t, strings.ToLower(rejected.stderr), "no applicable official verifier")
	for _, row := range array(sessionIndex(t, session)["executions"]) {
		require.NotEqual(t, "verify", obj(row)["command"])
	}

	resourceRoot := str(descriptor["resourceRoot"])
	sourcePath := str(obj(descriptor["source"])["path"])
	firstFinishResult, firstFinish := finishSession(t, env, session)
	require.Zero(t, firstFinishResult.code, firstFinishResult.stderr)
	require.Equal(t, "passed", firstFinish["status"])
	require.Equal(t, true, firstFinish["cleanupComplete"])
	require.Len(t, array(firstFinish["executions"]), 3)
	for _, value := range array(firstFinish["executions"]) {
		require.Equal(t, "passed", obj(value)["status"])
	}
	require.NoDirExists(t, resourceRoot)
	require.FileExists(t, sourcePath)
	require.FileExists(t, filepath.Join(str(firstCapture["path"]), "manifest.json"))
	require.FileExists(t, filepath.Join(str(secondCapture["path"]), "manifest.json"))

	secondFinishResult, secondFinish := finishSession(t, env, session)
	require.Zero(t, secondFinishResult.code, secondFinishResult.stderr)
	require.Equal(t, firstFinish["status"], secondFinish["status"])
	require.Equal(t, firstFinish["cleanupComplete"], secondFinish["cleanupComplete"])
	require.Len(t, array(secondFinish["executions"]), 3)

	blocked := runTool(t, env, t.TempDir(), requestTwo,
		"agent", "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://",
	)
	require.NotZero(t, blocked.code)
	require.Contains(t, blocked.stderr, "finished")
}

func TestOfficialLifecycleConformance(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	setControlledOfficialDriver(t, drivers, "local")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	recordControlledOfficial(t, env, session, agent)
	execution := latestExecution(t, session, "record")
	require.Equal(t, "external-sdk", execution["executor"])
	require.Equal(t, "native-agent", execution["target"])
	require.Equal(t, "official", execution["provenance"])
	capture := latestCapture(t, session)
	require.Equal(t, "capture", capture["kind"])
	require.Equal(t, "official", capture["provenance"])
	assertFileHashes(t, str(capture["path"]), obj(capture["files"]))
	plan := readJSON(t, filepath.Join(str(capture["path"]), "plan.json"))
	require.Equal(t, "SDK-CASE-001", plan["caseId"])
	require.Equal(t, "continuation", plan["driverProfile"])
	require.Equal(t, "personal-skills-v1", plan["recordingIsolation"])
	require.Equal(t, obj(obj(readJSON(t, filepath.Join(session, "session.json"))["routes"])["record"])["workspace"], plan["project"])
	manifest := readJSON(t, filepath.Join(str(capture["path"]), "manifest.json"))
	require.Equal(t, "capture", manifest["provenance"])
	require.Equal(t, "captured", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.Equal(t, "pass", manifest["credentialScan"])
	report := readJSON(t, filepath.Join(str(capture["path"]), "report.json"))
	require.Equal(t, "captured", report["status"])
	require.NotContains(t, report, "officialSDK")
	corpus := newSyntheticCorpus(t)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	verificationRoot, evidence := latestOfficialVerification(t, session)
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, capture["id"], obj(evidence["material"])["id"])
	admission := obj(evidence["admission"])
	require.Equal(t, filepath.Clean(corpus.index), admission["index"])
	require.Equal(t, filepath.Clean(corpus.scenarios), admission["scenarios"])
	require.True(t, filepath.IsAbs(str(admission["archive"])))
	require.Equal(t, "synthetic-official-driver", obj(evidence["officialSDK"])["version"])
	assertFileHashes(t, verificationRoot, obj(evidence["files"]))
	admitted := readJSON(t, filepath.Join(verificationRoot, "admission-summary.json"))
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
	runPreparedMock(t, env, session, "", conformancePayload(t, capture))
	replay := latestExecution(t, session, "mock")
	require.Equal(t, capture["id"], obj(replay["artifact"])["id"])
	require.Equal(t, "official", replay["provenance"])
	finished, finish := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
	require.Equal(t, "passed", finish["status"])
	require.Equal(t, true, finish["cleanupComplete"])
	require.Len(t, array(finish["executions"]), 3)
	for _, item := range array(finish["executions"]) {
		require.Equal(t, "passed", obj(item)["status"])
	}
	require.NoDirExists(t, str(readJSON(t, filepath.Join(session, "session.json"))["resourceRoot"]))
}

func conformancePayload(t *testing.T, material object) string {
	t.Helper()
	plan := readJSON(t, filepath.Join(str(material["path"]), "plan.json"))
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
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-032")

	mainRun, mainResult := runAgentResult(t, env, session, inputs["main"], "mock", "--connection", "main")
	require.Zero(t, mainRun.code, mainRun.stderr)
	execution := readJSON(t, str(mainResult["execution"]))
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
	require.FileExists(t, str(mainReceipt["receipt"]))
	require.FileExists(t, str(mainReceipt["processReceipt"]))
	require.NoFileExists(t, str(probeReceipt["receipt"]))
	require.NoFileExists(t, str(probeReceipt["processReceipt"]))

	openIndex := sessionIndex(t, session)
	require.Len(t, array(openIndex["executions"]), 1)
	require.Equal(t, "open", obj(array(openIndex["executions"])[0])["status"])

	probeRun, probeResult := runAgentResult(t, env, session, inputs["probe"], "mock", "--connection", "probe")
	require.Zero(t, probeRun.code, probeRun.stderr)
	require.Equal(t, mainResult["executionId"], probeResult["executionId"])
	require.Equal(t, "main", mainResult["connectionId"])
	require.Equal(t, "probe", probeResult["connectionId"])
	require.NotEqual(t, mainResult["result"], probeResult["result"])
	require.FileExists(t, str(probeReceipt["receipt"]))
	require.FileExists(t, str(probeReceipt["processReceipt"]))
	closedIndex := sessionIndex(t, session)
	require.Equal(t, "passed", obj(array(closedIndex["executions"])[0])["status"])

	finishResult, finish := finishSession(t, env, session)
	require.Zero(t, finishResult.code, finishResult.stderr)
	require.Equal(t, "passed", finish["status"])
	require.Len(t, array(finish["executions"]), 1)
	finishedExecution := obj(array(finish["executions"])[0])
	require.Len(t, array(finishedExecution["connections"]), 2)
	for _, value := range array(finishedExecution["connections"]) {
		require.Equal(t, "passed", obj(value)["status"])
	}
}

func TestPlanSeedHistoryConformance(t *testing.T) {
	inputs := builtinInputs(t, "codex", "SDK-CASE-035")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-035")

	index := materialIndex(t, session)
	require.Len(t, array(index["materials"]), 1)
	artifact := obj(array(index["materials"])[0])
	material := str(artifact["path"])
	manifest := readJSON(t, filepath.Join(material, "manifest.json"))
	plan := readJSON(t, filepath.Join(material, "plan.json"))
	seedFiles := obj(obj(plan["historySeed"])["files"])
	require.NotEmpty(t, seedFiles)

	runPreparedMock(t, env, session, "", inputs["main"])
	execution := latestExecution(t, session, "mock")
	historyFiles := obj(execution["historyFiles"])
	for name, value := range seedFiles {
		digest := str(obj(value)["sha256"])
		require.Equal(t, digest, historyFiles[name], name)
		require.Equal(t, digest, digestFile(t, filepath.Join(filepath.Dir(str(execution["home"])), filepath.FromSlash(name))), name)
		require.Nil(t, obj(manifest["savedFiles"])[name], "plan seed must provide an independent binding")
	}
}

func TestMockJoinInputConformance(t *testing.T) {
	inputs := builtinInputs(t, "codex", "SDK-CASE-032")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")

	t.Run("timeout nanoseconds", func(t *testing.T) {
		session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-032")
		main := runTool(t, env, t.TempDir(), inputs["main"],
			"agent", "mock", "--session-dir", session, "--timeout", "1.000",
			"--", "app-server", "--listen", "stdio://",
		)
		require.Zero(t, main.code, main.stderr)

		probe := runTool(t, env, t.TempDir(), inputs["probe"],
			"agent", "mock", "--session-dir", session, "--connection", "probe", "--timeout", "1.000001",
			"--", "app-server", "--listen", "stdio://",
		)
		require.NotZero(t, probe.code)
		require.Contains(t, probe.stderr, "timeout differs")
		execution := latestExecution(t, session, "mock")
		require.Equal(t, "pending", connectionStatus(t, session, execution, "probe"))
	})

	t.Run("progress descriptor", func(t *testing.T) {
		session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-032")
		notification, err := os.CreateTemp(t.TempDir(), "progress")
		require.NoError(t, err)
		defer notification.Close()
		main := runToolWithFiles(t, env, t.TempDir(), inputs["main"], []*os.File{notification},
			"agent", "mock", "--session-dir", session, "--progress-fd", "3",
			"--", "app-server", "--listen", "stdio://",
		)
		require.Zero(t, main.code, main.stderr)

		probe := runTool(t, env, t.TempDir(), inputs["probe"],
			"agent", "mock", "--session-dir", session, "--connection", "probe",
			"--", "app-server", "--listen", "stdio://",
		)
		require.NotZero(t, probe.code)
		require.Contains(t, probe.stderr, "progress descriptor differs")
		execution := latestExecution(t, session, "mock")
		require.Equal(t, "pending", connectionStatus(t, session, execution, "probe"))
	})
}

func TestStrictReplayAndTamperConformance(t *testing.T) {
	t.Run("strict replay failure", func(t *testing.T) {
		root := t.TempDir()
		agent, _, env := isolatedCommands(t, root, "codex")
		plan := writeConsumerPlan(t, root, "strict-replay-failure")
		session := prepareSession(t, env, "--plan", plan)
		recordConsumer(t, env, session, agent, requestOne)

		mock := runTool(t, env, t.TempDir(), `{"id":99,"method":"wrong"}`+"\n",
			"agent", "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://",
		)
		require.NotZero(t, mock.code)
		execution := latestExecution(t, session, "mock")
		connection := obj(array(execution["connections"])[0])
		require.FileExists(t, str(connection["receipt"]))
		require.FileExists(t, str(connection["processReceipt"]))

		for range 2 {
			finishResult, finish := finishSession(t, env, session)
			require.NotZero(t, finishResult.code)
			require.Equal(t, "failed", finish["status"])
			require.Equal(t, true, finish["cleanupComplete"])
			require.FileExists(t, filepath.Join(session, "finish.json"))
			require.FileExists(t, filepath.Join(session, "executions", str(execution["executionId"]), "finish.json"))
			require.FileExists(t, str(connection["receipt"]))
		}
	})

	t.Run("receipt tamper", func(t *testing.T) {
		root := t.TempDir()
		agent, _, env := isolatedCommands(t, root, "codex")
		plan := writeConsumerPlan(t, root, "finish-tamper")
		session := prepareSession(t, env, "--plan", plan)
		recordConsumer(t, env, session, agent, requestOne)
		runPreparedMock(t, env, session, "", requestOne)

		execution := latestExecution(t, session, "mock")
		connection := obj(array(execution["connections"])[0])
		receiptPath := str(connection["receipt"])
		receipt := readJSON(t, receiptPath)
		receipt["recordingSHA256"] = strings.Repeat("0", 64)
		writeJSON(t, receiptPath, receipt)

		for range 2 {
			finishResult, finish := finishSession(t, env, session)
			require.NotZero(t, finishResult.code)
			require.Equal(t, "failed", finish["status"])
			require.Equal(t, true, finish["cleanupComplete"])
			var finishedExecution object
			for _, value := range array(finish["executions"]) {
				row := obj(value)
				if row["id"] == execution["executionId"] {
					finishedExecution = row
					break
				}
			}
			require.NotNil(t, finishedExecution)
			require.Equal(t, "failed", finishedExecution["status"])
			require.Equal(t, "failed", obj(array(finishedExecution["connections"])[0])["status"])
			require.FileExists(t, receiptPath)
		}
	})
}

func TestConformanceDescriptorsAreStableJSON(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "json-descriptors")
	session := prepareSession(t, env, "--plan", plan)
	recordConsumer(t, env, session, agent, requestOne)
	runPreparedMock(t, env, session, "", requestOne)

	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	index := sessionIndex(t, session)
	require.Equal(t, descriptor["sessionId"], index["sessionId"])
	require.Len(t, array(index["executions"]), 2)
	for _, value := range array(index["executions"]) {
		row := obj(value)
		require.Equal(t, row["descriptorSHA256"], digestFile(t, filepath.Join(session, "executions", str(row["id"]), "execution.json")))
	}

	finishResult, finish := finishSession(t, env, session)
	require.Zero(t, finishResult.code, finishResult.stderr)
	persisted := readJSON(t, filepath.Join(session, "finish.json"))
	require.Equal(t, finish, persisted)
	require.Equal(t, "passed", persisted["status"])
	var encoded map[string]any
	require.NoError(t, json.Unmarshal(readFile(t, filepath.Join(session, "finish.json")), &encoded))
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
	session := prepareSession(t, env, "--plan", plan)

	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	recordRoute := obj(obj(descriptor["routes"])["record"])
	require.Contains(t, array(recordRoute["runtimeEnv"]), "OPENAI_API_KEY")
	require.NotContains(t, readFile(t, filepath.Join(session, "session.json")), secret)
	require.NotContains(t, readFile(t, filepath.Join(session, "source", "plan.json")), secret)

	record := recordConsumer(t, env, session, agent, requestOne)
	observed := readFile(t, filepath.Join(str(record["home"]), "runtime-secret-observed"))
	require.Equal(t, "matched\n", string(observed))
	require.NoFileExists(t, filepath.Join(str(record["config"]), "auth.json"))
	require.NotContains(t, readFile(t, filepath.Join(session, "executions", str(record["executionId"]), "execution.json")), secret)
	require.NotContains(t, string(readAllFiles(t, session)), secret)
	require.NotContains(t, string(readAllFiles(t, session)), fileSecret)

	runPreparedMock(t, env, session, "", requestOne)
	require.NotContains(t, string(readAllFiles(t, session)), secret)

	finishResult, finish := finishSession(t, env, session)
	require.Zero(t, finishResult.code, finishResult.stderr)
	require.Equal(t, "passed", finish["status"])
	require.NotContains(t, string(readAllFiles(t, session)), secret)
	require.NotContains(t, string(readAllFiles(t, session)), fileSecret)
	require.NoDirExists(t, str(descriptor["resourceRoot"]))
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
			session := prepareSession(t, env, "--plan", plan)
			args := append([]string{"agent", "mock", "--session-dir", session}, test.args...)
			args = append(args, "--", "app-server", "--listen", "stdio://")
			result := runTool(t, env, t.TempDir(), requestOne, args...)
			require.NotZero(t, result.code)
			require.Contains(t, result.stderr, test.want)
			require.Empty(t, array(sessionIndex(t, session)["executions"]))
		})
	}
}

func TestMockRequiresSessionBeforeLegacyReport(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	reportPath := filepath.Join(t.TempDir(), "managed-version-report.json")
	result := runTool(t, env, t.TempDir(), "",
		"agent", "mock", "--agent", "codex", "--case", "SDK-CASE-001", "--report", reportPath,
		"--", "--version",
	)
	require.NotZero(t, result.code)
	require.Contains(t, result.stderr, "mock requires --session-dir")
	require.NoFileExists(t, reportPath)
}

func TestCurrentRejectsUnknownMaterialIdentity(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	current := readJSON(t, filepath.Join(session, "current.json"))
	current["materialId"] = "missing-material"
	writeJSON(t, filepath.Join(session, "current.json"), current)
	index := readFile(t, filepath.Join(session, "executions", "index.json"))
	for _, action := range []string{"mock", "verify"} {
		result := runTool(t, env, t.TempDir(), "", append(publicAction(action), "--session-dir", session)...)
		require.NotZero(t, result.code)
		require.Contains(t, result.stderr, "current recording is absent from material index")
		require.Equal(t, index, readFile(t, filepath.Join(session, "executions", "index.json")))
	}
}

func TestFailedRecordKeepsLatestPublishedCapture(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "failed-record")
	session := prepareSession(t, env, "--plan", plan)
	recordConsumer(t, env, session, agent, requestOne)

	before := materialIndex(t, session)
	require.Len(t, array(before["materials"]), 1)
	latest := currentMaterialID(t, session)

	failed := runTool(t, env, t.TempDir(), requestTwo,
		"agent", "record", "--session-dir", session, "--cmd", "/usr/bin/false", "--",
		"app-server", "--listen", "stdio://",
	)
	require.NotZero(t, failed.code)
	after := materialIndex(t, session)
	require.Equal(t, latest, currentMaterialID(t, session))
	require.Len(t, array(after["materials"]), 1)

	finishResult, finish := finishSession(t, env, session)
	require.NotZero(t, finishResult.code)
	require.Equal(t, "failed", finish["status"])
	require.Equal(t, true, finish["cleanupComplete"])
}

func TestHistoryDriftAndAbandonedSessionConformance(t *testing.T) {
	t.Run("history drift", func(t *testing.T) {
		root := t.TempDir()
		agent, _, env := isolatedCommands(t, root, "codex")
		plan := writeConsumerPlan(t, root, "history-drift")
		session := prepareSession(t, env, "--plan", plan)
		recordConsumer(t, env, session, agent, requestOne)
		runPreparedMock(t, env, session, "", requestOne)
		execution := latestExecution(t, session, "mock")
		history := filepath.Join(str(execution["config"]), "sessions", "conformance-history.jsonl")
		require.NoError(t, os.WriteFile(history, []byte("changed\n"), 0o600))

		finishResult, finish := finishSession(t, env, session)
		require.NotZero(t, finishResult.code)
		require.Equal(t, "failed", finish["status"])
		require.Contains(t, str(finish["error"]), "history hash mismatch")
	})

	t.Run("abandoned", func(t *testing.T) {
		root := t.TempDir()
		_, _, env := isolatedCommands(t, root, "codex")
		plan := writeConsumerPlan(t, root, "abandoned")
		session := prepareSession(t, env, "--plan", plan)

		finishResult, finish := finishSession(t, env, session)
		require.Zero(t, finishResult.code, finishResult.stderr)
		require.Equal(t, "abandoned", finish["status"])
		require.Equal(t, true, finish["cleanupComplete"])
		require.Empty(t, array(finish["executions"]))
	})
}
