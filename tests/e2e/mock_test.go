package conformance

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMockUnverifiedConsumerCurrent(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	producer := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "mock-unverified"))
	recordConsumer(t, env, producer, agent, requestOne)
	original := fixtureMaterialRoot(t, producer, latestCapture(t, producer))
	fixtureDir := prepareFixture(t, env, "--recording", original)
	require.NoError(t, os.RemoveAll(producer))
	material := obj(array(materialIndex(t, fixtureDir)["materials"])[0])
	require.Equal(t, "consumer", material["provenance"])
	materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
	require.NoFileExists(t, filepath.Join(materialRoot, "verification.json"))
	before := readAllFiles(t, materialRoot)
	current := readFile(t, filepath.Join(fixtureDir, "current.json"))
	descriptor := readFile(t, filepath.Join(fixtureDir, "fixture.json"))
	// The material has no official verifier; this must not become a mock gate.
	rejected := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
	require.Equal(t, 1, rejected.code, rejected.stderr)
	require.Contains(t, rejected.stderr, "no applicable official verifier")
	require.Empty(t, array(fixtureIndex(t, fixtureDir)["executions"]))
	var previousWorkspace, previousReceipt string
	var previousReceiptBytes []byte
	for range 2 {
		result := runPreparedMock(t, env, fixtureDir, "", requestOne)
		require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, result.stdout)
		execution := latestExecution(t, fixtureDir, "mock")
		require.Equal(t, material["id"], obj(execution["artifact"])["id"])
		workspace := executionOverlayPath(t, fixtureDir, execution, "workspace")
		require.NotEqual(t, previousWorkspace, workspace)
		require.NoFileExists(t, filepath.Join(workspace, "previous-execution"))
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "previous-execution"), []byte("isolated"), 0o600))
		previousWorkspace = workspace
		connection := obj(array(execution["connections"])[0])
		receiptPath := resolveFixtureReference(t, executionPackageRoot(t, fixtureDir, execution), str(connection["receipt"]))
		receipt := readJSON(t, receiptPath)
		require.Equal(t, "passed", receipt["status"])
		require.EqualValues(t, 1, receipt["version"])
		require.Equal(t, "Go native mock", receipt["peer"])
		require.Equal(t, "codex", receipt["provider"])
		require.Equal(t, digestFile(t, filepath.Join(materialRoot, "raw", "raw.jsonl")), receipt["recordingSHA256"])
		require.EqualValues(t, 2, receipt["frames"])
		info, err := os.Stat(receiptPath)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		if previousReceipt != "" {
			require.NotEqual(t, previousReceipt, receiptPath)
			require.Equal(t, previousReceiptBytes, readFile(t, previousReceipt))
		}
		previousReceipt = receiptPath
		previousReceiptBytes = readFile(t, previousReceipt)
		require.Equal(t, true, receipt["fullConsumption"])
		require.Equal(t, true, receipt["clientEOF"])
		require.Equal(t, true, receipt["cleanupComplete"])
		require.Equal(t, false, receipt["nativeLaunched"])
		process := readJSON(t, resolveFixtureReference(t, executionPackageRoot(t, fixtureDir, execution), str(connection["processReceipt"])))
		require.Equal(t, "passed", process["status"])
		require.Zero(t, process["exitCode"])
	}
	require.Equal(t, before, readAllFiles(t, materialRoot))
	require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
	require.Equal(t, descriptor, readFile(t, filepath.Join(fixtureDir, "fixture.json")))
}

func TestMockRejectsRemovedInputsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	before := readAllFiles(t, fixtureDir)
	for _, args := range [][]string{
		{"--case", "SDK-CASE-001"},
		{"--recording", "/absent/material"},
		{"--plan", "/absent/plan"},
		{"--agent", "codex"},
		{"--report", filepath.Join(root, "report.json")},
		{"--list"},
		{"--describe"},
		{"--json"},
		{"--version"},
		{"-v"},
		{"version"},
		{"help"},
		{"--", "--version"},
		{"--", "--help"},
		{"--prepare"},
		{"--run", "/absent/run"},
		{"--finish", "/absent/run"},
		{"--provider", "codex"},
		{"--connection", "undeclared"},
		{"--timeout", "-1"},
		{"--progress-fd", "1"},
		{"--result-fd", "0"},
		{"--result-fd", "1"},
		{"--result-fd", "3", "--progress-fd", "3"},
		{"app-server", "--listen", "http://localhost:1"},
		{"--sdk-test-handshakes", "answer"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			argv := append([]string{"agent", "mock", "--fixture-dir", fixtureDir}, args...)
			result := runTool(t, env, t.TempDir(), requestOne, argv...)
			require.Equal(t, 1, result.code, result.stderr)
			require.Empty(t, result.stdout)
			require.True(t, bytes.Equal(before, readAllFiles(t, fixtureDir)), "rejected input changed fixture files")
			require.NoFileExists(t, filepath.Join(root, "report.json"))
		})
	}
	result := runTool(t, env, t.TempDir(), "", "agent", "mock")
	require.Equal(t, 1, result.code, result.stderr)
	require.Contains(t, result.stderr, "mock requires --fixture-dir")
}

func TestMockLauncherRejectsMetaCommandsAndOverrides(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	launcher := fixtureLauncher(fixtureDir, "main")
	before := readAllFiles(t, fixtureDir)
	for _, args := range [][]string{{"--version"}, {"-v"}, {"--help"}, {"help"}, {"--fixture-dir", "/absent/fixture"}, {"--connection", "probe"}, {"--case", "SDK-CASE-001"}, {"app-server", "--listen", "http://localhost:1"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), launcher, args...)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			require.Error(t, cmd.Run())
			require.Equal(t, 1, cmd.ProcessState.ExitCode(), stderr.String())
			require.Empty(t, stdout.String())
			require.True(t, bytes.Equal(before, readAllFiles(t, fixtureDir)), "launcher rejected input changed fixture files")
		})
	}
	inputs := builtinInputs(t, "codex", "SDK-CASE-001")
	cmd := exec.CommandContext(t.Context(), launcher, "app-server", "--listen", "stdio://")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(inputs["main"])
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	execution := latestExecution(t, fixtureDir, "mock")
	require.Equal(t, []string{"app-server", "--listen", "stdio://"}, stringsOf(execution["nativeArgs"]))
}

// syntheticReplayMaterial builds private synthetic input for the public CLI.
// It never changes the catalog, builtin snapshot or original recording bytes.
func syntheticReplayMaterial(t *testing.T, provider, raw string) string {
	t.Helper()
	root := t.TempDir()
	planPath := writeConsumerPlan(t, root, "synthetic-replay")
	plan := readJSON(t, planPath)
	plan["agentType"] = provider
	writeJSON(t, filepath.Join(root, "plan.json"), plan)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte(raw), 0o600))
	writeJSON(t, filepath.Join(root, "manifest.json"), object{
		"provenance": "consumer", "planSHA256": digestFile(t, filepath.Join(root, "plan.json")),
		"files": object{"raw.jsonl": digestBytes([]byte(raw))},
	})
	return root
}

