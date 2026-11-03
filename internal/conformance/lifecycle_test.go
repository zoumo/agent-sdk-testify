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
	require.InEpsilon(t, 1, descriptor["schemaVersion"], 0)
	require.Equal(t, "codex", descriptor["agentType"])
	require.Equal(t, "plan", obj(descriptor["source"])["kind"])
	require.Contains(t, str(obj(descriptor["source"])["path"]), filepath.Join(session, "source"))
	require.FileExists(t, filepath.Join(str(descriptor["workspace"]), "fixture.txt"))
	require.Equal(t, "external-sdk", obj(obj(descriptor["routes"])["record"])["executor"])
	require.Equal(t, "consumer", obj(obj(descriptor["routes"])["record"])["provenance"])
	require.Equal(t, false, obj(obj(descriptor["routes"])["verify"])["enabled"])
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
		append(stringsOf(recordConnection["args"])[1:], agent, "--", "app-server", "--listen", "stdio://"),
		stringsOf(firstRecord["args"]),
	)
	require.NoError(t, os.WriteFile(filepath.Join(str(firstRecord["workspace"]), "only-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(str(firstRecord["config"]), "sessions", "only-first.txt"), []byte("first\n"), 0o600))

	firstIndex := sessionIndex(t, session)
	require.Len(t, array(firstIndex["artifacts"]), 1)
	firstCapture := artifactByID(t, session, str(firstIndex["latestCapture"]))
	require.Equal(t, firstCapture["path"], firstConnection["material"])
	require.Equal(t, firstCapture["planSHA256"], firstConnection["planSHA256"])
	require.Equal(t, firstCapture["manifestSHA256"], firstConnection["manifestSHA256"])
	require.Equal(t, firstCapture["recordingSHA256"], firstConnection["recordingSHA256"])
	require.Equal(t, "capture", firstCapture["kind"])
	require.Equal(t, "record", firstCapture["action"])
	require.Equal(t, "consumer", firstCapture["provenance"])
	require.Equal(t, str(firstRecord["artifactRoot"]), firstCapture["path"])
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

	secondIndex := sessionIndex(t, session)
	require.Len(t, array(secondIndex["artifacts"]), 2)
	secondCapture := artifactByID(t, session, str(secondIndex["latestCapture"]))
	require.NotEqual(t, firstCapture["id"], secondCapture["id"])
	require.Equal(t, str(secondRecord["artifactRoot"]), secondCapture["path"])

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

	explicitMock := runPreparedMock(t, env, session, "", requestOne, "--recording", str(firstCapture["path"]))
	require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, explicitMock.stdout)
	explicitMockExecution := latestExecution(t, session, "mock")
	require.Equal(t, str(firstCapture["path"]), obj(explicitMockExecution["artifact"])["path"])
	require.Equal(t, firstCapture["id"], obj(explicitMockExecution["artifact"])["id"])
	afterMocks := sessionIndex(t, session)
	require.Equal(t, secondCapture["id"], afterMocks["latestCapture"])
	require.Len(t, array(afterMocks["artifacts"]), 2)

	rejected := runTool(t, env, t.TempDir(), "", "verify", "--session-dir", session)
	require.NotZero(t, rejected.code)
	require.Contains(t, strings.ToLower(rejected.stderr), "consumer material cannot be verified")
	for _, row := range array(sessionIndex(t, session)["executions"]) {
		require.NotEqual(t, "verify", obj(row)["command"])
	}

	resourceRoot := str(descriptor["resourceRoot"])
	sourcePath := str(obj(descriptor["source"])["path"])
	firstFinishResult, firstFinish := finishSession(t, env, session)
	require.Zero(t, firstFinishResult.code, firstFinishResult.stderr)
	require.Equal(t, "passed", firstFinish["status"])
	require.Equal(t, true, firstFinish["cleanupComplete"])
	require.Len(t, array(firstFinish["executions"]), 4)
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
	require.Len(t, array(secondFinish["executions"]), 4)

	blocked := runTool(t, env, t.TempDir(), requestTwo,
		"mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://",
	)
	require.NotZero(t, blocked.code)
	require.Contains(t, blocked.stderr, "finished")
}

func TestOfficialLifecycleConformance(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	installDriver(t, drivers, "codex", "record")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")

	record := mustRun(t, env, t.TempDir(), "",
		"record", "--session-dir", session, "--cmd", agent, "--",
		"app-server", "--listen", "stdio://",
	)
	require.NotEmpty(t, record.stdout)
	recordExecution := latestExecution(t, session, "record")
	require.Equal(t, "official-driver", recordExecution["executor"])
	require.Equal(t, "native-agent", recordExecution["target"])
	require.Equal(t, "official", recordExecution["provenance"])

	capture := latestCapture(t, session)
	require.Equal(t, "capture", capture["kind"])
	require.Equal(t, "official", capture["provenance"])
	require.Equal(t, str(recordExecution["artifactRoot"]), capture["path"])
	plan := readJSON(t, filepath.Join(str(capture["path"]), "plan.json"))
	manifest := readJSON(t, filepath.Join(str(capture["path"]), "manifest.json"))
	transport := readJSON(t, filepath.Join(str(capture["path"]), "raw", "transport.json"))
	report := readJSON(t, filepath.Join(str(capture["path"]), "report.json"))
	require.Equal(t, "SDK-CASE-001", plan["caseId"])
	require.Equal(t, "continuation", plan["driverProfile"])
	require.Equal(t, "personal-skills-v1", plan["recordingIsolation"])
	require.Equal(t, str(recordExecution["workspace"]), plan["project"])
	require.Equal(t, "capture", manifest["provenance"])
	require.Equal(t, "passed", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.Equal(t, "pass", manifest["credentialScan"])
	require.Equal(t, "complete", transport["captureStatus"])
	require.Equal(t, true, transport["cleanupComplete"])
	require.Equal(t, "passed", report["status"])
	require.Equal(t, "passed", report["branchStatus"])
	require.Equal(t, "complete", report["captureStatus"])
	require.Equal(t, "openai-codex", obj(report["officialSDK"])["package"])

	installDriver(t, drivers, "codex", "verify")
	corpus := newSyntheticCorpus(t)
	verified := mustRun(t, env, t.TempDir(), "",
		"verify", "--session-dir", session,
		"--index", corpus.index,
		"--scenarios", corpus.scenarios,
		"--archive", corpus.archive,
	)
	require.NotEmpty(t, verified.stdout)

	verifyExecution := latestExecution(t, session, "verify")
	require.Equal(t, "official-driver", verifyExecution["executor"])
	require.Equal(t, "replay", verifyExecution["target"])
	require.Equal(t, "official", verifyExecution["provenance"])
	require.Equal(t, capture["path"], obj(verifyExecution["artifact"])["path"])
	admission := obj(verifyExecution["admission"])
	require.Equal(t, filepath.Clean(corpus.index), admission["index"])
	require.Equal(t, filepath.Clean(corpus.scenarios), admission["scenarios"])
	require.True(t, filepath.IsAbs(str(admission["archive"])))
	require.Equal(t, filepath.Base(corpus.archive), filepath.Base(str(admission["archive"])))

	evidence := readJSON(t, filepath.Join(str(verifyExecution["artifactRoot"]), "verification.json"))
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, admission, obj(evidence["admission"]))
	require.Equal(t, "conformance-test-only", obj(evidence["officialSDK"])["version"])
	assertFileHashes(t, str(verifyExecution["artifactRoot"]), obj(evidence["files"]))
	admissionSummary := readJSON(t, filepath.Join(str(verifyExecution["artifactRoot"]), "admission-summary.json"))
	require.Equal(t, "admitted", admissionSummary["status"])
	require.DirExists(t, str(admissionSummary["materials"]))

	updatedCorpus := readJSON(t, corpus.index)
	adopted := object(nil)
	for _, value := range array(updatedCorpus["cases"]) {
		row := obj(value)
		if row["provider"] == "codex" && row["caseId"] == "SDK-CASE-001" {
			adopted = row
			break
		}
	}
	require.NotNil(t, adopted)
	require.Equal(t, "admitted", adopted["adoption"])
	require.Equal(t, "passed", adopted["verification"])

	mock := runPreparedMock(t, env, session, "", conformanceRequest)
	require.NotEmpty(t, mock.stdout)
	mockExecution := latestExecution(t, session, "mock")
	require.Equal(t, "official", mockExecution["provenance"])
	require.Equal(t, capture["path"], obj(mockExecution["artifact"])["path"])
	require.Equal(t, capture["id"], obj(mockExecution["artifact"])["id"])

	finishResult, finish := finishSession(t, env, session)
	require.Zero(t, finishResult.code, finishResult.stderr)
	require.Equal(t, "passed", finish["status"])
	require.Equal(t, true, finish["cleanupComplete"])
	require.Len(t, array(finish["executions"]), 3)
	for _, value := range array(finish["executions"]) {
		require.Equal(t, "passed", obj(value)["status"])
	}
	require.NoDirExists(t, str(readJSON(t, filepath.Join(session, "session.json"))["resourceRoot"]))
	require.FileExists(t, filepath.Join(str(capture["path"]), "manifest.json"))
	require.FileExists(t, filepath.Join(str(admissionSummary["materials"]), "adoption.json"))
}

func TestMultiConnectionBuiltinConformance(t *testing.T) {
	_, inputs := describeBuiltin(t, "codex", "SDK-CASE-032")
	require.Contains(t, inputs, "main")
	require.Contains(t, inputs, "probe")

	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-032")

	runPreparedMock(t, env, session, "main", inputs["main"])
	execution := latestExecution(t, session, "mock")
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

	runPreparedMock(t, env, session, "probe", inputs["probe"])
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
	_, inputs := describeBuiltin(t, "codex", "SDK-CASE-035")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-035")

	index := sessionIndex(t, session)
	require.Len(t, array(index["artifacts"]), 1)
	artifact := obj(array(index["artifacts"])[0])
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
	_, inputs := describeBuiltin(t, "codex", "SDK-CASE-032")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")

	t.Run("timeout nanoseconds", func(t *testing.T) {
		session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-032")
		main := runTool(t, env, t.TempDir(), inputs["main"],
			"mock", "--session-dir", session, "--timeout", "1.000",
			"--", "app-server", "--listen", "stdio://",
		)
		require.Zero(t, main.code, main.stderr)

		probe := runTool(t, env, t.TempDir(), inputs["probe"],
			"mock", "--session-dir", session, "--connection", "probe", "--timeout", "1.000001",
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
			"mock", "--session-dir", session, "--progress-fd", "3",
			"--", "app-server", "--listen", "stdio://",
		)
		require.Zero(t, main.code, main.stderr)

		probe := runTool(t, env, t.TempDir(), inputs["probe"],
			"mock", "--session-dir", session, "--connection", "probe",
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
			"mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://",
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

func TestManagedMockAliasConformance(t *testing.T) {
	_, inputs := describeBuiltin(t, "codex", "SDK-CASE-001")
	require.Contains(t, inputs, "main")
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	reportPath := filepath.Join(t.TempDir(), "managed-report.json")

	result := mustRun(t, env, t.TempDir(), inputs["main"],
		"mock", "--agent", "codex", "--case", "SDK-CASE-001", "--report", reportPath,
	)
	require.NotEmpty(t, result.stdout)
	report := readJSON(t, reportPath)
	require.InEpsilon(t, 1, report["schemaVersion"], 0)
	require.Equal(t, true, report["managed"])
	require.Equal(t, "passed", report["status"])
	require.Equal(t, "builtin", report["provenance"])
	require.NotEmpty(t, report["sessionId"])
	require.NotEmpty(t, report["executionId"])
	require.Equal(t, "passed", obj(report["finish"])["status"])
	require.Equal(t, true, obj(report["finish"])["cleanupComplete"])
	session := obj(report["session"])
	require.Equal(t, "case", obj(session["source"])["kind"])
	require.NotNil(t, obj(session["routes"])["record"])
	execution := obj(report["execution"])
	require.Equal(t, "mock", execution["command"])
	require.Equal(t, "builtin", execution["provenance"])
	require.NoDirExists(t, str(session["resourceRoot"]))
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
	require.NoFileExists(t, filepath.Join(session, "executions", str(record["executionId"]), "overlay", "config", "auth.json"))
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
			want: "agent type differs",
		},
		{
			name: "report",
			args: []string{"--report", filepath.Join(t.TempDir(), "prepared-report.json")},
			want: "--report is only valid for the managed alias",
		},
		{
			name: "json",
			args: []string{"--json"},
			want: "--json is only valid with --list or --describe",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := writeConsumerPlan(t, root, "reject-"+test.name)
			session := prepareSession(t, env, "--plan", plan)
			args := append([]string{"mock", "--session-dir", session}, test.args...)
			args = append(args, "--", "app-server", "--listen", "stdio://")
			result := runTool(t, env, t.TempDir(), requestOne, args...)
			require.NotZero(t, result.code)
			require.Contains(t, result.stderr, test.want)
			require.Empty(t, array(sessionIndex(t, session)["executions"]))
		})
	}
}

func TestManagedMockVersionProbeIsRejectedBeforeReport(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	reportPath := filepath.Join(t.TempDir(), "managed-version-report.json")
	result := runTool(t, env, t.TempDir(), "",
		"mock", "--agent", "codex", "--case", "SDK-CASE-001", "--report", reportPath,
		"--", "--version",
	)
	require.NotZero(t, result.code)
	require.Contains(t, result.stderr, "managed mock alias does not support --version")
	require.NoFileExists(t, reportPath)
}

func TestLatestCaptureRejectsMaterialArtifactIdentity(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	index := sessionIndex(t, session)
	materials := array(index["artifacts"])
	require.NotEmpty(t, materials)
	material := obj(materials[0])
	require.Equal(t, "material", material["kind"])
	index["latestCapture"] = material["id"]
	writeJSON(t, filepath.Join(session, "executions", "index.json"), index)

	mock := runTool(t, env, t.TempDir(), conformanceRequest,
		"mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://",
	)
	require.NotZero(t, mock.code)
	require.Contains(t, mock.stderr, "latest capture is not a record capture")

	finish := runTool(t, env, t.TempDir(), "", "finish", "--session-dir", session)
	require.NotZero(t, finish.code)
	require.Contains(t, finish.stderr, "latest capture is not a record capture")
}

func TestFailedRecordKeepsLatestPublishedCapture(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "failed-record")
	session := prepareSession(t, env, "--plan", plan)
	recordConsumer(t, env, session, agent, requestOne)

	before := sessionIndex(t, session)
	require.Len(t, array(before["artifacts"]), 1)
	latest := before["latestCapture"]

	failed := runTool(t, env, t.TempDir(), requestTwo,
		"record", "--session-dir", session, "--cmd", "/usr/bin/false", "--",
		"app-server", "--listen", "stdio://",
	)
	require.NotZero(t, failed.code)
	after := sessionIndex(t, session)
	require.Equal(t, latest, after["latestCapture"])
	require.Len(t, array(after["artifacts"]), 1)

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
