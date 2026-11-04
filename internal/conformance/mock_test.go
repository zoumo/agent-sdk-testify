package conformance

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMockUnverifiedConsumerCurrent(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	producer := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "mock-unverified"))
	recordConsumer(t, env, producer, agent, requestOne)
	original := str(latestCapture(t, producer)["path"])
	session := prepareSession(t, env, "--recording", original)
	finished, _ := finishSession(t, env, producer)
	require.Zero(t, finished.code, finished.stderr)
	require.NoError(t, os.RemoveAll(producer))
	material := obj(array(materialIndex(t, session)["materials"])[0])
	require.Equal(t, "consumer", material["provenance"])
	require.NoFileExists(t, filepath.Join(str(material["path"]), "verification.json"))
	before := readAllFiles(t, str(material["path"]))
	current := readFile(t, filepath.Join(session, "current.json"))
	descriptor := readFile(t, filepath.Join(session, "session.json"))
	// The material has no official verifier; this must not become a mock gate.
	rejected := runTool(t, env, t.TempDir(), "", "verify", "--session-dir", session)
	require.Equal(t, 1, rejected.code, rejected.stderr)
	require.Contains(t, rejected.stderr, "no applicable official verifier")
	require.Empty(t, array(sessionIndex(t, session)["executions"]))
	var previousWorkspace string
	for range 2 {
		result := runPreparedMock(t, env, session, "", requestOne)
		require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, result.stdout)
		execution := latestExecution(t, session, "mock")
		require.Equal(t, material["id"], obj(execution["artifact"])["id"])
		require.NotEqual(t, previousWorkspace, str(execution["workspace"]))
		require.NoFileExists(t, filepath.Join(str(execution["workspace"]), "previous-execution"))
		require.NoError(t, os.WriteFile(filepath.Join(str(execution["workspace"]), "previous-execution"), []byte("isolated"), 0o600))
		previousWorkspace = str(execution["workspace"])
		connection := obj(array(execution["connections"])[0])
		receipt := readJSON(t, str(connection["receipt"]))
		require.Equal(t, "passed", receipt["status"])
		require.Equal(t, true, receipt["fullConsumption"])
		require.Equal(t, true, receipt["clientEOF"])
		require.Equal(t, true, receipt["cleanupComplete"])
		require.Equal(t, false, receipt["nativeLaunched"])
		process := readJSON(t, str(connection["processReceipt"]))
		require.Equal(t, "passed", process["status"])
		require.Zero(t, process["exitCode"])
	}
	finished, finish := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
	require.Equal(t, true, finish["cleanupComplete"])
	require.NoDirExists(t, str(readJSON(t, filepath.Join(session, "session.json"))["resourceRoot"]))
	require.Equal(t, before, readAllFiles(t, str(material["path"])))
	require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
	require.Equal(t, descriptor, readFile(t, filepath.Join(session, "session.json")))
}

func TestMockRejectsRemovedInputsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	before := readAllFiles(t, session)
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
		{"app-server", "--listen", "http://localhost:1"},
		{"--sdk-test-handshakes", "answer"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			argv := append([]string{"mock", "--session-dir", session}, args...)
			result := runTool(t, env, t.TempDir(), requestOne, argv...)
			require.Equal(t, 1, result.code, result.stderr)
			require.Empty(t, result.stdout)
			require.True(t, bytes.Equal(before, readAllFiles(t, session)), "rejected input changed session files")
			require.NoFileExists(t, filepath.Join(root, "report.json"))
		})
	}
	result := runTool(t, env, t.TempDir(), "", "mock")
	require.Equal(t, 1, result.code, result.stderr)
	require.Contains(t, result.stderr, "mock requires --session-dir")
	finished, _ := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
}

func TestMockLauncherRejectsMetaCommandsAndOverrides(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	descriptor := readJSON(t, filepath.Join(session, "session.json"))
	route := obj(obj(descriptor["routes"])["mock"])
	launcher := str(obj(array(route["connections"])[0])["launcher"])
	before := readAllFiles(t, session)
	for _, args := range [][]string{{"--version"}, {"-v"}, {"--help"}, {"help"}, {"--session-dir", "/absent/session"}, {"--connection", "probe"}, {"--case", "SDK-CASE-001"}, {"app-server", "--listen", "http://localhost:1"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), launcher, args...)
			cmd.Env = env
			cmd.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			require.Error(t, cmd.Run())
			require.Equal(t, 1, cmd.ProcessState.ExitCode(), stderr.String())
			require.Empty(t, stdout.String())
			require.True(t, bytes.Equal(before, readAllFiles(t, session)), "launcher rejected input changed session files")
		})
	}
	inputs := builtinInputs(t, "codex", "SDK-CASE-001")
	cmd := exec.CommandContext(t.Context(), launcher, "app-server", "--listen", "stdio://")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	cmd.Stdin = strings.NewReader(inputs["main"])
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	execution := latestExecution(t, session, "mock")
	require.Equal(t, []string{"app-server", "--listen", "stdio://"}, stringsOf(execution["nativeArgs"]))
	finished, _ := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
}
