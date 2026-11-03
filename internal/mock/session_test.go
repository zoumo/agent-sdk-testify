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

func latestSessionExecution(t *testing.T, session, command string) (object, string) {
	t.Helper()
	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	rows := array(index["executions"])
	for i := len(rows) - 1; i >= 0; i-- {
		row := obj(rows[i])
		if str(row["command"]) != command {
			continue
		}
		id := str(row["id"])
		var execution object
		require.NoError(t, readJSON(filepath.Join(session, "executions", id, "execution.json"), &execution))
		return execution, id
	}
	t.Fatalf("session has no %s execution", command)
	return nil, ""
}

func routeHostEnvironment(route object, extra map[string]string) []string {
	values := map[string]string{"PATH": os.Getenv("PATH"), "GORACE": "atexit_sleep_ms=0"}
	for key, value := range obj(route["env"]) {
		values[key] = str(value)
	}
	for key, value := range extra {
		values[key] = value
	}
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
		hashes[filepath.ToSlash(rel)] = digestBytes(data)
		return nil
	}))
	return hashes
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
	recordConsumerRequest(t, session, env, `{"id":1,"method":"ping"}`+"\n")
}

func recordConsumerRequest(t *testing.T, session string, env []string, request string) {
	t.Helper()
	agent, err := os.Executable()
	require.NoError(t, err)
	record := sessionTool(t, env, "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	record.Stdin = strings.NewReader(request)
	var stderr bytes.Buffer
	record.Stderr = &stderr
	out, err := record.Output()
	require.NoError(t, err, "%s", stderr.String())
	var requestObject object
	require.NoError(t, json.Unmarshal([]byte(request), &requestObject))
	assert.JSONEq(t, fmt.Sprintf(`{"id":%v,"result":{"synthetic":true}}`, requestObject["id"]), string(out))
}

func TestPreparedRecordHandoffSelectsLatestAndHonorsExplicitCapture(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerRequest(t, session, env, `{"id":1,"method":"ping"}`+"\n")
	recordConsumerRequest(t, session, env, `{"id":2,"method":"ping"}`+"\n")

	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	artifacts := array(index["artifacts"])
	require.Len(t, artifacts, 2)
	first, second := obj(artifacts[0]), obj(artifacts[1])
	require.NotEqual(t, str(first["path"]), str(second["path"]))
	require.Equal(t, str(second["id"]), str(index["latestCapture"]))

	runMock := func(expected, request string, extra ...string) object {
		args := append([]string{"mock", "--session-dir", session}, extra...)
		args = append(args, "--", "app-server", "--listen", "stdio://")
		cmd := sessionTool(t, env, args...)
		cmd.Stdin = strings.NewReader(request)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "%s", stderr.String())
		assert.JSONEq(t, expected, string(out))
		execution, _ := latestSessionExecution(t, session, "mock")
		return execution
	}

	latest := runMock(`{"id":2,"result":{"synthetic":true}}`, `{"id":2,"method":"ping"}`+"\n")
	require.Equal(t, str(second["path"]), str(obj(latest["artifact"])["path"]))

	explicit := runMock(`{"id":1,"result":{"synthetic":true}}`, `{"id":1,"method":"ping"}`+"\n", "--recording", str(first["path"]))
	require.Equal(t, str(first["path"]), str(obj(explicit["artifact"])["path"]))
	require.Equal(t, str(first["recordingSHA256"]), str(obj(explicit["artifact"])["recordingSHA256"]))

	var after object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &after))
	require.Equal(t, str(second["id"]), str(after["latestCapture"]))
	require.Len(t, array(after["artifacts"]), 2)
}

