package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Each caller owns a separate result channel. Tests resolve the actual execution
// from that channel, never from the newest execution directory.
func runAgentResult(t *testing.T, env []string, fixtureDir, input, action string, extra ...string) (commandResult, object) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "result.jsonl")
	file, err := os.Create(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	args := []string{"agent", action, "--fixture-dir", fixtureDir, "--result-fd", "3"}
	args = append(args, extra...)
	result := runToolWithFiles(t, env, t.TempDir(), input, []*os.File{file}, args...)
	require.NoError(t, file.Close())
	lines := strings.Split(strings.TrimSpace(string(readFile(t, path))), "\n")
	require.Len(t, lines, 2, result.stderr)
	var started, completed object
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &started))
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &completed))
	require.Equal(t, "running", started["status"])
	require.Equal(t, started["executionId"], completed["executionId"])
	require.Equal(t, started["connectionId"], completed["connectionId"])
	resultPath := resolveFixtureReference(t, fixtureDir, str(completed["result"]))
	executionPath := resolveFixtureReference(t, fixtureDir, str(completed["execution"]))
	require.Equal(t, completed, readJSON(t, resultPath))
	require.Equal(t, digestFile(t, executionPath), completed["executionSHA256"])
	require.Equal(t, completed["executionId"], readJSON(t, executionPath)["executionId"])
	return result, completed
}

func TestAgentRecordSourcesDoNotLaunchDriver(t *testing.T) {
	for _, source := range []string{"case", "plan", "recording"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			agent, drivers, env := isolatedCommands(t, root, "codex")
			marker := filepath.Join(root, "driver-launched")
			require.NoError(t, os.WriteFile(filepath.Join(drivers, "agent-sdk-testify-codex"), []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\nexit 42\n"), 0o700))
			var fixtureDir string
			switch source {
			case "case":
				fixtureDir = prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
			case "plan":
				fixtureDir = prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "public-agent"))
			case "recording":
				producer := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
				material := artifactByID(t, producer, currentMaterialID(t, producer))
				fixtureDir = prepareFixture(t, env, "--recording", fixtureMaterialRoot(t, producer, material))
				require.NoError(t, os.RemoveAll(producer))
			}
			descriptor := readJSON(t, filepath.Join(fixtureDir, "fixture.json"))
			for _, action := range []string{"record", "mock"} {
				route := obj(obj(descriptor["routes"])[action])
				require.Equal(t, "external-sdk", route["executor"])
				require.Len(t, array(route["connections"]), 1)
			}
			initial := currentMaterialID(t, fixtureDir)
			var previousID, previousWorkspace string
			for range 2 {
				result, reference := runAgentResult(t, env, fixtureDir, requestOne, "record", "--cmd", agent, "--", "app-server", "--listen", "stdio://")
				require.Zero(t, result.code, result.stderr)
				require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, result.stdout)
				require.Equal(t, "passed", reference["status"])
				execution := readJSON(t, resolveFixtureReference(t, fixtureDir, str(reference["execution"])))
				executionArgs := stringsOf(execution["args"])
				require.Equal(t, "--cmd", executionArgs[0])
				require.NotContains(t, executionArgs, "--fixture-dir")
				workspace := executionOverlayPath(t, fixtureDir, execution, "workspace")
				require.NotEqual(t, previousID, reference["executionId"])
				require.NotEqual(t, previousWorkspace, workspace)
				require.NoFileExists(t, filepath.Join(workspace, "previous-execution"))
				require.NoError(t, os.WriteFile(filepath.Join(workspace, "previous-execution"), []byte("isolated"), 0o600))
				previousID, previousWorkspace = str(reference["executionId"]), workspace
				material := obj(reference["material"])
				require.NotEqual(t, initial, material["id"])
				require.Equal(t, currentMaterialID(t, fixtureDir), material["id"])
				artifact := obj(execution["artifact"])
				for key, value := range material {
					require.Equal(t, artifact[key], value, key)
				}
				provenance := "external"
				if source == "plan" {
					provenance = "consumer"
				}
				require.Equal(t, provenance, material["provenance"])
				materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
				assertFileHashes(t, materialRoot, obj(artifact["files"]))
				require.NoFileExists(t, filepath.Join(materialRoot, "result.json"))
				sealed := readAllFiles(t, materialRoot)
				// A parent business report written after capture cannot enter the material.
				writeJSON(t, filepath.Join(root, "parent-report.json"), object{"status": "failed"})
				replay, mockReference := runAgentResult(t, env, fixtureDir, requestOne, "mock", "--", "app-server", "--listen", "stdio://")
				require.Zero(t, replay.code, replay.stderr)
				require.Equal(t, "passed", mockReference["status"])
				require.Equal(t, material, mockReference["material"])
				require.NotEqual(t, reference["executionId"], mockReference["executionId"])
				require.Equal(t, "passed", readJSON(t, resolveFixtureReference(t, fixtureDir, str(mockReference["receipt"])))["status"])
				require.Equal(t, sealed, readAllFiles(t, materialRoot))
			}
			require.NoFileExists(t, marker)
		})
	}
}

