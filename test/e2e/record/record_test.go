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
	var runtime struct {
		SchemaVersion float64 `json:"schemaVersion"`
		Workspace     string  `json:"workspace"`
		Output        string  `json:"output"`
		Peer          struct {
			tool.Command
			Probe *tool.Command `json:"probe"`
		} `json:"peer"`
	}
	if tool.ReadJSON(req.Runtime, &runtime) != nil || runtime.SchemaVersion != 1 {
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
	if _, err := os.Stat(filepath.Join(runtime.Workspace, "numbers.txt")); err != nil {
		return 4
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 5
	}
	cmd := exec.CommandContext(context.Background(), runtime.Peer.Command.Command, runtime.Peer.Command.Args...)
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
		"workspace": runtime.Workspace,
		"home":      os.Getenv("HOME"),
		"config":    os.Getenv(configKey),
		"tmp":       os.Getenv("TMPDIR"),
	}
	if tool.WriteJSON(filepath.Join(runtime.Output, "report.json"), tool.Object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": status, "branchStatus": status, "captureStatus": "complete", "officialSDK": tool.Object{"package": map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider], "version": "synthetic-only"}, "observed": observed}) != nil {
		return 6
	}
	if tool.WriteJSON(filepath.Join(runtime.Output, "result.json"), tool.Result{Status: status, Message: "synthetic-only"}) != nil {
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
		if err := os.WriteFile(history, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"synthetic-history\",\"cwd\":\"/synthetic\"}}\n"), 0o600); err != nil {
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
	fixtureDir string
	cli        string
	bin        string
	env        []string
	provider   string
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
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	prepare := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "prepare", "--agent", provider, "--case", "SDK-CASE-001", "--fixture-dir", fixtureDir)
	prepare.Dir = t.TempDir()
	prepare.Env = env
	data, err := prepare.CombinedOutput()
	require.NoError(t, err, "%s", data)
	fixture := &preparedRecord{fixtureDir: fixtureDir, cli: cli, bin: bin, env: env, provider: provider}
	t.Cleanup(func() {
		var descriptor tool.Object
		if tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor) != nil {
			return
		}
		_ = os.RemoveAll(filepath.Join(fixtureDir, "runtime"))
	})
	return fixture
}

func recordVerifyDriverSubstitute(args []string) int {
	status := args[0]
	var req official.Request
	if len(args) != 3 || args[1] != "--request" || tool.ReadJSON(args[2], &req) != nil {
		return 2
	}
	var runtime struct {
		SchemaVersion float64      `json:"schemaVersion"`
		Output        string       `json:"output"`
		Peer          tool.Command `json:"peer"`
	}
	if tool.ReadJSON(req.Runtime, &runtime) != nil || runtime.SchemaVersion != 1 {
		return 2
	}
	var plan tool.Object
	if tool.ReadJSON(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	cmd := exec.CommandContext(context.Background(), runtime.Peer.Command, runtime.Peer.Args...)
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
	if tool.WriteJSON(filepath.Join(runtime.Output, "replay-report.json"), tool.Object{
		"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"],
		"status": status, "branchStatus": status, "captureStatus": "complete",
		"officialSDK": tool.Object{
			"package": map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[tool.Str(plan["provider"])],
			"version": "synthetic-test-only",
		},
	}) != nil {
		return 4
	}
	if tool.WriteJSON(filepath.Join(runtime.Output, "result.json"), tool.Object{"status": status}) != nil {
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
	args := []string{"official", "record", "--fixture-dir", p.fixtureDir, "--cmd", p.cli}
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
	require.NoError(t, tool.ReadJSON(filepath.Join(p.fixtureDir, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	require.NotEmpty(t, rows)
	last := tool.Obj(rows[len(rows)-1])
	require.Equal(t, "record", last["command"])
	return filepath.Join(p.fixtureDir, "packages", tool.Str(last["materialId"]), "material")
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
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.officialRoot(t), "report.json"), &report))
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
	resolvedFixture, err := filepath.EvalSymlinks(fixture.fixtureDir)
	require.NoError(t, err)
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.fixtureDir, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	require.NotEmpty(t, rows)
	row := tool.Obj(rows[len(rows)-1])
	require.Equal(t, "record", row["command"])
	id := tool.Str(row["id"])
	var execution, report tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.fixtureDir, "packages", tool.Str(row["materialId"]), "executions", id, "execution.json"), &execution))
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.officialRoot(t), "report.json"), &report))

	observed := tool.Obj(report["observed"])
	workspace := filepath.Join(fixture.fixtureDir, "runtime", "executions", id, "workspace")
	overlay := filepath.Join(fixture.fixtureDir, "runtime", "executions", id)
	resolvedHost, resolveErr := filepath.EvalSymlinks(tool.Str(observed["workspace"]))
	require.NoError(t, resolveErr)
	require.Equal(t, resolvedHost, observed["cwd"])
	require.Contains(t, tool.Str(observed["workspace"]), filepath.Join("routes", "record"))
	require.True(t, strings.HasPrefix(workspace, filepath.Join(fixture.fixtureDir, "runtime", "executions", id)+string(filepath.Separator)))
	resolvedWorkspace, resolveWorkspaceErr := filepath.EvalSymlinks(workspace)
	require.NoError(t, resolveWorkspaceErr)
	require.True(t, strings.HasPrefix(resolvedWorkspace, resolvedFixture+string(filepath.Separator)))
	require.NotContains(t, execution, "protocolCwd")
	require.NotEqual(t, observed["home"], filepath.Join(overlay, "home"))
	require.NotEqual(t, observed["config"], filepath.Join(overlay, "config"))
	require.NotEqual(t, observed["tmp"], filepath.Join(overlay, "tmp"))
}

