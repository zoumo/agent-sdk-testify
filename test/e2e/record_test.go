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
			producer := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
			if source == "consumer" {
				producer = prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "recording-source"))
				recordConsumer(t, env, producer, agent, requestOne)
			}
			initial := artifactByID(t, producer, currentMaterialID(t, producer))
			fixtureDir := prepareFixture(t, env, "--recording", fixtureMaterialRoot(t, producer, initial))
			descriptorPath := filepath.Join(fixtureDir, "fixture.json")
			descriptorBytes := readFile(t, descriptorPath)
			descriptor := readJSON(t, descriptorPath)
			fixedSource := obj(descriptor["source"])
			fixedSourceRoot := resolveFixtureReference(t, fixtureDir, str(fixedSource["path"]))
			sourceBytes := readAllFiles(t, fixedSourceRoot)
			require.NoError(t, os.RemoveAll(producer))
			installDriver(t, drivers, "codex", "record")
			previous := currentMaterialID(t, fixtureDir)
			for _, request := range []string{requestOne, requestTwo} {
				mustRun(t, env, t.TempDir(), request, "agent", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
				execution := latestExecution(t, fixtureDir, "record")
				require.Equal(t, fixedSource, obj(execution["source"]))
				require.NotEqual(t, previous, currentMaterialID(t, fixtureDir))
				previous = currentMaterialID(t, fixtureDir)
				material := artifactByID(t, fixtureDir, previous)
				materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
				require.Equal(t, previous, material["id"])
				require.NotContains(t, material, "path")
				require.Equal(t, material, readJSON(t, filepath.Join(materialRoot, "material.json")))
				assertFileHashes(t, materialRoot, obj(material["files"]))
				workspace := executionOverlayPath(t, fixtureDir, execution, "workspace")
				resolvedWorkspace := resolvedPath(t, workspace)
				require.True(t, strings.HasPrefix(resolvedWorkspace, fixtureRuntimeRoot(resolvedPath(t, fixtureDir))+string(filepath.Separator)))
				require.True(t, strings.HasPrefix(resolvedWorkspace, resolvedPath(t, fixtureDir)+string(filepath.Separator)))
				require.Equal(t, sourceBytes, readAllFiles(t, fixedSourceRoot))
				require.Equal(t, descriptorBytes, readFile(t, descriptorPath))
			}
			require.Len(t, array(materialIndex(t, fixtureDir)["materials"]), 3)
			require.DirExists(t, fixtureRuntimeRoot(fixtureDir))
			require.FileExists(t, filepath.Join(fixedSourceRoot, "plan.json"))
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
			fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "publication-failure"))
			recordConsumer(t, env, fixtureDir, agent, requestOne)
			t.Cleanup(func() { _ = os.Chmod(fixtureDir, 0o700) })
			current := readFile(t, filepath.Join(fixtureDir, "current.json"))
			registry := readFile(t, filepath.Join(fixtureDir, "packages", "index.json"))
			old := latestCapture(t, fixtureDir)
			oldBytes := readAllFiles(t, fixtureMaterialRoot(t, fixtureDir, old))
			faultAgent := filepath.Join(root, "fault-agent")
			writeExecutable(t, faultAgent, os.Executable, "conformance-agent", "--conformance-fault", fault, fixtureDir)
			failed := runTool(t, env, t.TempDir(), requestTwo, "agent", "record", "--fixture-dir", fixtureDir, "--cmd", faultAgent, "--", "app-server", "--listen", "stdio://")
			require.NotZero(t, failed.code)
			require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
			if fault == "current" {
				require.NoError(t, os.Chmod(fixtureDir, 0o700))
				materials := array(materialIndex(t, fixtureDir)["materials"])
				require.Len(t, materials, 2)
				unpublished := obj(materials[1])
				require.NotEqual(t, unpublished["id"], currentMaterialID(t, fixtureDir))
				assertFileHashes(t, fixtureMaterialRoot(t, fixtureDir, unpublished), obj(unpublished["files"]))
			} else {
				require.Equal(t, registry, readFile(t, filepath.Join(fixtureDir, "packages", "index.json")))
			}
			require.Equal(t, oldBytes, readAllFiles(t, fixtureMaterialRoot(t, fixtureDir, old)))
			execution := latestExecution(t, fixtureDir, "record")
			artifactRoot := executionArtifactRoot(t, fixtureDir, execution)
			receipt := readJSON(t, filepath.Join(artifactRoot, "raw", "transport.json"))
			require.Equal(t, "complete", receipt["captureStatus"])
			require.Equal(t, true, receipt["cleanupComplete"])
			require.FileExists(t, filepath.Join(artifactRoot, "raw", "raw.jsonl"))
			rows := array(fixtureIndex(t, fixtureDir)["executions"])
			require.Len(t, rows, 2)
			require.Equal(t, "failed", obj(rows[1])["status"])
			failure := readJSON(t, filepath.Join(fixtureDir, "packages", str(obj(execution["artifact"])["id"]), "executions", str(execution["executionId"]), "receipts", "main.failure.json"))
			require.Equal(t, "failed", failure["status"])
			require.NotEmpty(t, failure["error"])
			if fault == "cleanup" {
				require.NoError(t, os.Remove(filepath.Join(fixtureRouteRoot(fixtureDir, "record"), "workspace")))
			}
			runPreparedMock(t, env, fixtureDir, "", requestOne)
		})
	}
}

