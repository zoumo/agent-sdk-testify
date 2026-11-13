package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func latestOfficialVerification(t *testing.T, fixtureDir string) (string, object) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(fixtureDir, "packages", "*", "verify", "*", "verification.json"))
	require.NoError(t, err)
	paths = ownOfficialEvidence(t, fixtureDir, paths)
	require.NotEmpty(t, paths)
	var latest object
	var root string
	for _, path := range paths {
		evidence := readJSON(t, path)
		if root == "" || str(evidence["completedAt"]) > str(latest["completedAt"]) {
			root, latest = filepath.Dir(path), evidence
		}
	}
	return root, latest
}

func admissionSummary(t *testing.T, fixture, root string) object {
	t.Helper()
	return readJSON(t, filepath.Join(fixture, "runtime", "official", filepath.Base(root), "admission-summary.json"))
}

func setControlledOfficialDriver(t *testing.T, drivers, mode string) {
	t.Helper()
	writeExecutable(t, filepath.Join(drivers, "agent-sdk-testify-codex"), os.Executable, "conformance-official-driver", mode, os.Getenv("AGENT_SDK_TESTIFY_BIN"))
}

func recordControlledOfficial(t *testing.T, env []string, fixtureDir, agent string) {
	t.Helper()
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
}

func TestOfficialVerifyInstalledChain(t *testing.T) {
	for _, mode := range []string{"local", "busy", "business-failed", "no-trigger", "early-exit", "wrong-ref", "reuse-ref", "cancel-before", "cancel-during", "startup-failed", "replay-schema-missing", "process-passed-error"} {
		t.Run(mode, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			recordControlledOfficial(t, env, fixtureDir, agent)
			material := latestCapture(t, fixtureDir)
			materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
			before := readAllFiles(t, materialRoot)
			current := readFile(t, filepath.Join(fixtureDir, "current.json"))
			descriptor := readFile(t, filepath.Join(fixtureDir, "fixture.json"))

			if mode == "reuse-ref" {
				mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
				priorRoot, prior := latestOfficialVerification(t, fixtureDir)
				peer := readJSON(t, filepath.Join(priorRoot, "peers", "main.json"))
				ref := obj(array(prior["executions"])[0])
				executionRoot := filepath.Join(fixtureDir, "packages", str(material["id"]), "executions", str(ref["executionId"]))
				started, err := json.Marshal(peer["running"])
				require.NoError(t, err)
				finished, err := json.Marshal(readJSON(t, filepath.Join(executionRoot, "receipts", "main.result.json")))
				require.NoError(t, err)
				notices := filepath.Join(t.TempDir(), "notices.jsonl")
				require.NoError(t, os.WriteFile(notices, append(append(append(started, '\n'), finished...), '\n'), 0o600))
				delete(peer, "running")
				delete(peer, "result")
				process := filepath.Join(t.TempDir(), "process.json")
				writeJSON(t, process, peer)
				writeJSON(t, filepath.Join(fixtureDir, "reuse-reference.json"), object{
					"peer-main.results.jsonl": notices,
					"peer-main.process.json":  process,
				})
			}
			setControlledOfficialDriver(t, drivers, mode)
			if mode == "startup-failed" {
				require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-codex"), []byte("#!/missing/interpreter\n"), 0o700))
			}
			args := []string{"official", "verify", "--fixture-dir", fixtureDir}
			if mode == "cancel-before" {
				args = append(args, "--timeout", "1s")
			}
			var command commandResult
			if mode == "cancel-during" {
				command = cancelOfficialDuringProtocol(t, env, fixtureDir, args...)
			} else {
				command = runTool(t, env, t.TempDir(), "", args...)
			}
			if mode == "wrong-ref" {
				require.Equal(t, 1, command.code, command.stderr)
				journal := readJSON(t, filepath.Join(fixtureDir, "official.json"))
				require.Equal(t, material["id"], journal["materialId"])
				require.DirExists(t, filepath.Join(fixtureDir, filepath.FromSlash(str(journal["staging"]))))
				require.NoDirExists(t, filepath.Join(fixtureDir, filepath.FromSlash(str(journal["target"]))))
				require.Equal(t, before, readAllFiles(t, materialRoot))
				require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
				return
			}
			root, evidence := latestOfficialVerification(t, fixtureDir)
			if mode == "local" || mode == "busy" {
				require.Zero(t, command.code, command.stderr)
				require.Equal(t, "passed", evidence["status"])
				refs := array(evidence["executions"])
				require.Len(t, refs, 1)
				ref := obj(refs[0])
				packageRoot := filepath.Join(fixtureDir, "packages", str(material["id"]))
				execution := readJSON(t, filepath.Join(packageRoot, "executions", str(ref["executionId"]), "execution.json"))
				require.Equal(t, "mock", execution["command"])
				require.Equal(t, material["id"], obj(execution["artifact"])["id"])
				require.Equal(t, digestFile(t, filepath.Join(packageRoot, "executions", str(ref["executionId"]), "execution.json")), ref["executionSHA256"])
				assertFileHashes(t, packageRoot, obj(evidence["files"]))
				aggregate := readJSON(t, filepath.Join(packageRoot, str(ref["path"])))
				assertFileHashes(t, packageRoot, obj(aggregate["files"]))
				require.Equal(t, aggregate["connections"], ref["connections"])
				executionRoot := filepath.Join(packageRoot, "executions", str(ref["executionId"]))
				for _, name := range []string{"prepared-plan.json", "isolation.json"} {
					reference := "executions/" + str(ref["executionId"]) + "/" + name
					require.Equal(t, digestFile(t, filepath.Join(executionRoot, name)), obj(aggregate["files"])[reference])
				}
				isolation := readJSON(t, filepath.Join(executionRoot, "isolation.json"))
				require.Equal(t, "executions/"+str(ref["executionId"])+"/execution.json", obj(isolation["execution"])["path"])
				require.Equal(t, ref["executionSHA256"], obj(isolation["execution"])["sha256"])
				require.Equal(t, "executions/"+str(ref["executionId"])+"/prepared-plan.json", obj(isolation["preparedPlan"])["path"])
				require.Equal(t, digestFile(t, filepath.Join(executionRoot, "prepared-plan.json")), obj(isolation["preparedPlan"])["sha256"])
				require.Equal(t, ref["executionId"], isolation["executionId"])
				require.Equal(t, material["id"], isolation["materialId"])
				require.NoFileExists(t, filepath.Join(root, "mock-isolation.json"))
				require.NotContains(t, obj(evidence["files"]), "verify/"+filepath.Base(root)+"/mock-isolation.json")
				require.NotEmpty(t, evidence["contracts"])
				files := obj(evidence["files"])
				for _, name := range []string{"replay-report.json", "result.json", "driver-result.json", "peers/main.json", "stderr.log", "stdout.log"} {
					require.Contains(t, files, "verify/"+filepath.Base(root)+"/"+name)
				}
				resolvedFixture := resolvedPath(t, fixtureDir)
				mockRoute := fixtureRouteRoot(resolvedFixture, "mock")
				report := readJSON(t, filepath.Join(root, "replay-report.json"))
				host := obj(report["host"])
				require.Equal(t, filepath.Join(mockRoute, "home"), host["home"])
				require.Equal(t, filepath.Join(mockRoute, "config"), host["config"])
				require.Equal(t, filepath.Join(mockRoute, "workspace"), host["workspace"])

				require.Equal(t, "synthetic-official-driver", obj(report["officialSDK"])["version"])
				if mode == "busy" {
					for _, field := range []string{"verifyBefore", "recordBefore", "officialRecordBefore"} {
						require.EqualValues(t, 1, report[field])
					}
					require.Len(t, array(fixtureIndex(t, fixtureDir)["executions"]), 2)
				}
			} else {
				require.Equal(t, 1, command.code, command.stderr)
				require.Equal(t, "failed", evidence["status"], command.stderr)
				if mode == "reuse-ref" {
					require.Contains(t, command.stderr, "mock does not belong to this verification")
				}
				require.NotEmpty(t, evidence["error"])
			}
			require.Equal(t, before, readAllFiles(t, materialRoot))
			require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
			require.Equal(t, descriptor, readFile(t, filepath.Join(fixtureDir, "fixture.json")))
			require.NoFileExists(t, filepath.Join(fixtureDir, "official.json"))
			for _, item := range array(fixtureIndex(t, fixtureDir)["executions"]) {
				require.NotEqual(t, "verify", obj(item)["command"])
			}
		})
	}
}

