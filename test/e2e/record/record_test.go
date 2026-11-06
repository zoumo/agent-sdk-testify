package record

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const recordExecutionTimeout = "30s"

// These processes replace only the installed official driver and native Agent.
// The installed tool, environment preparation and recording proxy are real.
func recordDriverSubstitute(args []string) int {
	status := args[0]
	if status == "hang" {
		time.Sleep(time.Minute)
		return 9
	}
	if status == "crash" {
		return 9
	}
	var req official.Request
	if len(args) != 3 || args[1] != "--request" || tool.ReadJSON(args[2], &req) != nil {
		return 2
	}
	var plan tool.Object
	if tool.ReadJSON(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	provider := tool.Str(plan["provider"])
	configKey := "CODEX_HOME"
	if provider == "claude" {
		configKey = "CLAUDE_CONFIG_DIR"
	}
	if _, err := os.Stat(filepath.Join(req.Workspace, "numbers.txt")); err != nil {
		return 4
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 5
	}
	cmd := exec.CommandContext(context.Background(), req.Peer.Command.Command, req.Peer.Command.Args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return 5
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 5
	}
	cmd.Stderr = os.Stderr
	if cmd.Start() != nil {
		return 5
	}
	fmt.Fprintln(in, `{"id":1,"method":"synthetic","unknown":9007199254740993}`)
	// End the request stream even when native startup fails before a response.
	_ = in.Close()
	_, readErr := bufio.NewReader(out).ReadBytes('\n')
	waitErr := cmd.Wait()
	if status != "false-pass" && (readErr != nil || waitErr != nil) {
		status = "failed"
	}
	if status == "false-pass" {
		status = "passed"
	}
	observed := tool.Object{
		"cwd":       cwd,
		"workspace": req.Workspace,
		"home":      os.Getenv("HOME"),
		"config":    os.Getenv(configKey),
		"tmp":       os.Getenv("TMPDIR"),
	}
	if tool.WriteJSON(filepath.Join(req.Output, "report.json"), tool.Object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": status, "branchStatus": status, "captureStatus": "complete", "officialSDK": tool.Object{"package": map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider], "version": "synthetic-only"}, "observed": observed}) != nil {
		return 6
	}
	if tool.WriteJSON(filepath.Join(req.Output, "result.json"), tool.Result{Status: status, Message: "synthetic-only"}) != nil {
		return 6
	}
	return 0
}

func recordAgentSubstitute(args []string) int {
	if len(args) > 1 && args[1] == "--version" {
		fmt.Println("synthetic-native-only")
		return 0
	}
	if args[0] == "crash" {
		fmt.Fprintln(os.Stderr, "synthetic native crash")
		return 7
	}
	if args[0] == "history" {
		history := filepath.Join(os.Getenv("CODEX_HOME"), "sessions", "synthetic-history.jsonl")
		if err := os.MkdirAll(filepath.Dir(history), 0o700); err != nil {
			return 8
		}
		if err := os.WriteFile(history, []byte("{\"type\":\"official-history\"}\n"), 0o600); err != nil {
			return 8
		}
	}
	reader := bufio.NewReader(os.Stdin)
	if _, err := reader.ReadBytes('\n'); err != nil {
		return 2
	}
	fmt.Fprintln(os.Stdout, `{"id":1,"result":{"unknown":1.2300}}`)
	fmt.Fprintln(os.Stderr, "synthetic native stderr")
	for {
		if _, err := reader.ReadByte(); err != nil {
			return 0
		}
	}
}

type preparedRecord struct {
	session  string
	cli      string
	bin      string
	env      []string
	provider string
}

func recordProcess(t *testing.T, provider, status, native string) *preparedRecord {
	t.Helper()
	bin := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	_, err = writePeerLauncher(bin, "official", tool.Command{Command: self, Args: []string{"record-driver-substitute", status}})
	require.NoError(t, err)
	driverPath := filepath.Join(bin, "agent-sdk-testify-"+provider)
	require.NoError(t, os.Rename(filepath.Join(bin, "peer-official"), driverPath))
	for _, path := range []string{driverPath} {
		script, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(script[:len(script)-1], []byte(" \"$@\"\n")...), 0o700))
	}
	cli, err := writePeerLauncher(bin, "native", tool.Command{Command: self, Args: []string{"record-agent-substitute", native}})
	require.NoError(t, err)
	script, err := os.ReadFile(cli)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cli, append(script[:len(script)-1], []byte(" \"$@\"\n")...), 0o700))
	config := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(config, "AGENTS.md"), []byte("personal instructions must not be copied"), 0o600))
	home := t.TempDir()
	configKey := "CODEX_HOME"
	if provider == "claude" {
		configKey = "CLAUDE_CONFIG_DIR"
	}
	env := []string{"PATH=" + bin, "HOME=" + home, configKey + "=" + config, "GORACE=" + os.Getenv("GORACE")}
	session := filepath.Join(t.TempDir(), "session")
	prepare := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "prepare", "--agent", provider, "--case", "SDK-CASE-001", "--session-dir", session)
	prepare.Dir = t.TempDir()
	prepare.Env = env
	data, err := prepare.CombinedOutput()
	require.NoError(t, err, "%s", data)
	fixture := &preparedRecord{session: session, cli: cli, bin: bin, env: env, provider: provider}
	t.Cleanup(func() {
		var descriptor tool.Object
		if tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor) != nil {
			return
		}
		_ = os.RemoveAll(tool.Str(descriptor["resourceRoot"]))
	})
	return fixture
}