func TestAgentFailureResultsKeepCurrent(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "agent-failure"))
	recordConsumer(t, env, fixtureDir, agent, requestOne)
	current := readFile(t, filepath.Join(fixtureDir, "current.json"))
	failing := filepath.Join(root, "failing-agent")
	require.NoError(t, os.WriteFile(failing, []byte("#!/bin/sh\necho native-failure >&2\nexit 7\n"), 0o700))
	failed, reference := runAgentResult(t, env, fixtureDir, requestTwo, "record", "--cmd", failing, "--", "app-server", "--listen", "stdio://")
	require.Equal(t, 1, failed.code)
	require.Equal(t, "failed", reference["status"])
	require.NotContains(t, reference, "material")
	receiptPath := resolveFixtureReference(t, fixtureDir, str(reference["receipt"]))
	require.FileExists(t, receiptPath)
	require.Contains(t, string(readFile(t, filepath.Join(filepath.Dir(receiptPath), "stderr.raw"))), "native-failure")
	require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
	bad, mockReference := runAgentResult(t, env, fixtureDir, `{"id":1,"method":"wrong"}`+"\n", "mock")
	require.Equal(t, 1, bad.code)
	require.Equal(t, "failed", mockReference["status"])
	require.Equal(t, "failed", readJSON(t, resolveFixtureReference(t, fixtureDir, str(mockReference["receipt"])))["status"])
	require.Equal(t, "failed", readJSON(t, resolveFixtureReference(t, fixtureDir, str(mockReference["processReceipt"])))["status"])
	require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
}

func TestAgentCancellationPersistsOwnedExecution(t *testing.T) {
	for _, action := range []string{"record", "mock"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "agent-cancel"))
			recordConsumer(t, env, fixtureDir, agent, requestOne)
			current := readFile(t, filepath.Join(fixtureDir, "current.json"))
			r, w, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			args := []string{"agent", action, "--fixture-dir", fixtureDir, "--result-fd", "3"}
			if action == "record" {
				args = append(args, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			}
			cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
			cmd.Env = env
			cmd.ExtraFiles = []*os.File{w}
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err)
			defer stdin.Close()
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			require.NoError(t, w.Close())
			require.NoError(t, r.SetReadDeadline(time.Now().Add(10*time.Second)))
			reader := bufio.NewReader(r)
			line, err := reader.ReadBytes('\n')
			require.NoError(t, err)
			var started object
			require.NoError(t, json.Unmarshal(line, &started))
			require.Equal(t, "running", started["status"])
			_, err = stdin.Write([]byte(requestOne))
			require.NoError(t, err)
			_, err = bufio.NewReader(stdout).ReadBytes('\n')
			require.NoError(t, err)
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			require.Error(t, cmd.Wait(), stderr.String())
			require.Equal(t, 1, cmd.ProcessState.ExitCode(), stderr.String())
			line, err = reader.ReadBytes('\n')
			require.NoError(t, err)
			var completed object
			require.NoError(t, json.Unmarshal(line, &completed))
			require.Equal(t, started["executionId"], completed["executionId"])
			require.Equal(t, "failed", completed["status"])
			require.Equal(t, completed, readJSON(t, resolveFixtureReference(t, fixtureDir, str(completed["result"]))))
			require.FileExists(t, resolveFixtureReference(t, fixtureDir, str(completed["receipt"])))
			require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
		})
	}
}

func TestAgentResultBackpressureIsBounded(t *testing.T) {
	for _, action := range []string{"record", "mock"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "result-backpressure"))
			recordConsumer(t, env, fixtureDir, agent, requestOne)
			current := readFile(t, filepath.Join(fixtureDir, "current.json"))
			r, w, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()
			fd := int(w.Fd())
			require.NoError(t, syscall.SetNonblock(fd, true))
			for {
				_, err := syscall.Write(fd, bytes.Repeat([]byte("x"), 4096))
				if err != nil {
					require.ErrorIs(t, err, syscall.EAGAIN)
					break
				}
			}
			args := []string{"agent", action, "--fixture-dir", fixtureDir, "--result-fd", "3"}
			if action == "record" {
				args = append(args, "--timeout", "50ms", "--cmd", agent, "--", "app-server", "--listen", "stdio://")
			} else {
				args = append(args, "--timeout", "0.05")
			}
			started := time.Now()
			result := runToolWithFiles(t, env, root, "", []*os.File{w}, args...)
			require.Less(t, time.Since(started), 3*time.Second, "notification ignored timeout/backpressure")
			require.Equal(t, 1, result.code)
			require.Contains(t, result.stderr, "timeout")
			rows := array(fixtureIndex(t, fixtureDir)["executions"])
			require.Len(t, rows, 2)
			require.Equal(t, "failed", obj(rows[1])["status"])
			id := str(obj(rows[1])["id"])
			reference := readJSON(t, filepath.Join(fixtureDir, "executions", id, "receipts", "main.result.json"))
			require.Equal(t, id, reference["executionId"])
			require.Equal(t, "failed", reference["status"])
			require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
		})
	}
}