func TestOfficialVerifyExportAndConnections(t *testing.T) {
	for _, caseID := range []string{"SDK-CASE-001", "SDK-CASE-032"} {
		t.Run(caseID, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", caseID, "--model", "synthetic-model")
			recordControlledOfficial(t, env, fixtureDir, agent)
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
			_, first := latestOfficialVerification(t, fixtureDir)
			expected := 1
			if caseID == "SDK-CASE-032" {
				expected = 2
			}
			require.Len(t, array(obj(array(first["executions"])[0])["connections"]), expected)
			exported := filepath.Join(t.TempDir(), "exported")
			mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", exported)
			imported := prepareFixture(t, env, "--recording", exported)
			// Removing the fixture also removes its protocol environment.
			// Neither default verification nor drivers can rely on the old fixture.
			require.NoError(t, os.RemoveAll(fixtureDir))
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", imported)
			root, evidence := latestOfficialVerification(t, imported)
			require.Equal(t, "passed", evidence["status"])
			ref := obj(array(evidence["executions"])[0])
			require.Len(t, array(ref["connections"]), expected)
			packageRoot := filepath.Join(imported, "packages", str(evidence["materialId"]))
			assertFileHashes(t, packageRoot, obj(evidence["files"]))
			for _, item := range array(ref["connections"]) {
				connectionID := str(obj(item)["id"])
				peer := readJSON(t, filepath.Join(root, "peers", connectionID+".json"))
				durable := readJSON(t, filepath.Join(packageRoot, str(obj(peer["result"])["path"])))
				require.Equal(t, evidence["materialId"], durable["materialId"])
				require.Equal(t, peer["pid"], durable["processId"])
			}
			corpus := newSyntheticCorpus(t)
			oldIndex := readFile(t, corpus.index)
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", imported, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
			require.Equal(t, 1, failed.code)
			rejectedRoot, verified := latestOfficialVerification(t, imported)
			require.Equal(t, "passed", verified["status"])
			require.NotContains(t, verified, "error")
			require.Equal(t, "rejected", admissionSummary(t, imported, rejectedRoot)["status"])
			require.Equal(t, oldIndex, readFile(t, corpus.index))
		})
	}
}

