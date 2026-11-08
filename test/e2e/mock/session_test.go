package mock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func mockTestAgent() int {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("synthetic-consumer-native")
		return 0
	}
	config := os.Getenv("CODEX_HOME")
	if config != "" {
		history := filepath.Join(config, "sessions", "synthetic-history.jsonl")
		if err := os.MkdirAll(filepath.Dir(history), 0o700); err != nil {
			return 3
		}
		if err := os.WriteFile(history, []byte("{\"type\":\"consumer-history\"}\n"), 0o600); err != nil {
			return 3
		}
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return 0
		}
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &request) != nil {
			return 2
		}
		if len(request.ID) != 0 {
			fmt.Fprintf(os.Stdout, "{\"id\":%s,\"result\":{\"synthetic\":true}}\n", request.ID)
		}
	}
}

func sessionTool(t *testing.T, env []string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func sessionExecutable(t *testing.T, env []string, executable string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func latestSessionExecution(t *testing.T, session, command string) (tool.Object, string) {
	t.Helper()
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	rows := tool.Array(index["executions"])
	for i := range slices.Backward(rows) {
		row := tool.Obj(rows[i])
		if tool.Str(row["command"]) != command {
			continue
		}
		id := tool.Str(row["id"])
		var execution tool.Object
		require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", id, "execution.json"), &execution))
		return execution, id
	}
	t.Fatalf("session has no %s execution", command)
	return nil, ""
}

func routeHostEnvironment(route tool.Object, extra map[string]string) []string {
	values := map[string]string{"PATH": os.Getenv("PATH"), "GORACE": os.Getenv("GORACE")}
	for key, value := range tool.Obj(route["env"]) {
		values[key] = tool.Str(value)
	}
	maps.Copy(values, extra)
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func workspaceHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(rel)] = tool.DigestBytes(data)
		return nil
	}))
	return hashes
}

func consumerSession(t *testing.T) (string, []string) {
	t.Helper()
	base := t.TempDir()
	plan := filepath.Join(base, "plan.json")
	require.NoError(t, tool.WriteJSON(plan, tool.Object{
		"version":       1,
		"agentType":     "codex",
		"caseId":        "consumer-session",
		"driverProfile": "consumer",
		"project":       filepath.Join(base, "original-project"),
		"model":         "synthetic-model",
		"cliVersion":    "synthetic-consumer-native",
		"scenario": tool.Object{
			"files":      tool.Object{"fixture.txt": "fixture\n"},
			"parameters": tool.Object{"keep": 9007199254740993},
		},
	}))
	home := filepath.Join(base, "host-home")
	config := filepath.Join(base, "host-config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"CODEX_HOME=" + config,
		"GORACE=" + os.Getenv("GORACE"),
	}
	session := filepath.Join(base, "session")
	prepare := sessionTool(t, env, "prepare", "--plan", plan, "--session-dir", session)
	data, err := prepare.CombinedOutput()
	require.NoError(t, err, "%s", data)
	t.Cleanup(func() {
		var descriptor tool.Object
		if tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor) == nil {
			_ = os.RemoveAll(tool.Str(descriptor["resourceRoot"]))
		}
	})
	return session, env
}

func recordConsumerCapture(t *testing.T, session string, env []string) {
	t.Helper()
	recordConsumerRequest(t, session, env, `{"id":1,"method":"ping"}`+"\n")
}

func recordConsumerRequest(t *testing.T, session string, env []string, request string) {
	t.Helper()
	agent, err := os.Executable()
	require.NoError(t, err)
	launcher := filepath.Join(t.TempDir(), "native")
	quoted := "'" + strings.ReplaceAll(agent, "'", "'\"'\"'") + "'"
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\n"+testutil.RaceShellExport()+"exec "+quoted+" \"$@\"\n"), 0o700))
	agent = launcher
	record := sessionTool(t, env, "agent", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	record.Stdin = strings.NewReader(request)
	var stderr bytes.Buffer
	record.Stderr = &stderr
	out, err := record.Output()
	require.NoError(t, err, "%s", stderr.String())
	var requestObject tool.Object
	require.NoError(t, json.Unmarshal([]byte(request), &requestObject))
	assert.JSONEq(t, fmt.Sprintf(`{"id":%v,"result":{"synthetic":true}}`, requestObject["id"]), string(out))
}

