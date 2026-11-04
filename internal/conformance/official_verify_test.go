package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func latestOfficialVerification(t *testing.T, session string) (string, object) {
	t.Helper()
	var ids []string
	require.NoError(t, readJSONFile(filepath.Join(session, "verifications", "index.json"), &ids))
	require.NotEmpty(t, ids)
	root := filepath.Join(session, "verifications", ids[len(ids)-1])
	return root, readJSON(t, filepath.Join(root, "verification.json"))
}

func setControlledOfficialDriver(t *testing.T, drivers, mode string) {
	t.Helper()
	writeExecutable(t, filepath.Join(drivers, "agent-sdk-testify-codex"), os.Executable, "conformance-official-driver", mode, os.Getenv("AGENT_SDK_TESTIFY_BIN"))
}

func recordControlledOfficial(t *testing.T, env []string, session, agent string) {
	t.Helper()
	mustRun(t, env, t.TempDir(), "", "official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
}

func TestOfficialVerifyInstalledChain(t *testing.T) {
	for _, mode := range []string{"local", "busy", "business-failed", "no-trigger", "early-exit", "wrong-ref", "reuse-ref", "cancel-before", "cancel-during", "startup-failed"} {
		t.Run(mode, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			recordControlledOfficial(t, env, session, agent)
			material := latestCapture(t, session)
			before := readAllFiles(t, str(material["path"]))
			current := readFile(t, filepath.Join(session, "current.json"))
			descriptor := readFile(t, filepath.Join(session, "session.json"))

			if mode == "reuse-ref" {
				mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
				priorRoot, _ := latestOfficialVerification(t, session)
				writeJSON(t, filepath.Join(session, "reuse-reference.json"), object{
					"peer-main.results.jsonl": filepath.Join(priorRoot, "driver", "peer-main.results.jsonl"),
					"peer-main.process.json":  filepath.Join(priorRoot, "driver", "peer-main.process.json"),
				})
			}
			setControlledOfficialDriver(t, drivers, mode)
			if mode == "startup-failed" {
				require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-codex"), []byte("#!/missing/interpreter\n"), 0o700))
			}
			args := []string{"official", "verify", "--session-dir", session}
			if strings.HasPrefix(mode, "cancel-") {
				args = append(args, "--timeout", "1s")
			}
			command := runTool(t, env, t.TempDir(), "", args...)
			root, evidence := latestOfficialVerification(t, session)
			if mode == "local" || mode == "busy" {
				require.Zero(t, command.code, command.stderr)
				require.Equal(t, "passed", evidence["status"])
				refs := array(evidence["connections"])
				require.Len(t, refs, 1)
				ref := obj(refs[0])
				execution := readJSON(t, filepath.Join(session, "executions", str(ref["executionId"]), "execution.json"))
				require.Equal(t, "mock", execution["command"])
				require.Equal(t, material["id"], obj(execution["artifact"])["id"])
				require.Equal(t, digestFile(t, filepath.Join(session, "executions", str(ref["executionId"]), "execution.json")), ref["executionSHA256"])
				assertFileHashes(t, root, obj(evidence["files"]))
				require.NotEmpty(t, ref["receiptHashes"])
				require.NotEmpty(t, ref["evidenceFiles"])
				require.NotEmpty(t, evidence["contracts"])
				mockRoute := obj(obj(readJSON(t, filepath.Join(session, "session.json"))["routes"])["mock"])
				host := obj(obj(evidence["report"])["host"])
				require.Equal(t, obj(mockRoute["env"])["HOME"], host["home"])
				require.Equal(t, obj(mockRoute["env"])["CODEX_HOME"], host["config"])
				require.Equal(t, mockRoute["workspace"], host["workspace"])

				require.Equal(t, "synthetic-official-driver", obj(evidence["officialSDK"])["version"])
				if mode == "busy" {
					require.EqualValues(t, 1, obj(evidence["report"])["finishBefore"])
					require.EqualValues(t, 1, obj(evidence["report"])["finishAfter"])
					for _, field := range []string{"verifyBefore", "recordBefore", "officialRecordBefore"} {
						require.EqualValues(t, 1, obj(evidence["report"])[field])
					}
					require.Len(t, array(sessionIndex(t, session)["executions"]), 2)
				}
			} else {
				require.Equal(t, 1, command.code, command.stderr)
				require.Equal(t, "failed", evidence["status"])
				require.NotEmpty(t, evidence["error"])
			}
			require.Equal(t, before, readAllFiles(t, str(material["path"])))
			require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
			require.Equal(t, descriptor, readFile(t, filepath.Join(session, "session.json")))
			require.NoFileExists(t, filepath.Join(session, "official-active.json"))
			for _, item := range array(sessionIndex(t, session)["executions"]) {
				require.NotEqual(t, "verify", obj(item)["command"])
			}
			if mode == "local" || mode == "busy" {
				finished, report := finishSession(t, env, session)
				require.Zero(t, finished.code, finished.stderr)
				require.Len(t, array(report["verifications"]), 1)
			}
		})
	}
}