func TestPreparedMockConnectionDescriptorMatchesSelectedCapture(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	cmd := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	out, err := cmd.Output()
	require.NoError(t, err, "%s", out)

	execution, _ := latestSessionExecution(t, session, "mock")
	artifact := obj(execution["artifact"])
	connections := array(execution["connections"])
	require.Len(t, connections, 1)
	connection := obj(connections[0])
	require.Equal(t, "main", connection["id"])
	require.Equal(t, str(artifact["path"]), connection["material"])
	require.Equal(t, str(artifact["planSHA256"]), connection["planSHA256"])
	require.Equal(t, str(artifact["manifestSHA256"]), connection["manifestSHA256"])
	require.Equal(t, str(artifact["recordingSHA256"]), connection["recordingSHA256"])
	require.FileExists(t, str(connection["receipt"]))
	require.FileExists(t, str(connection["processReceipt"]))

	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	routeConnection := obj(array(obj(obj(descriptor["routes"])["mock"])["connections"])[0])
	require.Equal(t, str(routeConnection["command"]), connection["command"])
	require.Equal(t, array(routeConnection["args"]), array(connection["args"]))
	require.Equal(t, str(routeConnection["launcher"]), connection["launcher"])
	require.FileExists(t, str(connection["launcher"]))
}

func TestPreparedFailedRecordDoesNotReplaceLatestCapture(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)

	var before object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &before))
	require.Len(t, array(before["artifacts"]), 1)
	capture := obj(array(before["artifacts"])[0])
	require.Equal(t, str(capture["id"]), str(before["latestCapture"]))

	failed := sessionTool(t, env, "record", "--session-dir", session, "--cmd", "/usr/bin/false", "--", "app-server", "--listen", "stdio://")
	data, err := failed.CombinedOutput()
	require.Error(t, err, "%s", data)

	var after object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &after))
	require.Len(t, array(after["artifacts"]), 1)
	require.Equal(t, str(capture["id"]), str(after["latestCapture"]))

	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	launcher := str(obj(array(obj(obj(descriptor["routes"])["mock"])["connections"])[0])["launcher"])
	require.FileExists(t, launcher)
	cmd := sessionExecutable(t, env, launcher, "app-server", "--listen", "stdio://")
	cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(out))
}

func TestPreparedMockMultiConnectionReceiptsAndJoinAdmission(t *testing.T) {
	mainInput, source := builtinConnectionTranscript(t, "codex", "SDK-CASE-032", "main")
	probeInput, _ := builtinConnectionTranscript(t, "codex", "SDK-CASE-032", "probe")
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-032")

	run := func(connection, input string, extra ...string) ([]byte, error) {
		args := append([]string{"mock", "--session-dir", session, "--connection", connection}, extra...)
		args = append(args, "--", "app-server", "--listen", "stdio://")
		cmd := sessionTool(t, env, args...)
		cmd.Stdin = strings.NewReader(input)
		return cmd.CombinedOutput()
	}

	data, err := run("main", mainInput)
	require.NoError(t, err, "%s", data)
	execution, executionID := latestSessionExecution(t, session, "mock")
	connections := array(execution["connections"])
	require.Len(t, connections, 2)

	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	pending := false
	for _, value := range array(index["executions"]) {
		row := obj(value)
		if str(row["id"]) != executionID {
			continue
		}
		require.Equal(t, "open", row["status"])
		statuses := map[string]string{}
		for _, item := range array(row["connections"]) {
			connection := obj(item)
			statuses[str(connection["id"])] = str(connection["status"])
		}
		require.Equal(t, "passed", statuses["main"])
		require.Equal(t, "pending", statuses["probe"])
		pending = true
	}
	require.True(t, pending)

	data, err = run("main", mainInput)
	require.Error(t, err, "%s", data)
	require.Contains(t, string(data), "already started")

	recordingCopy := filepath.Join(t.TempDir(), "alternate-capture")
	require.NoError(t, copyTree(source, recordingCopy))
	for _, tc := range []struct {
		name  string
		extra []string
	}{
		{name: "recording", extra: []string{"--recording", recordingCopy}},
		{name: "timeout", extra: []string{"--timeout", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := run("probe", probeInput, tc.extra...)
			require.Error(t, err, "%s", data)
			require.Contains(t, string(data), "mock join")

			var current object
			require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &current))
			for _, value := range array(current["executions"]) {
				row := obj(value)
				if str(row["id"]) != executionID {
					continue
				}
				for _, item := range array(row["connections"]) {
					connection := obj(item)
					if str(connection["id"]) == "probe" {
						require.Equal(t, "pending", connection["status"])
					}
				}
			}
			var probeConnection object
			for _, item := range connections {
				connection := obj(item)
				if connection["id"] == "probe" {
					probeConnection = connection
				}
			}
			require.NotEmpty(t, probeConnection)
			require.NoFileExists(t, filepath.Join(filepath.Dir(str(probeConnection["receipt"])), ".probe.started"))
		})
	}

	data, err = run("probe", probeInput)
	require.NoError(t, err, "%s", data)

	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	for _, value := range array(index["executions"]) {
		row := obj(value)
		if str(row["id"]) != executionID {
			continue
		}
		require.Equal(t, "passed", row["status"])
		for _, item := range array(row["connections"]) {
			connection := obj(item)
			require.Equal(t, "passed", connection["status"])
		}
	}
	require.NoError(t, readJSON(filepath.Join(session, "executions", executionID, "execution.json"), &execution))
	artifact := obj(execution["artifact"])
	var manifest object
	require.NoError(t, readJSON(filepath.Join(str(artifact["path"]), "manifest.json"), &manifest))
	receipts := map[string]bool{}
	for _, item := range array(execution["connections"]) {
		connection := obj(item)
		require.Equal(t, str(artifact["path"]), connection["material"])
		require.Equal(t, str(artifact["planSHA256"]), connection["planSHA256"])
		require.Equal(t, str(artifact["manifestSHA256"]), connection["manifestSHA256"])
		switch connection["id"] {
		case "main":
			require.Equal(t, str(artifact["recordingSHA256"]), connection["recordingSHA256"])
		case "probe":
			require.Equal(t, obj(manifest["remotePhaseFiles"])["remote-probe/raw/raw.jsonl"], connection["recordingSHA256"])
		default:
			t.Fatalf("unexpected connection %v", connection["id"])
		}
		receipts[str(connection["receipt"])] = true
		require.FileExists(t, str(connection["receipt"]))
		require.FileExists(t, str(connection["processReceipt"]))
	}
	require.Len(t, receipts, 2)
}