func recordVerifyDriverSubstitute(args []string) int {
	status := args[0]
	var req official.Request
	if len(args) != 3 || args[1] != "--request" || tool.ReadJSON(args[2], &req) != nil {
		return 2
	}
	var plan tool.Object
	if tool.ReadJSON(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	cmd := exec.CommandContext(context.Background(), req.Peer.Command.Command, req.Peer.Command.Args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return 3
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 3
	}
	cmd.Stderr = os.Stderr
	if cmd.Start() != nil {
		return 3
	}
	fmt.Fprintln(in, `{"id":1,"method":"synthetic","unknown":9007199254740993}`)
	_ = in.Close()
	_, _ = bufio.NewReader(out).ReadBytes('\n')
	if cmd.Wait() != nil {
		status = "failed"
	}
	if tool.WriteJSON(filepath.Join(req.Output, "replay-report.json"), tool.Object{
		"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"],
		"status": status, "branchStatus": status, "captureStatus": "complete",
		"officialSDK": tool.Object{
			"package": map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[tool.Str(plan["provider"])],
			"version": "synthetic-test-only",
		},
	}) != nil {
		return 4
	}
	if tool.WriteJSON(filepath.Join(req.Output, "result.json"), tool.Object{"status": status}) != nil {
		return 4
	}
	return 0
}

func (p *preparedRecord) installVerifyDriver(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	driver := filepath.Join(p.bin, "agent-sdk-testify-"+p.provider)
	script := fmt.Sprintf("#!/bin/sh\n%sexec %q record-verify-driver-substitute passed \"$@\"\n", testutil.RaceShellExport(), self)
	require.NoError(t, os.WriteFile(driver, []byte(script), 0o700))
}

func (p *preparedRecord) command(t *testing.T, timeout string) *exec.Cmd {
	t.Helper()
	args := []string{"official", "record", "--session-dir", p.session, "--cmd", p.cli}
	if timeout != "" {
		args = append(args, "--timeout", timeout)
	}
	native := []string{"app-server", "--listen", "stdio://"}
	if p.provider == "claude" {
		native = []string{"--output-format", "stream-json", "--input-format", "stream-json", "--permission-prompt-tool", "stdio"}
	}
	args = append(args, "--")
	args = append(args, native...)
	cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Dir = t.TempDir()
	cmd.Env = p.env
	cmd.WaitDelay = 15 * time.Second
	return cmd
}

func (p *preparedRecord) recordRoot(t *testing.T) string {
	t.Helper()
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(p.session, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	require.NotEmpty(t, rows)
	last := tool.Obj(rows[len(rows)-1])
	require.Equal(t, "record", last["command"])
	return filepath.Join(p.session, "executions", tool.Str(last["id"]), "record")
}

func TestRecordUsesInstalledOfficialDriverOutsideCheckout(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) { checkRecordCapture(t, provider) })
	}
}

