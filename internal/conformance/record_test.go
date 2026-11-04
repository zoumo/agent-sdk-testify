package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordingSourceRemainsFixedAcrossRecords(t *testing.T) {
	for _, source := range []string{"consumer", "builtin"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			agent, drivers, env := isolatedCommands(t, root, "codex")
			producer := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
			if source == "consumer" {
				producer = prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "recording-source"))
				recordConsumer(t, env, producer, agent, requestOne)
			}
			initial := artifactByID(t, producer, currentMaterialID(t, producer))
			session := prepareSession(t, env, "--recording", str(initial["path"]))
			descriptorPath := filepath.Join(session, "session.json")
			descriptorBytes := readFile(t, descriptorPath)
			descriptor := readJSON(t, descriptorPath)
			fixedSource := obj(descriptor["source"])
			sourceBytes := readAllFiles(t, str(fixedSource["path"]))
			producerFinish, _ := finishSession(t, env, producer)
			require.Zero(t, producerFinish.code, producerFinish.stderr)
			require.NoError(t, os.RemoveAll(producer))
			installDriver(t, drivers, "codex", "record")
			previous := currentMaterialID(t, session)
			for _, request := range []string{requestOne, requestTwo} {
				mustRun(t, env, t.TempDir(), request, "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
				execution := latestExecution(t, session, "record")
				require.Equal(t, fixedSource, obj(execution["source"]))
				require.NotEqual(t, previous, currentMaterialID(t, session))
				previous = currentMaterialID(t, session)
				material := artifactByID(t, session, previous)
				require.Equal(t, filepath.Join(filepath.Dir(str(fixedSource["path"])), previous), material["path"])
				require.Equal(t, material, readJSON(t, filepath.Join(str(material["path"]), "material.json")))
				assertFileHashes(t, str(material["path"]), obj(material["files"]))
				require.True(t, strings.HasPrefix(str(execution["workspace"]), str(descriptor["resourceRoot"])+string(filepath.Separator)))
				require.False(t, strings.HasPrefix(str(execution["workspace"]), session+string(filepath.Separator)))
				require.Equal(t, sourceBytes, readAllFiles(t, str(fixedSource["path"])))
				require.Equal(t, descriptorBytes, readFile(t, descriptorPath))
			}
			require.Len(t, array(materialIndex(t, session)["materials"]), 3)
			finished, _ := finishSession(t, env, session)
			require.Zero(t, finished.code, finished.stderr)
			require.NoDirExists(t, str(descriptor["resourceRoot"]))
			require.FileExists(t, filepath.Join(str(fixedSource["path"]), "plan.json"))
		})
	}
}

func TestRecordCleanupAndPublicationFailuresKeepCurrent(t *testing.T) {
	for _, fault := range []string{"cleanup", "publication", "current"} {
		t.Run(fault, func(t *testing.T) {
			if fault == "current" && os.Geteuid() == 0 {
				t.Skip("current rename fault requires permission enforcement")
			}
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			session := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "publication-failure"))
			recordConsumer(t, env, session, agent, requestOne)
			t.Cleanup(func() { _ = os.Chmod(session, 0o700) })
			current := readFile(t, filepath.Join(session, "current.json"))
			registry := readFile(t, filepath.Join(session, "materials", "index.json"))
			old := latestCapture(t, session)
			oldBytes := readAllFiles(t, str(old["path"]))
			faultAgent := filepath.Join(root, "fault-agent")
			writeExecutable(t, faultAgent, os.Executable, "conformance-agent", "--conformance-fault", fault, session)
			failed := runTool(t, env, t.TempDir(), requestTwo, "record", "--session-dir", session, "--cmd", faultAgent, "--", "app-server", "--listen", "stdio://")
			require.NotZero(t, failed.code)
			require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
			if fault == "current" {
				require.NoError(t, os.Chmod(session, 0o700))
				materials := array(materialIndex(t, session)["materials"])
				require.Len(t, materials, 2)
				unpublished := obj(materials[1])
				require.NotEqual(t, unpublished["id"], currentMaterialID(t, session))
				assertFileHashes(t, str(unpublished["path"]), obj(unpublished["files"]))
			} else {
				require.Equal(t, registry, readFile(t, filepath.Join(session, "materials", "index.json")))
			}
			require.Equal(t, oldBytes, readAllFiles(t, str(old["path"])))
			execution := latestExecution(t, session, "record")
			receipt := readJSON(t, filepath.Join(str(execution["artifactRoot"]), "raw", "transport.json"))
			require.Equal(t, "complete", receipt["captureStatus"])
			require.Equal(t, true, receipt["cleanupComplete"])
			require.FileExists(t, filepath.Join(str(execution["artifactRoot"]), "raw", "raw.jsonl"))
			rows := array(sessionIndex(t, session)["executions"])
			require.Len(t, rows, 2)
			require.Equal(t, "failed", obj(rows[1])["status"])
			failure := readJSON(t, filepath.Join(str(execution["artifactRoot"]), "failure.json"))
			require.Equal(t, "failed", failure["status"])
			require.NotEmpty(t, failure["error"])
			if fault == "cleanup" {
				descriptor := readJSON(t, filepath.Join(session, "session.json"))
				require.NoError(t, os.Remove(filepath.Join(str(descriptor["resourceRoot"]), "routes", "record", "workspace")))
			}
			runPreparedMock(t, env, session, "", requestOne)
			finished, report := finishSession(t, env, session)
			require.NotZero(t, finished.code)
			require.Equal(t, true, report["cleanupComplete"])
		})
	}
}