func TestPreparedHalfOpenMockRetainsPendingConnection(t *testing.T) {
	mainInput, _ := builtinConnectionTranscript(t, "codex", "SDK-CASE-032", "main")
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-032")
	main := sessionTool(t, env, "agent", "mock", "--session-dir", session, "--connection", "main", "--", "app-server", "--listen", "stdio://")
	main.Stdin = strings.NewReader(mainInput)
	data, err := main.CombinedOutput()
	require.NoError(t, err, "%s", data)

	execution, executionID := latestSessionExecution(t, session, "mock")
	var mainConnection, probeConnection tool.Object
	for _, value := range tool.Array(execution["connections"]) {
		connection := tool.Obj(value)
		switch connection["id"] {
		case "main":
			mainConnection = connection
		case "probe":
			probeConnection = connection
		}
	}
	require.NotEmpty(t, mainConnection)
	require.NotEmpty(t, probeConnection)
	require.FileExists(t, tool.Str(mainConnection["receipt"]))
	require.NoFileExists(t, tool.Str(probeConnection["receipt"]))

	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	require.Equal(t, "open", tool.Obj(tool.Array(index["executions"])[0])["status"])

	require.FileExists(t, filepath.Join(session, "executions", executionID, "execution.json"))
	require.FileExists(t, tool.Str(mainConnection["receipt"]))
	require.FileExists(t, tool.Str(mainConnection["processReceipt"]))
	require.NoFileExists(t, tool.Str(probeConnection["receipt"]))
	require.NoFileExists(t, tool.Str(probeConnection["processReceipt"]))
}

func TestPreparedMockUsesCurrentWithoutRecordSource(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor))
	sourcePath := tool.Str(tool.Obj(descriptor["source"])["path"])
	require.NoError(t, os.Remove(sourcePath))
	cmd := sessionTool(t, env, "agent", "mock", "--session-dir", session)
	cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(data))
}

func TestPreparedSessionTamperRejected(t *testing.T) {
	session, env := consumerSession(t)
	path := filepath.Join(session, "session.json")
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(path, &descriptor))
	descriptor["toolVersion"] = "tampered"
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, tool.WriteJSON(path, descriptor))
	data, err := sessionTool(t, env, "agent", "mock", "--session-dir", session).CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "session descriptor changed after prepare")
}