func checkRecordCapture(t *testing.T, provider string) {
	fixture := recordProcess(t, provider, "passed", "passed")
	cmd := fixture.command(t, recordExecutionTimeout)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var summary tool.Object
	require.Empty(t, data)
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.officialRoot(t), "result.json"), &summary))
	require.Equal(t, "passed", summary["status"])
	output := fixture.recordRoot(t)
	var plan, manifest, transport, report tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "plan.json"), &plan))
	require.Equal(t, "SDK-CASE-001", plan["caseId"])
	require.Equal(t, "continuation", plan["driverProfile"])
	require.Equal(t, "personal-skills-v1", plan["recordingIsolation"])
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "manifest.json"), &manifest))
	require.Equal(t, "captured", manifest["status"])
	require.Equal(t, "complete", manifest["captureStatus"])
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "raw", "transport.json"), &transport))
	require.Equal(t, true, transport["cleanupComplete"])
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.officialRoot(t), "driver", "report.json"), &report))
	require.Equal(t, "synthetic-only", tool.Obj(report["officialSDK"])["version"])
	raw, err := os.ReadFile(filepath.Join(output, "raw", "raw.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "9007199254740993")
	require.Contains(t, string(raw), "1.2300")
	stderr, err := os.ReadFile(filepath.Join(output, "raw", "stderr.raw"))
	require.NoError(t, err)
	require.Contains(t, string(stderr), "synthetic native stderr")
	require.NoFileExists(t, filepath.Join(output, "config", "AGENTS.md"))
	require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
	for _, name := range []string{"auth.json", ".credentials.json", "config.toml", "settings.json"} {
		require.NoFileExists(t, filepath.Join(output, "config", name))
	}
	require.NoFileExists(t, filepath.Join(output, "review.json"))
	if provider == "codex" {
		checkRecordExecutionPaths(t, fixture)
	}
}

func checkRecordExecutionPaths(t *testing.T, fixture *preparedRecord) {
	t.Helper()
	resolvedSession, err := filepath.EvalSymlinks(fixture.session)
	require.NoError(t, err)
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.session, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	require.NotEmpty(t, rows)
	row := tool.Obj(rows[len(rows)-1])
	require.Equal(t, "record", row["command"])
	id := tool.Str(row["id"])
	var execution, report tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.session, "executions", id, "execution.json"), &execution))
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.officialRoot(t), "driver", "report.json"), &report))

	observed := tool.Obj(report["observed"])
	workspace := tool.Str(execution["workspace"])
	resolvedHost, resolveErr := filepath.EvalSymlinks(tool.Str(observed["workspace"]))
	require.NoError(t, resolveErr)
	require.Equal(t, resolvedHost, observed["cwd"])
	require.Contains(t, tool.Str(observed["workspace"]), filepath.Join("routes", "record"))
	var sessionDescriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(resolvedSession, "session.json"), &sessionDescriptor))
	require.True(t, strings.HasPrefix(workspace, filepath.Join(tool.Str(sessionDescriptor["resourceRoot"]), "executions", id)+string(filepath.Separator)))
	require.True(t, strings.HasPrefix(workspace, resolvedSession+string(filepath.Separator)))
	require.Equal(t, workspace, execution["protocolCwd"])
	require.NotEqual(t, observed["home"], execution["home"])
	require.NotEqual(t, observed["config"], execution["config"])
	require.NotEqual(t, observed["tmp"], execution["tmp"])
	require.Equal(t, execution["home"], tool.Obj(execution["env"])["HOME"])
	require.Equal(t, execution["config"], tool.Obj(execution["env"])["CODEX_HOME"])
	require.Equal(t, execution["tmp"], tool.Obj(execution["env"])["TMPDIR"])
}