func TestOfficialVerifyExportAndConnections(t *testing.T) {
	for _, caseID := range []string{"SDK-CASE-001", "SDK-CASE-032"} {
		t.Run(caseID, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			session := prepareSession(t, env, "--agent", "codex", "--case", caseID, "--model", "synthetic-model")
			recordControlledOfficial(t, env, session, agent)
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
			_, first := latestOfficialVerification(t, session)
			expected := 1
			if caseID == "SDK-CASE-032" {
				expected = 2
			}
			require.Len(t, array(first["connections"]), expected)
			exported := filepath.Join(t.TempDir(), "exported")
			mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", exported)
			imported := prepareSession(t, env, "--recording", exported)
			// Finishing also removes the old protocol environment, not just its descriptors.
			finished, _ := finishSession(t, env, session)
			require.Zero(t, finished.code, finished.stderr)
			// Neither default verification nor drivers can rely on the old session.
			require.NoError(t, os.RemoveAll(session))
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", imported)
			root, evidence := latestOfficialVerification(t, imported)
			require.Equal(t, "passed", evidence["status"])
			require.Len(t, array(evidence["connections"]), expected)
			assertFileHashes(t, root, obj(evidence["files"]))
			for _, item := range array(evidence["connections"]) {
				ref := obj(item)
				require.Equal(t, str(obj(evidence["material"])["id"]), str(obj(ref["material"])["id"]))
				require.Equal(t, obj(ref["process"])["pid"], obj(ref["finished"])["processId"])
			}
			corpus := newSyntheticCorpus(t)
			oldIndex := readFile(t, corpus.index)
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", imported, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
			require.Equal(t, 1, failed.code)
			rejectedRoot, verified := latestOfficialVerification(t, imported)
			require.Equal(t, "passed", verified["status"])
			require.NotContains(t, verified, "error")
			require.Equal(t, "rejected", readJSON(t, filepath.Join(rejectedRoot, "admission-summary.json"))["status"])
			require.Equal(t, oldIndex, readFile(t, corpus.index))
		})
	}
}

func TestOfficialVerifyDoesNotTransferEvidence(t *testing.T) {
	agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	setControlledOfficialDriver(t, drivers, "local")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	recordControlledOfficial(t, env, session, agent)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
	oldRoot, old := latestOfficialVerification(t, session)
	oldBytes := readAllFiles(t, oldRoot)
	oldMaterial := obj(old["material"])
	recordControlledOfficial(t, env, session, agent)
	material := latestCapture(t, session)
	require.NotEqual(t, oldMaterial["id"], material["id"])
	require.NoFileExists(t, filepath.Join(str(material["path"]), "verification.json"))
	require.Equal(t, oldBytes, readAllFiles(t, oldRoot))
	setControlledOfficialDriver(t, drivers, "no-trigger")
	failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
	require.Equal(t, 1, failed.code)
	_, current := latestOfficialVerification(t, session)
	require.Equal(t, "failed", current["status"])
	require.Equal(t, material["id"], obj(current["material"])["id"])
	require.Equal(t, oldBytes, readAllFiles(t, oldRoot))
}

// Legacy source evidence remains subject to the same qualification and atomic
// archive/index replacement path. No new origin fields are invented for it.
func TestOfficialVerifyLegacyAdmission(t *testing.T) {
	agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	installDriver(t, drivers, "codex", "verify")
	corpus := newSyntheticCorpus(t)
	before := readFile(t, corpus.index)
	lock := strings.TrimSuffix(corpus.index, filepath.Ext(corpus.index)) + ".lock"
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	args := []string{"official", "verify", "--session-dir", session, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive}
	failed := runTool(t, env, t.TempDir(), "", args...)
	require.Equal(t, 1, failed.code)
	failedRoot, evidence := latestOfficialVerification(t, session)
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, "rejected", readJSON(t, filepath.Join(failedRoot, "admission-summary.json"))["status"])
	require.Equal(t, before, readFile(t, corpus.index))
	failedBytes := readAllFiles(t, failedRoot)
	require.NoError(t, os.Remove(lock))
	mustRun(t, env, t.TempDir(), "", args...)
	adoptedRoot, _ := latestOfficialVerification(t, session)
	admitted := readJSON(t, filepath.Join(adoptedRoot, "admission-summary.json"))
	require.Equal(t, "admitted", admitted["status"])
	require.DirExists(t, str(admitted["materials"]))
	require.Equal(t, failedBytes, readAllFiles(t, failedRoot))
}