func TestPreparedHalfOpenMockFailsFinishAndRetainsEvidence(t *testing.T) {
	mainInput, _ := builtinConnectionTranscript(t, "codex", "SDK-CASE-032", "main")
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-032")
	main := sessionTool(t, env, "mock", "--session-dir", session, "--connection", "main", "--", "app-server", "--listen", "stdio://")
	main.Stdin = strings.NewReader(mainInput)
	data, err := main.CombinedOutput()
	require.NoError(t, err, "%s", data)

	execution, executionID := latestSessionExecution(t, session, "mock")
	var mainConnection, probeConnection object
	for _, value := range array(execution["connections"]) {
		connection := obj(value)
		switch connection["id"] {
		case "main":
			mainConnection = connection
		case "probe":
			probeConnection = connection
		}
	}
	require.NotEmpty(t, mainConnection)
	require.NotEmpty(t, probeConnection)
	require.FileExists(t, str(mainConnection["receipt"]))
	require.NoFileExists(t, str(probeConnection["receipt"]))

	finish := sessionTool(t, env, "finish", "--session-dir", session)
	data, err = finish.Output()
	require.Error(t, err, "%s", data)
	var report object
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, "failed", report["status"])
	require.Equal(t, true, report["cleanupComplete"])
	rows := array(report["executions"])
	require.Len(t, rows, 1)
	require.Equal(t, executionID, obj(rows[0])["id"])
	require.Equal(t, "failed", obj(rows[0])["status"])

	require.FileExists(t, filepath.Join(session, "executions", executionID, "execution.json"))
	require.FileExists(t, str(mainConnection["receipt"]))
	require.FileExists(t, str(mainConnection["processReceipt"]))
	require.NoFileExists(t, str(probeConnection["receipt"]))
	require.NoFileExists(t, str(probeConnection["processReceipt"]))
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
	assert.NotEmpty(t, obj(manifest["savedFiles"])["config/sessions/synthetic-history.jsonl"])

	mock := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader(request)
	stderr.Reset()
	mock.Stderr = &stderr
	out, err := mock.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(out))
	execution, _ := latestSessionExecution(t, session, "mock")
	assert.Equal(t, "consumer", execution["provenance"])
	history := filepath.Join(str(execution["config"]), "sessions", "synthetic-history.jsonl")
	data, err := os.ReadFile(history)
	require.NoError(t, err)
	assert.Equal(t, "{\"type\":\"consumer-history\"}\n", string(data))

	finish := sessionTool(t, env, "finish", "--session-dir", session)
	data, err = finish.Output()
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