func TestNewRecordDoesNotInheritVerification(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	old := latestCapture(t, session)
	oldBytes := readAllFiles(t, str(old["path"]))
	installDriver(t, drivers, "codex", "verify")
	mustRun(t, env, t.TempDir(), "", "verify", "--session-dir", session)
	verification := latestExecution(t, session, "verify")
	evidenceRoot := str(verification["artifactRoot"])
	evidence := readAllFiles(t, evidenceRoot)
	require.Equal(t, old["id"], obj(verification["artifact"])["id"])
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	material := latestCapture(t, session)
	require.NotEqual(t, old["id"], material["id"])
	require.NotContains(t, material, "verification")
	require.NoFileExists(t, filepath.Join(str(material["path"]), "verification.json"))
	require.Equal(t, oldBytes, readAllFiles(t, str(old["path"])))
	require.Equal(t, evidence, readAllFiles(t, evidenceRoot))
	finished, _ := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
}

func TestRecordingRecordUsesProtocolPrerequisites(t *testing.T) {
	for _, fault := range []string{"missing-driver-binding", "source-history-drift"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			producer := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "record-prerequisites"))
			recordConsumer(t, env, producer, agent, requestOne)
			material := str(latestCapture(t, producer)["path"])
			if fault == "missing-driver-binding" {
				planPath := filepath.Join(material, "plan.json")
				plan := readJSON(t, planPath)
				plan["driverProfile"] = "no-such-official-driver"
				writeJSON(t, planPath, plan)
				manifestPath := filepath.Join(material, "manifest.json")
				manifest := readJSON(t, manifestPath)
				manifest["provenance"] = "synthetic"
				manifest["planSHA256"] = digestFile(t, planPath)
				writeJSON(t, manifestPath, manifest)
				require.NoError(t, os.Remove(filepath.Join(material, "material.json")))
			}
			session := prepareSession(t, env, "--recording", material)
			if fault == "missing-driver-binding" {
				result, reference := runAgentResult(t, env, session, requestTwo, "record", "--cmd", agent, "--", "app-server", "--listen", "stdio://")
				require.Zero(t, result.code, result.stderr)
				require.Equal(t, "passed", reference["status"])
				require.Equal(t, "external", obj(reference["material"])["provenance"])
				return
			}
			if fault == "source-history-drift" {
				source := str(obj(readJSON(t, filepath.Join(session, "session.json"))["source"])["path"])
				require.NoError(t, os.WriteFile(filepath.Join(source, "config", "sessions", "conformance-history.jsonl"), []byte("changed\n"), 0o600))
			}
			index := readFile(t, filepath.Join(session, "executions", "index.json"))
			current := readFile(t, filepath.Join(session, "current.json"))
			result := runTool(t, env, t.TempDir(), requestTwo, "agent", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			require.NotZero(t, result.code)
			require.Equal(t, index, readFile(t, filepath.Join(session, "executions", "index.json")))
			require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
			entries, err := os.ReadDir(filepath.Join(session, "executions"))
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}