func TestOfficialVerifyPrerequisites(t *testing.T) {
	for _, fault := range []string{"no-current", "consumer", "no-driver"} {
		t.Run(fault, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			var session string
			if fault == "no-driver" {
				session = prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
				recordControlledOfficial(t, env, session, agent)
				require.NoError(t, os.Remove(filepath.Join(drivers, "agent-sdk-testify-codex")))
				env[0] = "PATH=/usr/bin:/bin"
			} else {
				session = prepareSession(t, env, "--plan", writeConsumerPlan(t, t.TempDir(), "consumer-verify"))
				if fault == "consumer" {
					recordConsumer(t, env, session, agent, conformanceRequest)
				}
			}
			executions := array(sessionIndex(t, session)["executions"])
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
			require.Equal(t, 1, failed.code)
			_, evidence := latestOfficialVerification(t, session)
			require.Equal(t, "failed", evidence["status"])
			require.NotContains(t, evidence, "driverProcess")
			require.Equal(t, executions, array(sessionIndex(t, session)["executions"]))
			require.NoFileExists(t, filepath.Join(session, "official-active.json"))
		})
	}
}

// The SDK substitute is synthetic; this proves adoption plumbing and evidence
// requirements, never a live Agent or official SDK business pass.
func TestOfficialVerifyNewCaptureAdmission(t *testing.T) {
	for _, fault := range []string{"complete", "missing-isolation", "changed-isolation", "missing-business", "business-failed", "wrong-record-reference", "changed-native-plan", "missing-parent-scan", "credential-leak", "missing-preparation-binding"} {
		t.Run(fault, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			if fault == "business-failed" {
				setControlledOfficialDriver(t, drivers, "business-failed")
			}
			if fault == "credential-leak" {
				env = append(env, "OPENAI_API_KEY=synthetic-selected-credential")
				setControlledOfficialDriver(t, drivers, "credential-leak")
			}
			session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			recorded := runTool(t, env, t.TempDir(), "", "official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			if fault == "business-failed" || fault == "credential-leak" {
				require.Equal(t, 1, recorded.code)
			} else {
				require.Zero(t, recorded.code, recorded.stderr)
			}
			material := latestCapture(t, session)
			materialRoot := str(material["path"])
			original := readAllFiles(t, materialRoot)
			sourceReport := readJSON(t, filepath.Join(materialRoot, "report.json"))
			require.Equal(t, "captured", sourceReport["status"])
			require.NoFileExists(t, filepath.Join(materialRoot, "summary.json"))
			require.NoFileExists(t, filepath.Join(materialRoot, "result.json"))
			execution := latestExecution(t, session, "record")
			executionRoot := filepath.Join(session, "executions", str(execution["executionId"]))
			isolation := filepath.Join(executionRoot, "receipts", "isolation.json")
			origin := obj(material["officialOrigin"])
			recordRoot := filepath.Join(session, "official-records", str(origin["runId"]))
			switch fault {
			case "missing-isolation":
				require.NoError(t, os.Remove(isolation))
			case "changed-isolation":
				receipt := readJSON(t, isolation)
				obj(receipt["environment"])["HOME"] = "/external-personal-home"
				writeJSON(t, isolation, receipt)
			case "missing-preparation-binding":
				result := readJSON(t, filepath.Join(recordRoot, "result.json"))
				delete(result, "recordPreparation")
				writeJSON(t, filepath.Join(recordRoot, "result.json"), result)
			case "missing-parent-scan":
				result := readJSON(t, filepath.Join(recordRoot, "result.json"))
				delete(result, "credentialScan")
				writeJSON(t, filepath.Join(recordRoot, "result.json"), result)
			case "missing-business":
				require.NoError(t, os.Remove(filepath.Join(recordRoot, "driver", "report.json")))
			case "wrong-record-reference":
				result := readJSON(t, filepath.Join(recordRoot, "result.json"))
				obj(array(result["connections"])[0])["executionId"] = "unrelated-capture"
				writeJSON(t, filepath.Join(recordRoot, "result.json"), result)
			case "changed-native-plan":
				runtimePlan := filepath.Join(filepath.Dir(str(execution["home"])), "record-plan.json")
				plan := readJSON(t, runtimePlan)
				plan["args"] = []string{"app-server", "--profile", "personal"}
				writeJSON(t, runtimePlan, plan)
			}
			setControlledOfficialDriver(t, drivers, "local")
			corpus := newSyntheticCorpus(t)
			oldIndex := readFile(t, corpus.index)
			args := []string{"official", "verify", "--session-dir", session, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive}
			result := runTool(t, env, t.TempDir(), "", args...)
			root, verification := latestOfficialVerification(t, session)
			require.Equal(t, "passed", verification["status"])
			require.NotContains(t, verification, "error")
			admission := readJSON(t, filepath.Join(root, "admission-summary.json"))
			if fault == "complete" {
				require.Zero(t, result.code, result.stderr)
				require.Equal(t, "admitted", admission["status"])
				require.NotEmpty(t, verification["officialRecord"])
				assertFileHashes(t, root, obj(verification["files"]))
				adopted := str(admission["materials"])
				require.Equal(t, readFile(t, filepath.Join(materialRoot, "report.json")), readFile(t, filepath.Join(adopted, "report.json")))
				require.Equal(t, readFile(t, filepath.Join(materialRoot, "source-plan.json")), readFile(t, filepath.Join(adopted, "source-plan.json")))
				require.FileExists(t, filepath.Join(adopted, "official-replay", "driver", "record-origin", "isolation.json"))
				require.NotEqual(t, oldIndex, readFile(t, corpus.index))
				// Replacement revalidates the previous admitted view using only its
				// portable original record, isolation and replay evidence.
				mustRun(t, env, t.TempDir(), "", args...)
				retriedRoot, _ := latestOfficialVerification(t, session)
				require.Equal(t, "admitted", readJSON(t, filepath.Join(retriedRoot, "admission-summary.json"))["status"])
			} else {
				require.Equal(t, 1, result.code, result.stderr)
				require.Equal(t, "rejected", admission["status"])
				require.NotEmpty(t, admission["error"])
				require.Equal(t, oldIndex, readFile(t, corpus.index))
			}
			require.Equal(t, original, readAllFiles(t, materialRoot))
			require.Equal(t, material["id"], obj(verification["material"])["id"])
		})
	}
}