func TestRecordVerifyMockRetainsBoundHistory(t *testing.T) {
	fixture := recordProcess(t, "codex", "passed", "history")
	data, err := fixture.command(t, recordExecutionTimeout).CombinedOutput()
	require.NoError(t, err, "%s", data)

	const history = "config/sessions/synthetic-history.jsonl"
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.recordRoot(t), "manifest.json"), &manifest))
	require.Equal(t, tool.DigestBytes([]byte("{\"type\":\"official-history\"}\n")), tool.Obj(manifest["savedFiles"])[history])

	var before tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.session, "materials", "index.json"), &before))
	var current tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.session, "current.json"), &current))
	var capture tool.Object
	for _, value := range tool.Array(before["materials"]) {
		artifact := tool.Obj(value)
		if tool.Str(artifact["id"]) == tool.Str(current["materialId"]) {
			capture = artifact
		}
	}
	require.NotEmpty(t, capture)

	fixture.installVerifyDriver(t)
	verify := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "official", "verify", "--session-dir", fixture.session)
	verify.Env = fixture.env
	data, err = verify.CombinedOutput()
	require.NoError(t, err, "%s", data)
	verifyExecution := latestExecution(t, fixture.session, "mock")
	execution := verifyExecution
	artifact := tool.Obj(execution["artifact"])
	require.Equal(t, tool.Str(capture["path"]), artifact["path"])
	require.Equal(t, "official", execution["provenance"])
	connection := tool.Obj(tool.Array(execution["connections"])[0])
	require.Equal(t, tool.Str(artifact["path"]), connection["material"])
	require.Equal(t, tool.Str(artifact["planSHA256"]), connection["planSHA256"])
	require.Equal(t, tool.Str(artifact["manifestSHA256"]), connection["manifestSHA256"])
	require.Equal(t, tool.Str(artifact["recordingSHA256"]), connection["recordingSHA256"])

	var after tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.session, "materials", "index.json"), &after))
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.session, "current.json"), &current))
	require.Equal(t, tool.Str(capture["id"]), tool.Str(current["materialId"]))
	require.Len(t, tool.Array(after["materials"]), len(tool.Array(before["materials"])))

	mock := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "agent", "mock", "--session-dir", fixture.session, "--", "app-server", "--listen", "stdio://")
	mock.Env = fixture.env
	mock.Stdin = strings.NewReader(`{"id":1,"method":"synthetic","unknown":9007199254740993}` + "\n")
	data, err = mock.Output()
	require.NoError(t, err, "%s", data)
	require.JSONEq(t, `{"id":1,"result":{"unknown":1.2300}}`, string(data))
	mockExecution := latestExecution(t, fixture.session, "mock")

	for _, execution := range []tool.Object{verifyExecution, mockExecution} {
		assert.Equal(t, "official", execution["provenance"])
		path := filepath.Join(tool.Str(execution["config"]), "sessions", "synthetic-history.jsonl")
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.True(t, bytes.Equal([]byte("{\"type\":\"official-history\"}\n"), content), "unexpected history bytes: %q", content)
	}
}

func latestExecution(t *testing.T, session, command string) tool.Object {
	t.Helper()
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	for i := range slices.Backward(rows) {
		row := tool.Obj(rows[i])
		if tool.Str(row["command"]) != command {
			continue
		}
		var execution tool.Object
		require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", tool.Str(row["id"]), "execution.json"), &execution))
		return execution
	}
	t.Fatalf("session has no %s execution", command)
	return nil
}