func TestPreparedMockLauncherUsesIsolatedExecutionOverlays(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	resolvedSession, err := filepath.EvalSymlinks(session)
	require.NoError(t, err)
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor))
	route := tool.Obj(tool.Obj(descriptor["routes"])["mock"])
	routeWorkspace := tool.Str(route["workspace"])
	launcher := tool.Str(tool.Obj(tool.Array(route["connections"])[0])["launcher"])
	require.FileExists(t, launcher)
	routeRoot := filepath.Join(tool.Str(descriptor["resourceRoot"]), "routes", "mock")
	templateRoot := filepath.Join(tool.Str(descriptor["resourceRoot"]), "templates", "mock")
	assertRouteSlotsAtTemplate := func() {
		for _, name := range []string{"workspace", "home", "config", "tmp", "history-seed"} {
			require.Equal(t, filepath.Join(templateRoot, name), resolvedSymlink(t, filepath.Join(routeRoot, name)))
		}
	}

	observe := filepath.Join(t.TempDir(), "observed.json")
	wrapper := filepath.Join(t.TempDir(), "sdk-host")
	script := fmt.Sprintf(`#!/bin/sh
{
  printf 'cwd=%%s\n' "$(pwd -P)"
  printf 'home=%%s\n' "$(cd "$HOME" && pwd -P)"
  printf 'config=%%s\n' "$(cd "$CODEX_HOME" && pwd -P)"
  printf 'tmp=%%s\n' "$(cd "$TMPDIR" && pwd -P)"
} > '%s'
exec '%s' "$@"
`, observe, launcher)
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o700))

	run := func(label string) (tool.Object, string, map[string]string) {
		cmd := sessionExecutable(t, routeHostEnvironment(route, map[string]string{
			"RUN_LABEL": label,
		}), wrapper, "app-server", "--listen", "stdio://")
		cmd.Dir = routeWorkspace
		cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "%s", stderr.String())
		assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(out))
		require.FileExists(t, launcher)

		observed := map[string]string{}
		data, err := os.ReadFile(observe)
		require.NoError(t, err)
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			require.True(t, ok, line)
			observed[key] = value
		}
		execution, id := latestSessionExecution(t, session, "mock")
		return execution, id, observed
	}

	first, firstID, firstObserved := run("first")
	firstWorkspace := tool.Str(first["workspace"])
	firstRoot := filepath.Join(tool.Str(descriptor["resourceRoot"]), "executions", firstID)
	require.NotEqual(t, routeWorkspace, firstWorkspace)
	require.True(t, strings.HasPrefix(firstWorkspace, resolvedSession+string(filepath.Separator)))
	require.True(t, strings.HasPrefix(firstWorkspace, firstRoot+string(filepath.Separator)))
	require.Equal(t, tool.Str(first["home"]), tool.Str(tool.Obj(first["env"])["HOME"]))
	require.Equal(t, tool.Str(first["config"]), tool.Str(tool.Obj(first["env"])["CODEX_HOME"]))
	require.Equal(t, tool.Str(first["tmp"]), tool.Str(tool.Obj(first["env"])["TMPDIR"]))
	require.NotEmpty(t, firstObserved)
	assertRouteSlotsAtTemplate()
	firstConnection := tool.Obj(tool.Array(first["connections"])[0])
	var firstProcess tool.Object
	require.NoError(t, tool.ReadJSON(tool.Str(firstConnection["processReceipt"]), &firstProcess))
	require.Equal(t, firstObserved["cwd"], firstProcess["cwd"])
	require.Equal(t, firstWorkspace, firstProcess["workspace"])
	require.NoError(t, os.WriteFile(filepath.Join(firstWorkspace, "run-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tool.Str(first["home"]), "run-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tool.Str(first["config"]), "run-first.txt"), []byte("first\n"), 0o600))
	firstHashes := workspaceHashes(t, firstWorkspace)
	require.NoFileExists(t, filepath.Join(templateRoot, "workspace", "run-first.txt"))
	require.NoFileExists(t, filepath.Join(templateRoot, "home", "run-first.txt"))
	require.NoFileExists(t, filepath.Join(templateRoot, "config", "run-first.txt"))

	second, secondID, secondObserved := run("second")
	secondWorkspace := tool.Str(second["workspace"])
	secondRoot := filepath.Join(tool.Str(descriptor["resourceRoot"]), "executions", secondID)
	require.NotEqual(t, firstID, secondID)
	require.NotEqual(t, firstWorkspace, secondWorkspace)
	require.True(t, strings.HasPrefix(secondWorkspace, secondRoot+string(filepath.Separator)))
	require.NotEmpty(t, secondObserved)
	assertRouteSlotsAtTemplate()
	secondConnection := tool.Obj(tool.Array(second["connections"])[0])
	var secondProcess tool.Object
	require.NoError(t, tool.ReadJSON(tool.Str(secondConnection["processReceipt"]), &secondProcess))
	require.Equal(t, secondObserved["cwd"], secondProcess["cwd"])
	require.Equal(t, secondWorkspace, secondProcess["workspace"])
	require.FileExists(t, filepath.Join(secondWorkspace, "fixture.txt"))
	require.NoFileExists(t, filepath.Join(secondWorkspace, "run-first.txt"))
	require.NoFileExists(t, filepath.Join(tool.Str(second["home"]), "run-first.txt"))
	require.NoFileExists(t, filepath.Join(tool.Str(second["config"]), "run-first.txt"))
	require.NoFileExists(t, filepath.Join(firstWorkspace, "run-second.txt"))
	require.Equal(t, firstHashes, workspaceHashes(t, firstWorkspace))
}

func resolvedSymlink(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

func TestPreparedConsumerRecordFailure(t *testing.T) {
	session, env := consumerSession(t)
	record := sessionTool(t, env, "agent", "record", "--session-dir", session, "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
	data, err := record.CombinedOutput()
	require.Error(t, err, "%s", data)

	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	var materials tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "materials", "index.json"), &materials))
	assert.Empty(t, tool.Array(materials["materials"]))

	require.Equal(t, "failed", tool.Obj(tool.Array(index["executions"])[0])["status"])
}

func TestConsumerPlanAgentTypeCannotBeOverriddenByLegacyProvider(t *testing.T) {
	for _, legacy := range []string{"claude", "unknown-legacy-provider"} {
		t.Run(legacy, func(t *testing.T) {
			base := t.TempDir()
			plan := filepath.Join(base, "plan.json")
			require.NoError(t, tool.WriteJSON(plan, tool.Object{
				"version":       1,
				"agentType":     "codex",
				"provider":      legacy,
				"caseId":        "agent-identity-conflict",
				"driverProfile": "consumer",
				"project":       filepath.Join(base, "project"),
			}))
			session := filepath.Join(base, "session")
			cmd := sessionTool(t, []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + filepath.Join(base, "home"),
				"GORACE=" + os.Getenv("GORACE"),
			}, "prepare", "--agent", "codex", "--plan", plan, "--session-dir", session)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			assert.Contains(t, string(data), "agentType")
			assert.Contains(t, string(data), "provider")
			require.NoDirExists(t, session)
		})
	}
}