func TestNewRecordDoesNotInheritVerification(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	old := latestCapture(t, fixtureDir)
	oldBytes := readAllFiles(t, fixtureMaterialRoot(t, fixtureDir, old))
	installDriver(t, drivers, "codex", "verify")
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
	evidenceRoot, verification := latestOfficialVerification(t, fixtureDir)
	evidence := readAllFiles(t, evidenceRoot)
	require.Equal(t, old["id"], obj(verification["material"])["id"])
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	material := latestCapture(t, fixtureDir)
	require.NotEqual(t, old["id"], material["id"])
	require.NotContains(t, material, "verification")
	require.NoFileExists(t, filepath.Join(fixtureMaterialRoot(t, fixtureDir, material), "verification.json"))
	require.Equal(t, oldBytes, readAllFiles(t, fixtureMaterialRoot(t, fixtureDir, old)))
	require.Equal(t, evidence, readAllFiles(t, evidenceRoot))
}

func TestRecordingRecordUsesProtocolPrerequisites(t *testing.T) {
	for _, fault := range []string{"missing-driver-binding", "source-history-drift"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			producer := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "record-prerequisites"))
			recordConsumer(t, env, producer, agent, requestOne)
			material := fixtureMaterialRoot(t, producer, latestCapture(t, producer))
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
			fixtureDir := prepareFixture(t, env, "--recording", material)
			if fault == "missing-driver-binding" {
				result, reference := runAgentResult(t, env, fixtureDir, requestTwo, "record", "--cmd", agent, "--", "app-server", "--listen", "stdio://")
				require.Zero(t, result.code, result.stderr)
				require.Equal(t, "passed", reference["status"])
				require.Equal(t, "external", obj(reference["material"])["provenance"])
				return
			}
			if fault == "source-history-drift" {
				source := resolveFixtureReference(t, fixtureDir, str(obj(readJSON(t, filepath.Join(fixtureDir, "fixture.json"))["source"])["path"]))
				require.NoError(t, os.WriteFile(filepath.Join(source, "config", "sessions", "conformance-history.jsonl"), []byte("changed\n"), 0o600))
			}
			index := readFile(t, filepath.Join(fixtureDir, "executions", "index.json"))
			current := readFile(t, filepath.Join(fixtureDir, "current.json"))
			result := runTool(t, env, t.TempDir(), requestTwo, "agent", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			require.NotZero(t, result.code)
			require.Equal(t, index, readFile(t, filepath.Join(fixtureDir, "executions", "index.json")))
			require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
			entries, err := os.ReadDir(filepath.Join(fixtureDir, "executions"))
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}