func TestRecordPreservesFailedAttempts(t *testing.T) {
	for _, test := range []struct{ name, driver, native, status string }{
		{"business-failed", "failed", "passed", "failed"},
		{"untriggered", "untriggered", "passed", "untriggered"},
		{"unsupported", "unsupported", "passed", "unsupported"},
		{"driver-crash", "crash", "passed", ""},
		{"native-crash-after-business-pass", "false-pass", "crash", "passed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := recordProcess(t, "codex", test.driver, test.native)
			cmd := fixture.command(t, recordExecutionTimeout)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			officialRoot := fixture.officialRoot(t)
			var result tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(officialRoot, "result.json"), &result))
			require.Equal(t, "failed", result["status"])
			require.Equal(t, test.status, tool.Str(tool.Obj(result["business"])["status"]))
			require.FileExists(t, filepath.Join(officialRoot, "driver", "stderr.log"))
			require.FileExists(t, filepath.Join(officialRoot, "driver", "materials", "case.json"))
			if test.driver != "crash" {
				output := fixture.recordRoot(t)
				require.FileExists(t, filepath.Join(output, "raw", "raw.jsonl"))
				require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
				if test.native != "crash" {
					var manifest tool.Object
					require.NoError(t, tool.ReadJSON(filepath.Join(output, "manifest.json"), &manifest))
					require.Equal(t, "captured", manifest["status"])
					require.Equal(t, "complete", manifest["captureStatus"])
				} else {
					var receipt tool.Object
					require.NoError(t, tool.ReadJSON(filepath.Join(output, "raw", "transport.json"), &receipt))
					require.Equal(t, "failed", receipt["captureStatus"])
				}
			}
		})
	}
}

func TestRecordPreparationFailureKeepsDiagnostic(t *testing.T) {
	config := filepath.Join(t.TempDir(), "invalid-config")
	require.NoError(t, os.WriteFile(config, []byte("not a directory"), 0o600))
	session := filepath.Join(t.TempDir(), "session")
	cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "prepare", "--agent", "claude", "--case", "SDK-CASE-001", "--session-dir", session)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/missing", "HOME=" + t.TempDir(), "CLAUDE_CONFIG_DIR=" + config, "GORACE=" + os.Getenv("GORACE")}
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var diagnostic tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "prepare.json"), &diagnostic))
	require.NotEmpty(t, diagnostic["error"])
	require.NoFileExists(t, filepath.Join(session, "session.json"))
}

func TestRecordHelpAndSelectionErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		message string
		success bool
	}{
		{"help", []string{"--help"}, "session-dir", true},
		{"arbitrary-driver", []string{"--driver", "/never-execute"}, "unknown flag", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), append([]string{"official", "record"}, test.args...)...)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=/missing", "GORACE=" + os.Getenv("GORACE")}
			data, err := cmd.CombinedOutput()
			if test.success {
				require.NoError(t, err, "%s", data)
			} else {
				require.Error(t, err)
			}
			require.Contains(t, string(data), test.message)
		})
	}
	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{"provider", []string{"prepare", "--agent", "other", "--case", "SDK-CASE-001", "--session-dir", filepath.Join(t.TempDir(), "session")}, "unsupported agent type"},
		{"missing-case", []string{"prepare", "--agent", "codex", "--case", "SDK-CASE-999", "--session-dir", filepath.Join(t.TempDir(), "session")}, "absent from shared materials"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), test.args...)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=/missing", "HOME=" + t.TempDir(), "GORACE=" + os.Getenv("GORACE")}
			data, err := cmd.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(data), test.message)
		})
	}
}

func TestRecordTimeoutPreservesDiagnostic(t *testing.T) {
	fixture := recordProcess(t, "codex", "hang", "passed")
	cmd := fixture.command(t, "500ms")
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	output := filepath.Join(fixture.officialRoot(t), "driver")
	var summary tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.officialRoot(t), "result.json"), &summary))
	require.Contains(t, tool.Str(summary["error"]), "context deadline exceeded")
	require.NoFileExists(t, filepath.Join(output, "native.env.private.json"))
}

func TestRecordRequiresInstalledOfficialDriver(t *testing.T) {
	fixture := recordProcess(t, "codex", "passed", "passed")
	for _, name := range []string{"agent-sdk-testify-codex", "agent-sdk-testify-claude"} {
		_ = os.Remove(filepath.Join(filepath.Dir(fixture.cli), name))
	}
	cmd := fixture.command(t, recordExecutionTimeout)
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	require.Contains(t, string(data), "install the driver wheel")
}

func (p *preparedRecord) officialRoot(t *testing.T) string {
	t.Helper()
	var ids []string
	require.NoError(t, tool.ReadJSON(filepath.Join(p.session, "official-records", "index.json"), &ids))
	require.NotEmpty(t, ids)
	return filepath.Join(p.session, "official-records", ids[len(ids)-1])
}