func syntheticCaseMaterial(t *testing.T, caseID, provenance string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	profile := "continuation"
	if provenance == "consumer" {
		profile = "consumer"
	}
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "plan.json"), tool.Object{
		"version":       1,
		"agentType":     "codex",
		"provider":      "codex",
		"caseId":        caseID,
		"driverProfile": profile,
		"project":       filepath.Join(t.TempDir(), "project"),
	}))
	const raw = "{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"protocol\",\"epoch\":1,\"elapsedMs\":0,\"wire\":{\"id\":1,\"method\":\"ping\"}}\n" +
		"{\"ordinal\":2,\"direction\":\"s2c\",\"channel\":\"protocol\",\"epoch\":1,\"elapsedMs\":0,\"wire\":{\"id\":1,\"result\":{\"synthetic\":true}}}\n"
	const transport = "{\"captureStatus\":\"complete\",\"cleanupComplete\":true,\"nativeLaunched\":true,\"clientEOF\":true,\"reaped\":true}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte(raw), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "stderr.raw"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "transport.json"), []byte(transport), 0o600))
	saved := tool.Object{}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		saved[name] = tool.DigestBytes([]byte(content))
	}
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "raw", "manifest.json"), tool.Object{
		"captureStatus": "complete",
		"files": tool.Object{
			"raw.jsonl":      tool.DigestBytes([]byte(raw)),
			"stderr.raw":     tool.DigestBytes(nil),
			"transport.json": tool.DigestBytes([]byte(transport)),
		},
		"recordingFormat": tool.Object{
			"name": "agent-mock-jsonl", "version": 1, "transport": "stdio-jsonl",
		},
		"native": tool.Object{
			"executable": "/synthetic/agent", "sha256": strings.Repeat("a", 64), "cwd": "/synthetic/project",
		},
	}))
	planHash, err := tool.FileDigest(filepath.Join(root, "plan.json"))
	require.NoError(t, err)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"provenance": provenance,
		"planSHA256": planHash,
		"files":      tool.Object{"raw.jsonl": tool.DigestBytes([]byte(raw))},
		"savedFiles": saved,
	}))
	return root
}

func TestPreparedHistoryRejectsUnboundFilesBeforeSession(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "external", map[string]string{"config/sessions/history.jsonl": "bound\n"})
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest))
	delete(manifest, "savedFiles")
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest))
	session := filepath.Join(t.TempDir(), "session")
	data, err := sessionTool(t, os.Environ(), "prepare", "--session-dir", session, "--recording", root).CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "unbound history")
	assert.NoFileExists(t, filepath.Join(session, "session.json"))
}

func TestPreparedMockUsesSelectedArtifactProvenance(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "consumer", map[string]string{
		"config/sessions/history.jsonl": "consumer\n",
	})
	session, env := preparedSessionWithArgs(t, "prepare", "--recording", root)
	cmd := sessionTool(t, env, "agent", "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	execution, _ := latestSessionExecution(t, session, "mock")
	assert.Equal(t, "consumer", execution["provenance"])
	assert.Equal(t, "mock", tool.Obj(execution["artifact"])["action"])
	data, err = os.ReadFile(filepath.Join(tool.Str(execution["config"]), "sessions", "history.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "consumer\n", string(data))
}

func TestPreparedVerifyRejectsConsumerMaterial(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "consumer", nil)
	session, env := preparedSessionWithArgs(t, "prepare", "--recording", root)
	data, err := sessionTool(t, env, "official", "verify", "--session-dir", session).CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "no applicable official verifier")
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	assert.Empty(t, tool.Array(index["executions"]))
}

func TestPreparedMaterialProvenanceConflictFailsBeforeReplay(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "official", nil)
	var plan tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "plan.json"), &plan))
	plan["driverProfile"] = "consumer"
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "plan.json"), plan))
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest))
	planHash, err := tool.FileDigest(filepath.Join(root, "plan.json"))
	require.NoError(t, err)
	manifest["planSHA256"] = planHash
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest))
	session := filepath.Join(t.TempDir(), "session")
	data, err := sessionTool(t, os.Environ(), "prepare", "--session-dir", session, "--recording", root).CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "provenance")
}

func preparedSessionWithArgs(t *testing.T, args ...string) (string, []string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	config := filepath.Join(base, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"CODEX_HOME=" + config,
		"GORACE=" + os.Getenv("GORACE"),
	}
	session := filepath.Join(base, "session")
	data, err := sessionTool(t, env, append(args, "--session-dir", session)...).CombinedOutput()
	require.NoError(t, err, "%s", data)
	t.Cleanup(func() {
		var descriptor tool.Object
		if tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor) == nil {
			_ = os.RemoveAll(tool.Str(descriptor["resourceRoot"]))
		}
	})
	return session, env
}