func TestRecordVerifyMockRetainsBoundHistory(t *testing.T) {
	fixture := recordProcess(t, "codex", "passed", "history")
	data, err := fixture.command(t, recordExecutionTimeout).CombinedOutput()
	require.NoError(t, err, "%s", data)

	const history = "history/runtime/config/sessions/synthetic-history.jsonl"
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.recordRoot(t), "manifest.json"), &manifest))
	require.Equal(t, tool.DigestBytes([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"synthetic-history\",\"cwd\":\"/synthetic\"}}\n")), tool.Obj(manifest["files"])[history])

	var before tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.fixtureDir, "packages", "index.json"), &before))
	var current tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.fixtureDir, "current.json"), &current))
	var capture tool.Object
	for _, value := range tool.Array(before["materials"]) {
		artifact := tool.Obj(value)
		if tool.Str(artifact["id"]) == tool.Str(current["materialId"]) {
			capture = artifact
		}
	}
	require.NotEmpty(t, capture)

	fixture.installVerifyDriver(t)
	verify := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "official", "verify", "--fixture-dir", fixture.fixtureDir)
	verify.Env = fixture.env
	data, err = verify.CombinedOutput()
	require.NoError(t, err, "%s", data)
	verifyExecution := latestExecution(t, fixture.fixtureDir, "mock")
	execution := verifyExecution
	artifact := tool.Obj(execution["artifact"])
	require.Equal(t, tool.Str(capture["id"]), artifact["id"])
	require.Equal(t, "official", execution["provenance"])
	connection := tool.Obj(tool.Array(execution["connections"])[0])
	require.Equal(t, tool.Str(artifact["id"]), connection["materialId"])
	require.Equal(t, tool.Str(artifact["planSHA256"]), connection["planSHA256"])
	require.Equal(t, tool.Str(artifact["manifestSHA256"]), connection["manifestSHA256"])
	require.Equal(t, tool.Str(artifact["recordingSHA256"]), connection["recordingSHA256"])

	var after tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.fixtureDir, "packages", "index.json"), &after))
	require.NoError(t, tool.ReadJSON(filepath.Join(fixture.fixtureDir, "current.json"), &current))
	require.Equal(t, tool.Str(capture["id"]), tool.Str(current["materialId"]))
	require.Len(t, tool.Array(after["materials"]), len(tool.Array(before["materials"])))

	mock := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "agent", "mock", "--fixture-dir", fixture.fixtureDir, "--", "app-server", "--listen", "stdio://")
	mock.Env = fixture.env
	mock.Stdin = strings.NewReader(`{"id":1,"method":"synthetic","unknown":9007199254740993}` + "\n")
	data, err = mock.Output()
	require.NoError(t, err, "%s", data)
	require.JSONEq(t, `{"id":1,"result":{"unknown":1.2300}}`, string(data))
	mockExecution := latestExecution(t, fixture.fixtureDir, "mock")

	for _, execution := range []tool.Object{verifyExecution, mockExecution} {
		assert.Equal(t, "official", execution["provenance"])
		path := filepath.Join(fixture.fixtureDir, "runtime", "executions", tool.Str(execution["executionId"]), "config", "sessions", "synthetic-history.jsonl")
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.True(t, bytes.Equal([]byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"synthetic-history\",\"cwd\":\"/synthetic\"}}\n"), content), "unexpected history bytes: %q", content)
	}
}

func latestExecution(t *testing.T, fixtureDir, command string) tool.Object {
	t.Helper()
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	for i := range slices.Backward(rows) {
		row := tool.Obj(rows[i])
		if tool.Str(row["command"]) != command {
			continue
		}
		var execution tool.Object
		require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "packages", tool.Str(row["materialId"]), "executions", tool.Str(row["id"]), "execution.json"), &execution))
		return execution
	}
	t.Fatalf("fixture has no %s execution", command)
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
			if test.status != "" {
				var report tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(officialRoot, "report.json"), &report))
				require.Equal(t, test.status, report["status"])
			} else {
				require.NoFileExists(t, filepath.Join(officialRoot, "report.json"))
			}
			require.FileExists(t, filepath.Join(officialRoot, "stderr.log"))
			require.FileExists(t, filepath.Join(fixture.fixtureDir, "runtime", "official", filepath.Base(officialRoot), "materials", "case.json"))
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
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "prepare", "--agent", "claude", "--case", "SDK-CASE-001", "--fixture-dir", fixtureDir)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/missing", "HOME=" + t.TempDir(), "CLAUDE_CONFIG_DIR=" + config, "GORACE=" + os.Getenv("GORACE")}
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var diagnostic tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "prepare.json"), &diagnostic))
	require.NotEmpty(t, diagnostic["error"])
	require.NoFileExists(t, filepath.Join(fixtureDir, "fixture.json"))
}

func TestRecordHelpAndSelectionErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		message string
		success bool
	}{
		{"help", []string{"--help"}, "fixture-dir", true},
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
		{"provider", []string{"prepare", "--agent", "other", "--case", "SDK-CASE-001", "--fixture-dir", filepath.Join(t.TempDir(), "fixture")}, "unsupported agent type"},
		{"missing-case", []string{"prepare", "--agent", "codex", "--case", "SDK-CASE-999", "--fixture-dir", filepath.Join(t.TempDir(), "fixture")}, "absent from shared materials"},
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
	paths, err := filepath.Glob(filepath.Join(p.fixtureDir, "packages", "*", "record", "*", "result.json"))
	require.NoError(t, err)
	failures, err := filepath.Glob(filepath.Join(p.fixtureDir, "failures", "official-record", "*", "result.json"))
	require.NoError(t, err)
	paths = append(paths, failures...)
	require.Len(t, paths, 1)
	return filepath.Dir(paths[0])
}