func TestOfficialVerifyRemoteAdmissionRequiresNativeEvidence(t *testing.T) {
	_, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	setControlledOfficialDriver(t, drivers, "local")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	remote := newControlledRemote(t)
	args := append([]string{"official", "record", "--session-dir", session}, remote.args("/success")...)
	mustRun(t, env, t.TempDir(), "", args...)
	corpus := newSyntheticCorpus(t)
	before := readFile(t, corpus.index)
	result := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	require.Equal(t, 1, result.code, result.stderr)
	root, verification := latestOfficialVerification(t, session)
	require.Equal(t, "passed", verification["status"])
	admission := readJSON(t, filepath.Join(root, "admission-summary.json"))
	require.Equal(t, "rejected", admission["status"])
	require.Contains(t, str(admission["error"]), "native preparation")
	require.Equal(t, before, readFile(t, corpus.index))
	remote.assertAlive(t)
}

// Migrated from the retired official engine: protocol receipts, process facts,
// unsupported/untriggered business and malformed driver output remain failures.
func TestOfficialDriverFailureMatrixRetainsEvidence(t *testing.T) {
	for _, mode := range []string{"business-failed", "crash", "bad-result", "false-pass", "wrong-hash", "wrong-provider", "not-consumed", "no-eof", "native-launched", "wrong-frames", "receipt-error", "unsupported", "untriggered"} {
		t.Run(mode, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			recordControlledOfficial(t, env, session, agent)
			material := latestCapture(t, session)
			original := readAllFiles(t, str(material["path"]))
			setControlledOfficialDriver(t, drivers, mode)
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
			require.Equal(t, 1, failed.code)
			failedRoot, evidence := latestOfficialVerification(t, session)
			require.Equal(t, "failed", evidence["status"])
			require.NotEmpty(t, evidence["error"])
			before := readAllFiles(t, failedRoot)
			setControlledOfficialDriver(t, drivers, "local")
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
			successRoot, success := latestOfficialVerification(t, session)
			require.NotEqual(t, failedRoot, successRoot)
			require.Equal(t, "passed", success["status"])
			require.Equal(t, before, readAllFiles(t, failedRoot))
			require.Equal(t, original, readAllFiles(t, str(material["path"])))
			finished, report := finishSession(t, env, session)
			require.Equal(t, 1, finished.code)
			require.Equal(t, "failed", report["status"])
			require.Equal(t, true, report["cleanupComplete"])
		})
	}
}