func TestOfficialVerifyDoesNotTransferEvidence(t *testing.T) {
	agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	setControlledOfficialDriver(t, drivers, "local")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	recordControlledOfficial(t, env, fixtureDir, agent)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
	oldRoot, old := latestOfficialVerification(t, fixtureDir)
	oldBytes := readAllFiles(t, oldRoot)
	oldMaterial := old["materialId"]
	recordControlledOfficial(t, env, fixtureDir, agent)
	material := latestCapture(t, fixtureDir)
	require.NotEqual(t, oldMaterial, material["id"])
	require.NoFileExists(t, filepath.Join(fixtureMaterialRoot(t, fixtureDir, material), "verification.json"))
	require.Equal(t, oldBytes, readAllFiles(t, oldRoot))
	setControlledOfficialDriver(t, drivers, "no-trigger")
	failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
	require.Equal(t, 1, failed.code)
	_, current := latestOfficialVerification(t, fixtureDir)
	require.Equal(t, "failed", current["status"])
	require.Equal(t, material["id"], current["materialId"])
	require.Equal(t, oldBytes, readAllFiles(t, oldRoot))
}

// Legacy source evidence remains subject to the same qualification and atomic
// archive/index replacement path. No new origin fields are invented for it.
func TestOfficialVerifyLegacyAdmission(t *testing.T) {
	agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	installDriver(t, drivers, "codex", "verify")
	corpus := newSyntheticCorpus(t)
	before := readFile(t, corpus.index)
	lock := strings.TrimSuffix(corpus.index, filepath.Ext(corpus.index)) + ".lock"
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	args := []string{"official", "verify", "--fixture-dir", fixtureDir, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive}
	failed := runTool(t, env, t.TempDir(), "", args...)
	require.Equal(t, 1, failed.code)
	failedRoot, evidence := latestOfficialVerification(t, fixtureDir)
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, "rejected", admissionSummary(t, fixtureDir, failedRoot)["status"])
	require.Equal(t, before, readFile(t, corpus.index))
	failedBytes := readAllFiles(t, failedRoot)
	require.NoError(t, os.Remove(lock))
	mustRun(t, env, t.TempDir(), "", args...)
	adoptedRoot, _ := latestOfficialVerification(t, fixtureDir)
	admitted := admissionSummary(t, fixtureDir, adoptedRoot)
	require.Equal(t, "admitted", admitted["status"])
	require.DirExists(t, str(admitted["materials"]))
	require.Equal(t, failedBytes, readAllFiles(t, failedRoot))
}