func TestMockTimeoutPolicyAndSourceBytes(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, tc := range []struct {
			name, wire         string
			timeoutMS, delayMS int
			args               []string
			code               int
		}{
			{name: "scenario timeout", wire: "{}", timeoutMS: 40, delayMS: 1000, code: 1},
			{name: "explicit overrides scenario", wire: "{}", timeoutMS: 1, delayMS: 30, args: []string{"--timeout", "1"}},
			// Opaque wire variants and both providers' timeout selection are
			// tested in process. Keep one actual CLI source-byte path.
			{name: "source native version", wire: `{ "claude_code_version":"2.1.1" }`},
		} {
			if (provider == "claude") != (tc.name == "source native version") {
				continue
			}
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				_, _, env := isolatedCommands(t, root, provider)
				raw := fmt.Sprintf(`{"version":1,"provenance":"synthetic","timeoutMs":%d,"frames":[{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","delayMs":%d,"wire":%s}]}`, tc.timeoutMS, tc.delayMS, tc.wire)
				material := syntheticReplayMaterial(t, provider, raw)
				fixtureDir := prepareFixture(t, env, "--recording", material)
				args := append([]string{"agent", "mock", "--fixture-dir", fixtureDir}, tc.args...)
				result := runTool(t, env, root, "", args...)
				require.Equal(t, tc.code, result.code, result.stderr)
				execution := latestExecution(t, fixtureDir, "mock")
				receipt := readJSON(t, resolveFixtureReference(t, executionPackageRoot(t, fixtureDir, execution), str(obj(array(execution["connections"])[0])["receipt"])))
				require.Equal(t, digestBytes([]byte(raw)), receipt["recordingSHA256"])
				if tc.code == 0 {
					require.Equal(t, tc.wire+"\n", result.stdout)
					require.Equal(t, "passed", receipt["status"])
					require.Equal(t, true, receipt["clientEOF"])
				} else {
					require.Empty(t, result.stdout)
					require.Contains(t, result.stderr, "deadline exceeded")
					require.Equal(t, "failed", receipt["status"])
					require.Equal(t, false, receipt["clientEOF"])
					require.Equal(t, "timeout", receipt["failureClass"])
				}
			})
		}
	}
}

func TestMockProgressReceiptWaitsForEOF(t *testing.T) {
	for _, ending := range []string{"EOF", "extra", "timeout"} {
		t.Run(ending, func(t *testing.T) {
			root := t.TempDir()
			_, _, env := isolatedCommands(t, root, "codex")
			raw := `{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","wire":{}}` + "\n"
			fixtureDir := prepareFixture(t, env, "--recording", syntheticReplayMaterial(t, "codex", raw))
			progress, notification, err := os.Pipe()
			require.NoError(t, err)
			defer progress.Close()
			defer notification.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), "agent", "mock", "--fixture-dir", fixtureDir, "--timeout", "0.4", "--progress-fd", "3")
			cmd.Env, cmd.Dir = env, root
			cmd.ExtraFiles = []*os.File{notification}
			input, err := cmd.StdinPipe()
			require.NoError(t, err)
			defer input.Close()
			output, err := cmd.StdoutPipe()
			require.NoError(t, err)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			require.NoError(t, notification.Close())
			require.NoError(t, progress.SetReadDeadline(time.Now().Add(5*time.Second)))
			var marker [1]byte
			_, err = io.ReadFull(progress, marker[:])
			require.NoError(t, err)
			require.Equal(t, "1", string(marker[:]))
			line, err := bufio.NewReader(output).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "{}\n", line)
			execution := latestExecution(t, fixtureDir, "mock")
			receiptPath := resolveFixtureReference(t, executionPackageRoot(t, fixtureDir, execution), str(obj(array(execution["connections"])[0])["receipt"]))
			// Publication may reserve the path, but must not claim success yet.
			require.Empty(t, readFile(t, receiptPath))
			switch ending {
			case "EOF":
				require.NoError(t, input.Close())
			case "extra":
				_, err = io.WriteString(input, " ")
				require.NoError(t, err)
				require.NoError(t, input.Close())
			}
			err = cmd.Wait()
			receipt := readJSON(t, receiptPath)
			require.Equal(t, true, receipt["fullConsumption"])
			code := 0
			if ending == "EOF" {
				require.NoError(t, err, stderr.String())
				require.Equal(t, "passed", receipt["status"])
				require.Equal(t, true, receipt["clientEOF"])
			} else {
				code = 1
				require.Error(t, err)
				require.Equal(t, "failed", receipt["status"])
				require.Equal(t, false, receipt["clientEOF"])
				if ending == "extra" {
					require.Equal(t, "strict-replay", receipt["failureClass"])
				}
				require.NotEmpty(t, stderr.String())
			}
			require.Equal(t, code, cmd.ProcessState.ExitCode())
		})
	}
}
