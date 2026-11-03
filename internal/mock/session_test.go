package mock

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mockTestAgent() int {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("synthetic-consumer-native")
		return 0
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

func consumerSession(t *testing.T) (string, []string) {
	t.Helper()
	base := t.TempDir()
	plan := filepath.Join(base, "plan.json")
	require.NoError(t, writeJSON(plan, object{
		"version":       1,
		"agentType":     "codex",
		"caseId":        "consumer-session",
		"driverProfile": "consumer",
		"project":       filepath.Join(base, "original-project"),
		"model":         "synthetic-model",
		"cliVersion":    "synthetic-consumer-native",
		"scenario": object{
			"files":      object{"fixture.txt": "fixture\n"},
			"parameters": object{"keep": 9007199254740993},
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
		"GORACE=atexit_sleep_ms=0",
	}
	session := filepath.Join(base, "session")
	prepare := sessionTool(t, env, "prepare", "--plan", plan, "--session-dir", session)
	data, err := prepare.CombinedOutput()
	require.NoError(t, err, "%s", data)
	t.Cleanup(func() {
		var descriptor object
		if readJSON(filepath.Join(session, "session.json"), &descriptor) == nil {
			_ = os.RemoveAll(str(descriptor["resourceRoot"]))
		}
	})
	return session, env
}

func recordConsumerCapture(t *testing.T, session string, env []string) {
	t.Helper()
	agent, err := os.Executable()
	require.NoError(t, err)
	const request = `{"id":1,"method":"ping"}` + "\n"
	record := sessionTool(t, env, "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	record.Stdin = strings.NewReader(request)
	var stderr bytes.Buffer
	record.Stderr = &stderr
	out, err := record.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(out))
}

func TestPreparedConsumerRecordMockFinish(t *testing.T) {
	session, env := consumerSession(t)
	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	source := obj(descriptor["source"])
	require.Equal(t, "plan", source["kind"])
	sourcePath := str(source["path"])
	require.True(t, filepath.IsAbs(sourcePath))
	resolvedSession, err := filepath.EvalSymlinks(session)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(sourcePath, resolvedSession+string(filepath.Separator)))
	require.FileExists(t, sourcePath)
	workspace := str(descriptor["workspace"])
	require.FileExists(t, filepath.Join(workspace, "fixture.txt"))
	require.DirExists(t, str(descriptor["home"]))
	require.DirExists(t, str(descriptor["config"]))

	recordConsumerCapture(t, session, env)
	const request = `{"id":1,"method":"ping"}` + "\n"
	var stderr bytes.Buffer

	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	artifacts := array(index["artifacts"])
	require.Len(t, artifacts, 1)
	artifact := obj(artifacts[0])
	assert.Equal(t, "record", artifact["action"])
	assert.Equal(t, "consumer", artifact["provenance"])
	root := str(artifact["path"])
	var manifest, plan object
	require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
	require.NoError(t, readJSON(filepath.Join(root, "plan.json"), &plan))
	assert.Equal(t, "consumer", manifest["provenance"])
	assert.Equal(t, "captured", manifest["status"])
	assert.Equal(t, "complete", manifest["captureStatus"])
	assert.Equal(t, "consumer-session", plan["caseId"])

	mock := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader(request)
	stderr.Reset()
	mock.Stderr = &stderr
	out, err := mock.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(out))

	finish := sessionTool(t, env, "finish", "--session-dir", session)
	data, err := finish.Output()
	require.NoError(t, err, "%s", data)
	var report object
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, "passed", report["status"])
	assert.Equal(t, true, report["cleanupComplete"])
	assert.Len(t, array(report["executions"]), 2)
	require.NoFileExists(t, str(descriptor["resourceRoot"]))
	require.FileExists(t, sourcePath)

	blocked := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	data, err = blocked.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "finished")

	agent, err := os.Executable()
	require.NoError(t, err)
	blocked = sessionTool(t, env, "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	data, err = blocked.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "finished")

	blocked = sessionTool(t, env, "verify", "--session-dir", session)
	data, err = blocked.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(data), "finished")
}

func TestPreparedMockFailureAndFinishRetry(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	mock := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader("{\"id\":1,\"method\":\"wrong\"}\n")
	data, err := mock.CombinedOutput()
	require.Error(t, err, "%s", data)

	for range 2 {
		finish := sessionTool(t, env, "finish", "--session-dir", session)
		data, err := finish.Output()
		require.Error(t, err, "%s", data)
		var report object
		require.NoError(t, json.Unmarshal(data, &report))
		assert.Equal(t, "failed", report["status"], string(data))
		assert.Equal(t, true, report["cleanupComplete"], string(data))
	}
}

