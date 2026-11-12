package conformance

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeHistoryMockCompletesWithoutNativeSessionFinalization(t *testing.T) {
	for _, caseID := range []string{"SDK-CASE-016", "SDK-CASE-018", "SDK-CASE-019", "SDK-CASE-020", "SDK-CASE-029", "SDK-CASE-033", "SDK-CASE-034", "SDK-CASE-036"} {
		t.Run(caseID, func(t *testing.T) {
			_, _, env := isolatedCommands(t, t.TempDir(), "claude")
			fixtureDir := prepareFixture(t, env, "--agent", "claude", "--case", caseID)
			material := fixtureMaterialRoot(t, fixtureDir, obj(array(materialIndex(t, fixtureDir)["materials"])[0]))
			original := readAllFiles(t, material)
			input := builtinInputs(t, "claude", caseID)["main"]
			mustRun(t, env, t.TempDir(), input, "agent", "mock", "--fixture-dir", fixtureDir)
			execution := latestExecution(t, fixtureDir, "mock")
			source := obj(readJSON(t, filepath.Join(material, "manifest.json"))["savedFiles"])
			bindings := obj(execution["historyFiles"])
			require.Greater(t, len(bindings), len(source))
			for name, digest := range source {
				require.Equal(t, digest, bindings[name])
			}
			assertFileHashes(t, executionOverlayRoot(t, fixtureDir, execution), bindings)
			require.Equal(t, "passed", connectionStatus(t, fixtureDir, execution, "main"))
			require.Equal(t, original, readAllFiles(t, material))
			require.NoFileExists(t, filepath.Join(fixtureDir, "finish.json"))
			runtime := fixtureRuntimeRoot(fixtureDir)
			require.Equal(t, filepath.Join(resolvedPath(t, fixtureDir), "runtime"), resolvedPath(t, runtime))
			require.NoError(t, os.RemoveAll(fixtureDir))
			require.NoDirExists(t, runtime)
		})
	}
}

func TestMockRejectsHistoryDriftBeforePublishingActionSuccess(t *testing.T) {
	for _, target := range []string{"source", "derived"} {
		t.Run(target, func(t *testing.T) {
			_, _, env := isolatedCommands(t, t.TempDir(), "claude")
			material := syntheticReplayMaterial(t, "claude", `{"version":1,"provenance":"synthetic","frames":[{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"wire":{}}]}`)
			plan := readJSON(t, filepath.Join(material, "plan.json"))
			plan["project"] = "/synthetic/project"
			writeJSON(t, filepath.Join(material, "plan.json"), plan)
			const history = "config/projects/-synthetic-project/history.jsonl"
			require.NoError(t, os.MkdirAll(filepath.Join(material, "config", "projects", "-synthetic-project"), 0o700))
			content := []byte("{\"cwd\":\"/synthetic/project\",\"id\":\"keep\"}\n")
			require.NoError(t, os.WriteFile(filepath.Join(material, history), content, 0o600))
			manifest := readJSON(t, filepath.Join(material, "manifest.json"))
			manifest["planSHA256"] = digestFile(t, filepath.Join(material, "plan.json"))
			manifest["savedFiles"] = object{history: digestBytes(content)}
			writeJSON(t, filepath.Join(material, "manifest.json"), manifest)
			fixtureDir := prepareFixture(t, env, "--recording", material)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), "agent", "mock", "--fixture-dir", fixtureDir)
			cmd.Env = env
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err)
			t.Cleanup(func() { _ = stdin.Close() })
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			line, err := bufio.NewReader(stdout).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "{}\n", line)
			execution := latestExecution(t, fixtureDir, "mock")
			name := history
			if target == "derived" {
				name = ""
				for candidate := range obj(execution["historyFiles"]) {
					if candidate != history {
						name = candidate
					}
				}
				require.NotEmpty(t, name)
			}
			require.NoError(t, os.WriteFile(filepath.Join(executionOverlayRoot(t, fixtureDir, execution), name), []byte("changed\n"), 0o600))
			require.NoError(t, stdin.Close())
			require.Error(t, cmd.Wait())
			require.Contains(t, stderr.String(), "history hash mismatch")
			require.Equal(t, "failed", connectionStatus(t, fixtureDir, execution, "main"))
			connection := obj(array(execution["connections"])[0])
			receipt := readJSON(t, resolveFixtureReference(t, executionPackageRoot(t, fixtureDir, execution), str(connection["receipt"])))
			require.Equal(t, "failed", receipt["status"])
			require.Equal(t, true, receipt["fullConsumption"])
			require.Equal(t, true, receipt["clientEOF"])
			require.Equal(t, true, receipt["cleanupComplete"])
			require.Equal(t, "internal", receipt["failureClass"])
			require.Contains(t, receipt["error"], "history hash mismatch")
			process := readJSON(t, resolveFixtureReference(t, executionPackageRoot(t, fixtureDir, execution), str(connection["processReceipt"])))
			require.Equal(t, "failed", process["status"])
			require.EqualValues(t, 1, process["exitCode"])
			require.Equal(t, "internal", process["failureClass"])
			require.Contains(t, process["error"], "history hash mismatch")
			result := readJSON(t, filepath.Join(executionPackageRoot(t, fixtureDir, execution), "executions", str(execution["executionId"]), "receipts", "main.result.json"))
			require.Equal(t, "failed", result["status"])
			require.Equal(t, "internal", result["failureClass"])
			require.Contains(t, result["error"], "history hash mismatch")
			var indexed object
			for _, value := range array(fixtureIndex(t, fixtureDir)["executions"]) {
				if row := obj(value); str(row["id"]) == str(execution["executionId"]) {
					indexed = row
					break
				}
			}
			require.NotEmpty(t, indexed)
			require.Equal(t, "failed", indexed["status"])
			require.Equal(t, "failed", obj(array(indexed["connections"])[0])["status"])
			require.Equal(t, content, readFile(t, filepath.Join(material, history)))
		})
	}
}