func TestOfficialVerifyPrerequisites(t *testing.T) {
	for _, fault := range []string{"no-current", "consumer", "no-driver"} {
		t.Run(fault, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			var fixtureDir string
			if fault == "no-driver" {
				fixtureDir = prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
				recordControlledOfficial(t, env, fixtureDir, agent)
				require.NoError(t, os.Remove(filepath.Join(drivers, "agent-sdk-testify-codex")))
				env[0] = "PATH=/usr/bin:/bin"
			} else {
				fixtureDir = prepareFixture(t, env, "--plan", writeConsumerPlan(t, t.TempDir(), "consumer-verify"))
				if fault == "consumer" {
					recordConsumer(t, env, fixtureDir, agent, conformanceRequest)
				}
			}
			before := readAllFiles(t, fixtureDir)
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
			require.Equal(t, 1, failed.code)
			require.NotEmpty(t, failed.stderr)
			require.Equal(t, before, readAllFiles(t, fixtureDir))
			require.NoFileExists(t, filepath.Join(fixtureDir, "official-active.json"))
			require.NoFileExists(t, filepath.Join(fixtureDir, "verifications", "index.json"))
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
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			recorded := runTool(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			if fault == "business-failed" || fault == "credential-leak" {
				require.Equal(t, 1, recorded.code)
			} else {
				require.Zero(t, recorded.code, recorded.stderr)
			}
			material := latestCapture(t, fixtureDir)
			materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
			original := readAllFiles(t, materialRoot)
			sourceReport := readJSON(t, filepath.Join(materialRoot, "report.json"))
			require.Equal(t, "captured", sourceReport["status"])
			require.NoFileExists(t, filepath.Join(materialRoot, "summary.json"))
			require.NoFileExists(t, filepath.Join(materialRoot, "result.json"))
			execution := latestExecution(t, fixtureDir, "record")
			packageRoot := filepath.Dir(materialRoot)
			executionRoot := filepath.Join(packageRoot, "executions", str(execution["executionId"]))
			isolation := filepath.Join(executionRoot, "isolation.json")
			origin := readJSON(t, filepath.Join(materialRoot, "official-origin.json"))
			recordRoot := filepath.Join(packageRoot, "record", str(origin["runId"]))
			switch fault {
			case "missing-isolation":
				require.NoError(t, os.Remove(isolation))
			case "changed-isolation":
				receipt := readJSON(t, isolation)
				obj(receipt["environment"])["HOME"] = "/external-personal-home"
				writeJSON(t, isolation, receipt)
			case "missing-preparation-binding":
				receipt := readJSON(t, isolation)
				delete(receipt, "preparedPlan")
				writeJSON(t, isolation, receipt)
			case "missing-parent-scan":
				result := readJSON(t, filepath.Join(recordRoot, "result.json"))
				delete(result, "credentialScan")
				writeJSON(t, filepath.Join(recordRoot, "result.json"), result)
			case "missing-business":
				require.NoError(t, os.Remove(filepath.Join(recordRoot, "report.json")))
			case "wrong-record-reference":
				result := readJSON(t, filepath.Join(recordRoot, "result.json"))
				obj(array(result["executions"])[0])["executionId"] = "unrelated-capture"
				writeJSON(t, filepath.Join(recordRoot, "result.json"), result)
			case "changed-native-plan":
				runtimePlan := filepath.Join(executionRoot, "prepared-plan.json")
				plan := readJSON(t, runtimePlan)
				plan["args"] = []string{"app-server", "--profile", "personal"}
				writeJSON(t, runtimePlan, plan)
			}
			setControlledOfficialDriver(t, drivers, "local")
			corpus := newSyntheticCorpus(t)
			oldIndex := readFile(t, corpus.index)
			args := []string{"official", "verify", "--fixture-dir", fixtureDir, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive}
			result := runTool(t, env, t.TempDir(), "", args...)
			root, verification := latestOfficialVerification(t, fixtureDir)
			require.Equal(t, "passed", verification["status"])
			require.NotContains(t, verification, "error")
			admission := admissionSummary(t, fixtureDir, root)
			if fault == "complete" {
				require.Zero(t, result.code, result.stderr)
				require.Equal(t, "admitted", admission["status"])
				assertFileHashes(t, packageRoot, obj(verification["files"]))
				adopted := str(admission["materials"])
				require.Equal(t, readFile(t, filepath.Join(materialRoot, "report.json")), readFile(t, filepath.Join(adopted, "report.json")))
				require.Equal(t, readFile(t, filepath.Join(materialRoot, "source-plan.json")), readFile(t, filepath.Join(adopted, "source-plan.json")))
				require.NoDirExists(t, filepath.Join(adopted, "executions"))
				require.FileExists(t, filepath.Join(corpus.archive, filepath.Base(adopted), "executions", str(execution["executionId"]), "isolation.json"))
				require.NotEqual(t, oldIndex, readFile(t, corpus.index))
				// Replacement checks the previous publication's bound replay core;
				// the new candidate still needs complete official qualification.
				mustRun(t, env, t.TempDir(), "", args...)
				retriedRoot, _ := latestOfficialVerification(t, fixtureDir)
				require.Equal(t, "admitted", admissionSummary(t, fixtureDir, retriedRoot)["status"])
			} else {
				require.Equal(t, 1, result.code, result.stderr)
				require.Equal(t, "rejected", admission["status"])
				require.NotEmpty(t, admission["error"])
				require.Equal(t, oldIndex, readFile(t, corpus.index))
			}
			require.Equal(t, original, readAllFiles(t, materialRoot))
			require.Equal(t, material["id"], verification["materialId"])
		})
	}
}

