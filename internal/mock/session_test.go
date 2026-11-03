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
	for i := range slices.Backward(rows) {
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
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
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