func TestPreparedFinishRejectsHistoryDrift(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	mock := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	data, err := mock.Output()
	require.NoError(t, err, "%s", data)
	execution, _ := latestSessionExecution(t, session, "mock")
	require.NoError(t, os.WriteFile(filepath.Join(str(execution["config"]), "sessions", "synthetic-history.jsonl"), []byte("changed\n"), 0o600))

	data, err = sessionTool(t, env, "finish", "--session-dir", session).Output()
	require.Error(t, err, "%s", data)
	var report object
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, "failed", report["status"])
	assert.Contains(t, str(report["error"]), "history hash mismatch")
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

func TestPreparedMockLauncherUsesIsolatedExecutionOverlays(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	resolvedSession, err := filepath.EvalSymlinks(session)
	require.NoError(t, err)
	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	route := obj(obj(descriptor["routes"])["mock"])
	routeWorkspace := str(route["workspace"])
	launcher := str(obj(array(route["connections"])[0])["launcher"])
	require.FileExists(t, launcher)
	routeRoot := filepath.Join(str(descriptor["resourceRoot"]), "routes", "mock")
	templateRoot := filepath.Join(str(descriptor["resourceRoot"]), "templates", "mock")
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

	run := func(label string) (object, string, map[string]string) {
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

		observed := map[string]string{}
		data, err := os.ReadFile(observe)
		require.NoError(t, err)
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			key, value, ok := strings.Cut(line, "=")
			require.True(t, ok, line)
			observed[key] = value
		}
		execution, id := latestSessionExecution(t, session, "mock")
		return execution, id, observed
	}

	first, firstID, firstObserved := run("first")
	firstWorkspace := str(first["workspace"])
	firstRoot := filepath.Join(resolvedSession, "executions", firstID)
	require.NotEqual(t, routeWorkspace, firstWorkspace)
	require.True(t, strings.HasPrefix(firstWorkspace, firstRoot+string(filepath.Separator)))
	require.Equal(t, str(first["home"]), str(obj(first["env"])["HOME"]))
	require.Equal(t, str(first["config"]), str(obj(first["env"])["CODEX_HOME"]))
	require.Equal(t, str(first["tmp"]), str(obj(first["env"])["TMPDIR"]))
	require.NotEmpty(t, firstObserved)
	assertRouteSlotsAtTemplate()
	firstConnection := obj(array(first["connections"])[0])
	var firstProcess object
	require.NoError(t, readJSON(str(firstConnection["processReceipt"]), &firstProcess))
	require.Equal(t, firstWorkspace, firstProcess["cwd"])
	require.NoError(t, os.WriteFile(filepath.Join(firstWorkspace, "run-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(str(first["home"]), "run-first.txt"), []byte("first\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(str(first["config"]), "run-first.txt"), []byte("first\n"), 0o600))
	firstHashes := workspaceHashes(t, firstWorkspace)
	require.NoFileExists(t, filepath.Join(templateRoot, "workspace", "run-first.txt"))
	require.NoFileExists(t, filepath.Join(templateRoot, "home", "run-first.txt"))
	require.NoFileExists(t, filepath.Join(templateRoot, "config", "run-first.txt"))

	second, secondID, secondObserved := run("second")
	secondWorkspace := str(second["workspace"])
	secondRoot := filepath.Join(resolvedSession, "executions", secondID)
	require.NotEqual(t, firstID, secondID)
	require.NotEqual(t, firstWorkspace, secondWorkspace)
	require.True(t, strings.HasPrefix(secondWorkspace, secondRoot+string(filepath.Separator)))
	require.NotEmpty(t, secondObserved)
	assertRouteSlotsAtTemplate()
	secondConnection := obj(array(second["connections"])[0])
	var secondProcess object
	require.NoError(t, readJSON(str(secondConnection["processReceipt"]), &secondProcess))
	require.Equal(t, secondWorkspace, secondProcess["cwd"])
	require.FileExists(t, filepath.Join(secondWorkspace, "fixture.txt"))
	require.NoFileExists(t, filepath.Join(secondWorkspace, "run-first.txt"))
	require.NoFileExists(t, filepath.Join(str(second["home"]), "run-first.txt"))
	require.NoFileExists(t, filepath.Join(str(second["config"]), "run-first.txt"))
	require.NoFileExists(t, filepath.Join(firstWorkspace, "run-second.txt"))
	require.Equal(t, firstHashes, workspaceHashes(t, firstWorkspace))

	data, err := sessionTool(t, env, "finish", "--session-dir", session).CombinedOutput()
	require.NoError(t, err, "%s", data)
}