func TestOfficialVerifyRemoteAdmissionRequiresNativeEvidence(t *testing.T) {
	_, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	setControlledOfficialDriver(t, drivers, "local")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	remote := newControlledRemote(t)
	args := append([]string{"official", "record", "--fixture-dir", fixtureDir}, remote.args("/success")...)
	mustRun(t, env, t.TempDir(), "", args...)
	corpus := newSyntheticCorpus(t)
	before := readFile(t, corpus.index)
	result := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	require.Equal(t, 1, result.code, result.stderr)
	root, verification := latestOfficialVerification(t, fixtureDir)
	require.Equal(t, "passed", verification["status"])
	admission := admissionSummary(t, fixtureDir, root)
	require.Equal(t, "rejected", admission["status"])
	require.Contains(t, str(admission["error"]), "native preparation")
	require.Equal(t, before, readFile(t, corpus.index))
	remote.assertAlive(t)
}

// Migrated from the retired official engine: protocol receipts, process facts,
// unsupported/untriggered business and malformed driver output remain failures.
func TestOfficialDriverFailureMatrixRetainsEvidence(t *testing.T) {
	for _, mode := range []string{"crash", "bad-result", "wrong-hash"} {
		t.Run(mode, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			setControlledOfficialDriver(t, drivers, "local")
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			recordControlledOfficial(t, env, fixtureDir, agent)
			material := latestCapture(t, fixtureDir)
			original := readAllFiles(t, fixtureMaterialRoot(t, fixtureDir, material))
			setControlledOfficialDriver(t, drivers, mode)
			failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
			require.Equal(t, 1, failed.code)
			failedRoot, evidence := latestOfficialVerification(t, fixtureDir)
			require.Equal(t, "failed", evidence["status"])
			require.NotEmpty(t, evidence["error"])
			before := readAllFiles(t, failedRoot)
			setControlledOfficialDriver(t, drivers, "local")
			mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
			successRoot, success := latestOfficialVerification(t, fixtureDir)
			require.NotEqual(t, failedRoot, successRoot)
			require.Equal(t, "passed", success["status"])
			require.Equal(t, before, readAllFiles(t, failedRoot))
			require.Equal(t, original, readAllFiles(t, fixtureMaterialRoot(t, fixtureDir, material)))
		})
	}
}