func TestPreparedFinishWithoutExecutionsIsAbandoned(t *testing.T) {
	session, env := consumerSession(t)
	finish := sessionTool(t, env, "finish", "--session-dir", session)
	data, err := finish.Output()
	require.NoError(t, err, "%s", data)
	var report object
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, "abandoned", report["status"])
	assert.Equal(t, true, report["cleanupComplete"])
	assert.Empty(t, array(report["executions"]))
}

func TestPreparedBuiltinMockFinishRetry(t *testing.T) {
	input, _ := builtinTranscript(t, "codex", "SDK-CASE-001")
	base := t.TempDir()
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(base, "home"),
		"CODEX_HOME=" + filepath.Join(base, "config"),
		"GORACE=atexit_sleep_ms=0",
	}
	session := filepath.Join(base, "session")
	data, err := sessionTool(t, env, "prepare", "--agent", "codex", "--case", "SDK-CASE-001", "--session-dir", session).CombinedOutput()
	require.NoError(t, err, "%s", data)
	mock := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader(input)
	var stderr bytes.Buffer
	mock.Stderr = &stderr
	_, err = mock.Output()
	require.NoError(t, err, "%s", stderr.String())

	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	for range 2 {
		data, err = sessionTool(t, env, "finish", "--session-dir", session).Output()
		require.NoError(t, err, "%s", data)
		var report object
		require.NoError(t, json.Unmarshal(data, &report))
		assert.Equal(t, "passed", report["status"])
		assert.Equal(t, true, report["cleanupComplete"])
		require.NoDirExists(t, str(descriptor["resourceRoot"]))
	}
}

func TestPreparedSourceTamperRejected(t *testing.T) {
	session, env := consumerSession(t)
	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	sourcePath := str(obj(descriptor["source"])["path"])
	require.NoError(t, os.WriteFile(sourcePath, []byte("{}\n"), 0o600))
	data, err := sessionTool(t, env, "mock", "--session-dir", session, "--version").CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "session source changed after prepare")
}

func TestPreparedSessionTamperRejected(t *testing.T) {
	session, env := consumerSession(t)
	path := filepath.Join(session, "session.json")
	var descriptor object
	require.NoError(t, readJSON(path, &descriptor))
	descriptor["toolVersion"] = "tampered"
	require.NoError(t, writeJSON(path, descriptor))
	data, err := sessionTool(t, env, "mock", "--session-dir", session, "--version").CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "session descriptor changed after prepare")
}

func TestPreparedMockLauncherRemainsAcrossExecutions(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	route := obj(obj(descriptor["routes"])["mock"])
	connections := array(route["connections"])
	require.NotEmpty(t, connections)
	launcher := str(obj(connections[0])["launcher"])
	require.FileExists(t, launcher)

	const request = `{"id":1,"method":"ping"}` + "\n"
	for range 2 {
		cmd := sessionExecutable(t, env, launcher, "app-server", "--listen", "stdio://")
		cmd.Stdin = strings.NewReader(request)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "%s", stderr.String())
		assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(out))
		require.FileExists(t, launcher)
	}
	data, err := sessionTool(t, env, "finish", "--session-dir", session).CombinedOutput()
	require.NoError(t, err, "%s", data)
}

func TestPreparedExecutionTamperRejectedByFinish(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	rows := array(index["executions"])
	require.NotEmpty(t, rows)
	path := filepath.Join(session, "executions", str(obj(rows[len(rows)-1])["id"]), "execution.json")
	var execution object
	require.NoError(t, readJSON(path, &execution))
	execution["args"] = append(array(execution["args"]), "--tampered")
	require.NoError(t, writeJSON(path, execution))

	data, err := sessionTool(t, env, "finish", "--session-dir", session).Output()
	require.Error(t, err, "%s", data)
	var report object
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, "failed", report["status"])
	assert.Contains(t, string(data), "execution descriptor changed after creation")
}

func TestPreparedConsumerRecordFailure(t *testing.T) {
	session, env := consumerSession(t)
	record := sessionTool(t, env, "record", "--session-dir", session, "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
	data, err := record.CombinedOutput()
	require.Error(t, err, "%s", data)

	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	assert.Empty(t, array(index["artifacts"]))

	finish := sessionTool(t, env, "finish", "--session-dir", session)
	data, err = finish.Output()
	require.Error(t, err, "%s", data)
	var report object
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, "failed", report["status"])
	assert.Equal(t, true, report["cleanupComplete"])
}