func resolvedSymlink(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
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

func TestConsumerPlanAgentTypeCannotBeOverriddenByLegacyProvider(t *testing.T) {
	for _, legacy := range []string{"claude", "unknown-legacy-provider"} {
		t.Run(legacy, func(t *testing.T) {
			base := t.TempDir()
			plan := filepath.Join(base, "plan.json")
			require.NoError(t, writeJSON(plan, object{
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
				"GORACE=atexit_sleep_ms=0",
			}, "prepare", "--agent", "codex", "--plan", plan, "--session-dir", session)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			assert.Contains(t, string(data), "agentType")
			assert.Contains(t, string(data), "provider")
			require.NoDirExists(t, session)
		})
	}
}

type syntheticMaterialChange func(*testing.T, string, object)

func syntheticCaseMaterial(t *testing.T, caseID, provenance string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	profile := "continuation"
	if provenance == "consumer" {
		profile = "consumer"
	}
	require.NoError(t, writeJSON(filepath.Join(root, "plan.json"), object{
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
	saved := object{}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		saved[name] = digestBytes([]byte(content))
	}
	require.NoError(t, writeJSON(filepath.Join(root, "raw", "manifest.json"), object{
		"captureStatus": "complete",
		"files": object{
			"raw.jsonl":      digestBytes([]byte(raw)),
			"stderr.raw":     digestBytes(nil),
			"transport.json": digestBytes([]byte(transport)),
		},
		"recordingFormat": object{
			"name": "agent-mock-jsonl", "version": 1, "transport": "stdio-jsonl",
		},
		"native": object{
			"executable": "/synthetic/agent", "sha256": strings.Repeat("a", 64), "cwd": "/synthetic/project",
		},
	}))
	planHash, err := fileDigest(filepath.Join(root, "plan.json"))
	require.NoError(t, err)
	require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), object{
		"provenance": provenance,
		"planSHA256": planHash,
		"files":      object{"raw.jsonl": digestBytes([]byte(raw))},
		"savedFiles": saved,
	}))
	return root
}

func TestPreparedHistoryRejectsUnboundMissingDriftLinksAndEscapes(t *testing.T) {
	const history = "config/sessions/history.jsonl"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		mutate  syntheticMaterialChange
		message string
	}{
		{
			name:    "unbound",
			files:   map[string]string{history: "bound\n"},
			message: "unbound history",
			mutate: func(t *testing.T, _ string, manifest object) {
				delete(obj(manifest["savedFiles"]), history)
			},
		},
		{
			name:    "missing",
			files:   map[string]string{history: "bound\n"},
			message: "history.jsonl",
			mutate: func(t *testing.T, root string, _ object) {
				require.NoError(t, os.Remove(filepath.Join(root, history)))
			},
		},
		{
			name:    "drift",
			files:   map[string]string{history: "bound\n"},
			message: "hash mismatch",
			mutate: func(t *testing.T, root string, _ object) {
				require.NoError(t, os.WriteFile(filepath.Join(root, history), []byte("changed\n"), 0o600))
			},
		},
		{
			name:    "parent symlink",
			files:   map[string]string{"config/sessions/target/history.jsonl": "bound\n"},
			message: "symlink",
			mutate: func(t *testing.T, root string, _ object) {
				target := filepath.Join(root, "config", "sessions", "target")
				linked := filepath.Join(root, "config", "sessions", "linked")
				require.NoError(t, os.Rename(target, linked))
				require.NoError(t, os.Symlink(linked, target))
			},
		},
		{
			name:    "path escape",
			files:   map[string]string{history: "bound\n"},
			message: "escapes material",
			mutate: func(t *testing.T, root string, manifest object) {
				outside := filepath.Join(filepath.Dir(root), "outside.jsonl")
				require.NoError(t, os.WriteFile(outside, []byte("outside\n"), 0o600))
				obj(manifest["savedFiles"])["../outside.jsonl"] = digestBytes([]byte("outside\n"))
			},
		},
		{
			name:    "unapproved subtree",
			files:   map[string]string{"config/auth.json": "secret\n"},
			message: "outside allowed history",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := syntheticCaseMaterial(t, "SDK-CASE-001", "external", tc.files)
			if tc.mutate != nil {
				var manifest object
				require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
				tc.mutate(t, root, manifest)
				require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
			}
			session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-001")
			cmd := sessionTool(t, env, "mock", "--session-dir", session, "--recording", root, "--", "app-server", "--listen", "stdio://")
			cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			assert.Contains(t, string(data), tc.message)
			var index object
			require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
			assert.Empty(t, array(index["executions"]))
		})
	}
}

func TestPreparedMockUsesSelectedArtifactProvenance(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "consumer", map[string]string{
		"config/sessions/history.jsonl": "consumer\n",
	})
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-001")
	cmd := sessionTool(t, env, "mock", "--session-dir", session, "--recording", root, "--", "app-server", "--listen", "stdio://")
	cmd.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	execution, _ := latestSessionExecution(t, session, "mock")
	assert.Equal(t, "consumer", execution["provenance"])
	assert.Equal(t, "external", obj(execution["artifact"])["action"])
	data, err = os.ReadFile(filepath.Join(str(execution["config"]), "sessions", "history.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "consumer\n", string(data))
}

func TestPreparedVerifyRejectsConsumerMaterial(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "consumer", nil)
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-001")
	data, err := sessionTool(t, env, "verify", "--session-dir", session, "--recording", root).CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "consumer material cannot be verified")
	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	assert.Empty(t, array(index["executions"]))
}

func TestPreparedMaterialProvenanceConflictFailsBeforeReplay(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "official", nil)
	var plan object
	require.NoError(t, readJSON(filepath.Join(root, "plan.json"), &plan))
	plan["driverProfile"] = "consumer"
	require.NoError(t, writeJSON(filepath.Join(root, "plan.json"), plan))
	var manifest object
	require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
	planHash, err := fileDigest(filepath.Join(root, "plan.json"))
	require.NoError(t, err)
	manifest["planSHA256"] = planHash
	require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-001")
	data, err := sessionTool(t, env, "mock", "--session-dir", session, "--recording", root, "--", "app-server", "--listen", "stdio://").CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "provenance")
}

func TestPreparedMaterialMissingProvenanceFailsBeforeReplay(t *testing.T) {
	root := syntheticCaseMaterial(t, "SDK-CASE-001", "external", nil)
	var manifest object
	require.NoError(t, readJSON(filepath.Join(root, "manifest.json"), &manifest))
	delete(manifest, "provenance")
	require.NoError(t, writeJSON(filepath.Join(root, "manifest.json"), manifest))
	session, env := preparedSessionWithArgs(t, "prepare", "--agent", "codex", "--case", "SDK-CASE-001")
	data, err := sessionTool(t, env, "mock", "--session-dir", session, "--recording", root, "--", "app-server", "--listen", "stdio://").CombinedOutput()
	require.Error(t, err, "%s", data)
	assert.Contains(t, string(data), "provenance is missing")
	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	assert.Empty(t, array(index["executions"]))
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
		"GORACE=atexit_sleep_ms=0",
	}
	session := filepath.Join(base, "session")
	data, err := sessionTool(t, env, append(args, "--session-dir", session)...).CombinedOutput()
	require.NoError(t, err, "%s", data)
	t.Cleanup(func() {
		var descriptor object
		if readJSON(filepath.Join(session, "session.json"), &descriptor) == nil {
			_ = os.RemoveAll(str(descriptor["resourceRoot"]))
		}
	})
	return session, env
}
