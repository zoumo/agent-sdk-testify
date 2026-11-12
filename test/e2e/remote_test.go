package conformance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

const remoteReply = `{ "id":1, "result":{"synthetic":true,"large":9007199254740993,"exponent":1e+09} }`

type controlledRemote struct {
	server       *httptest.Server
	token        string
	tokenFile    string
	requests     chan string
	disconnected chan struct{}
}

func newControlledRemote(t *testing.T) *controlledRemote {
	t.Helper()
	r := &controlledRemote{
		token:     "synthetic-private-websocket-bearer-" + filepath.Base(t.TempDir()),
		tokenFile: filepath.Join(t.TempDir(), "private-bearer"),
		requests:  make(chan string, 32), disconnected: make(chan struct{}, 32),
	}
	require.NoError(t, os.WriteFile(r.tokenFile, []byte(r.token+"\n"), 0o600))
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/health" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if req.Header.Get("Authorization") != "Bearer "+r.token {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, req, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow(); r.disconnected <- struct{}{} }()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			r.requests <- string(data)
			if kind != websocket.MessageText {
				return
			}
			payload, replyKind := remoteReply, websocket.MessageText
			switch req.URL.Path {
			case "/binary":
				replyKind = websocket.MessageBinary
			case "/multiline":
				payload = "{\n\"id\":1}"
			case "/oversized":
				payload = `{"text":"` + strings.Repeat("x", 4096) + `"}`
			}
			// Two writes in one WebSocket message exercise message reassembly.
			writer, err := conn.Writer(ctx, replyKind)
			if err != nil {
				return
			}
			if _, err = io.WriteString(writer, payload[:len(payload)/2]); err != nil {
				return
			}
			if _, err = io.WriteString(writer, payload[len(payload)/2:]); err != nil {
				return
			}
			if err = writer.Close(); err != nil || req.URL.Path == "/early" {
				return
			}
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *controlledRemote) args(path string) []string {
	return []string{"--url", "ws" + strings.TrimPrefix(r.server.URL, "http") + path, "--token-file", r.tokenFile}
}

func (r *controlledRemote) assertAlive(t *testing.T) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.server.URL+"/health", http.NoBody)
	require.NoError(t, err)
	response, err := r.server.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusNoContent, response.StatusCode)
}

// Drive the installed process interactively: client EOF follows receipt of the
// response, as with an SDK, so EOF cannot discard an in-flight response.
func recordRemote(t *testing.T, env []string, fixtureDir string, remote *controlledRemote, mode string) (commandResult, object) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	require.NoError(t, r.SetReadDeadline(time.Now().Add(10*time.Second)))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	path := "/" + mode
	args := append([]string{"agent", "record", "--fixture-dir", fixtureDir, "--result-fd", "3"}, remote.args(path)...)
	if mode == "deadline" {
		args = append(args, "--timeout", "1s")
	}
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Env, cmd.ExtraFiles = env, []*os.File{w}
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
	reader := bufio.NewReader(r)
	readResult := func() object {
		line, err := reader.ReadBytes('\n')
		require.NoError(t, err)
		var value object
		require.NoError(t, json.Unmarshal(line, &value))
		return value
	}
	started := readResult()
	require.Equal(t, "running", started["status"])
	_, err = io.WriteString(stdin, requestOne)
	require.NoError(t, err)
	var response []byte
	if mode == "success" || mode == "cancel" || mode == "deadline" || mode == "early" {
		response, err = bufio.NewReader(stdout).ReadBytes('\n')
		require.NoError(t, err)
		require.Equal(t, remoteReply+"\n", string(response)) //nolint:testifylint // Exact message bytes and JSONL framing are contractual.
	}
	switch mode {
	case "success":
		require.NoError(t, stdin.Close())
	case "cancel":
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	}
	remaining, err := io.ReadAll(stdout)
	require.NoError(t, err)
	waitErr := cmd.Wait()
	if mode == "success" {
		require.NoError(t, waitErr, stderr.String())
	} else {
		require.Error(t, waitErr, stderr.String())
		require.Equal(t, 1, cmd.ProcessState.ExitCode(), stderr.String())
	}
	completed := readResult()
	require.Equal(t, started["executionId"], completed["executionId"])
	require.Equal(t, completed, readJSON(t, resolveFixtureReference(t, resultPackageRoot(t, fixtureDir, completed), str(completed["result"]))))
	require.Equal(t, digestFile(t, resolveFixtureReference(t, resultPackageRoot(t, fixtureDir, completed), str(completed["execution"]))), completed["executionSHA256"])
	select {
	case <-remote.disconnected:
	case <-ctx.Done():
		t.Fatal("record left its connection open")
	}
	select {
	case request := <-remote.requests:
		require.Equal(t, strings.TrimSuffix(requestOne, "\n"), request)
	default:
		t.Fatal("service did not receive the client frame")
	}
	// No implicit shutdown RPC or additional protocol request may reach the service.
	require.Empty(t, remote.requests)
	remote.assertAlive(t)
	return commandResult{stdout: string(append(response, remaining...)), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}, completed
}

func TestAgentRemoteRecordLifecycle(t *testing.T) {
	root := t.TempDir()
	_, drivers, env := isolatedCommands(t, root, "codex")
	marker := filepath.Join(root, "native-or-driver-launched")
	for _, name := range []string{"codex", "claude", "agent-sdk-testify-codex"} {
		require.NoError(t, os.WriteFile(filepath.Join(drivers, name), []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\nexit 42\n"), 0o700))
	}
	remote := newControlledRemote(t)
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "remote-record"))
	result, reference := recordRemote(t, env, fixtureDir, remote, "success")
	require.Zero(t, result.code, result.stderr)
	require.Equal(t, "passed", reference["status"])
	receipt := readJSON(t, resolveFixtureReference(t, resultPackageRoot(t, fixtureDir, reference), str(reference["receipt"])))
	require.Equal(t, "complete", receipt["captureStatus"])
	require.Equal(t, "remote-websocket", receipt["target"])
	for _, field := range []string{"remoteConnected", "remoteClosed", "clientEOF", "cleanupComplete"} {
		require.Equal(t, true, receipt[field], field)
	}
	for _, field := range []string{"nativeLaunched", "reaped"} {
		require.Equal(t, false, receipt[field], field)
	}
	require.Nil(t, receipt["nativeExitCode"])
	require.NotContains(t, receipt, "stdinEOFAction")
	material := obj(reference["material"])
	require.Equal(t, currentMaterialID(t, fixtureDir), material["id"])
	artifact := artifactByID(t, fixtureDir, str(material["id"]))
	materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
	assertFileHashes(t, materialRoot, obj(artifact["files"]))
	raw := string(readFile(t, filepath.Join(materialRoot, "raw", "raw.jsonl")))
	require.Contains(t, raw, `"large":9007199254740993,"exponent":1e+09`)
	manifest := readJSON(t, filepath.Join(materialRoot, "raw", "manifest.json"))
	require.Equal(t, "websocket", obj(manifest["native"])["transport"])
	require.Empty(t, obj(manifest["native"])["executable"])
	require.Empty(t, obj(manifest["native"])["sha256"])
	sealed := readAllFiles(t, materialRoot)
	exported := filepath.Join(root, "exported")
	mustRun(t, env, root, "", "export", "--fixture-dir", fixtureDir, "--output", exported)
	imported := prepareFixture(t, env, "--recording", exported)
	replay := runPreparedMock(t, env, imported, "", requestOne)
	require.JSONEq(t, remoteReply, replay.stdout)
	for _, directory := range []string{fixtureDir, exported, imported} {
		evidence := string(readAllFiles(t, directory))
		for _, secret := range []string{remote.token, remote.tokenFile, remote.server.URL, strings.TrimPrefix(remote.server.URL, "http")} {
			require.NotContains(t, evidence, secret)
		}
	}
	require.Equal(t, sealed, readAllFiles(t, materialRoot))
	require.NoFileExists(t, marker)
	require.Equal(t, remote.token+"\n", string(readFile(t, remote.tokenFile)))
	remote.assertAlive(t)
}

func TestAgentRemoteFailureKeepsCurrentAndService(t *testing.T) {
	for _, mode := range []string{"early", "cancel", "deadline", "binary", "multiline", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			agent, _, env := isolatedCommands(t, root, "codex")
			remote := newControlledRemote(t)
			fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "remote-failure"), "--max-frame-bytes", "1024")
			recordConsumer(t, env, fixtureDir, agent, requestOne)
			current := readFile(t, filepath.Join(fixtureDir, "current.json"))
			result, reference := recordRemote(t, env, fixtureDir, remote, mode)
			require.Equal(t, 1, result.code)
			require.Equal(t, "failed", reference["status"])
			require.NotContains(t, reference, "material")
			require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
			receiptPath := resolveFixtureReference(t, resultPackageRoot(t, fixtureDir, reference), str(reference["receipt"]))
			receipt := readJSON(t, receiptPath)
			require.Equal(t, "failed", receipt["captureStatus"])
			require.Equal(t, true, receipt["remoteConnected"])
			require.Equal(t, true, receipt["remoteClosed"])
			require.Equal(t, true, receipt["cleanupComplete"])
			require.Equal(t, false, receipt["nativeLaunched"])
			require.Equal(t, false, receipt["reaped"])
			if mode == "cancel" || mode == "deadline" {
				reason := "canceled"
				if mode == "deadline" {
					reason = "deadline"
				}
				require.Equal(t, reason, receipt["reason"])
			}
			if mode == "early" {
				require.Contains(t, string(readFile(t, filepath.Join(filepath.Dir(receiptPath), "raw.jsonl"))), `"direction":"s2c"`)
			}
			// The same service and fixture accept a fresh connection after failure.
			success, _ := recordRemote(t, env, fixtureDir, remote, "success")
			require.Zero(t, success.code, success.stderr)
			remote.assertAlive(t)
		})
	}
}

func TestAgentRemotePreflightAndAuthentication(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	remote := newControlledRemote(t)
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "remote-preflight"))
	for _, extra := range [][]string{
		{"--url", remote.args("/")[1]},
		{"--token-file", remote.tokenFile},
		{"--url", "https://localhost/", "--token-file", remote.tokenFile},
		{"--url", "ws://user:secret@localhost/", "--token-file", remote.tokenFile},
		{"--url", "ws://localhost/?token=secret", "--token-file", remote.tokenFile},
		{"--url", remote.args("/")[1], "--token-file", filepath.Join(root, "missing")},
		append(remote.args("/"), "--cmd", agent),
		append(remote.args("/"), "--cmd="),
		append(remote.args("/"), "--", "app-server"),
		append(remote.args("/"), "--"),
	} {
		before := readAllFiles(t, fixtureDir)
		result := runTool(t, env, root, "", append([]string{"agent", "record", "--fixture-dir", fixtureDir}, extra...)...)
		require.Equal(t, 1, result.code, extra)
		require.Equal(t, before, readAllFiles(t, fixtureDir))
	}
	for _, token := range []string{"", "invalid token"} {
		require.NoError(t, os.WriteFile(remote.tokenFile, []byte(token), 0o600))
		before := readAllFiles(t, fixtureDir)
		result := runTool(t, env, root, "", append([]string{"agent", "record", "--fixture-dir", fixtureDir}, remote.args("/")...)...)
		require.Equal(t, 1, result.code)
		require.Equal(t, before, readAllFiles(t, fixtureDir))
	}
	require.NoError(t, os.WriteFile(remote.tokenFile, []byte(remote.token), 0o600))
	require.NoError(t, os.Chmod(remote.tokenFile, 0o644))
	before := readAllFiles(t, fixtureDir)
	badPermissions := runTool(t, env, root, "", append([]string{"agent", "record", "--fixture-dir", fixtureDir}, remote.args("/")...)...)
	require.Equal(t, 1, badPermissions.code)
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	require.NoError(t, os.Chmod(remote.tokenFile, 0o600))
	// An actual authentication rejection is an execution failure with a receipt.
	require.NoError(t, os.WriteFile(remote.tokenFile, []byte("wrong-private-bearer"), 0o600))
	failed, reference := runAgentResult(t, env, fixtureDir, "", "record", remote.args("/")...)
	require.Equal(t, 1, failed.code)
	require.Equal(t, "failed", reference["status"])
	receipt := readJSON(t, resolveFixtureReference(t, resultPackageRoot(t, fixtureDir, reference), str(reference["receipt"])))
	require.NotEqual(t, true, receipt["remoteConnected"])
	require.Equal(t, true, receipt["cleanupComplete"])
	require.Equal(t, false, receipt["nativeLaunched"])
	require.NotContains(t, failed.stderr, "wrong-private-bearer")
	require.NotContains(t, string(readAllFiles(t, fixtureDir)), "wrong-private-bearer")
	remote.assertAlive(t)
}

func TestRemoteTokenStaysOutsideFixtureEvidence(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	remote := newControlledRemote(t)
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "runtime-token"))
	token := []byte(remote.token + "\n")
	path := filepath.Join(fixtureDir, "source", "remote-token.private")
	require.NoError(t, os.WriteFile(path, token, 0o600))
	before := readAllFiles(t, fixtureDir)
	rejected := runTool(t, env, root, "", "agent", "record", "--fixture-dir", fixtureDir, "--url", remote.args("/")[1], "--token-file", path)
	require.Equal(t, 1, rejected.code)
	require.Contains(t, rejected.stderr, "token file must be outside fixture evidence")
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	require.NoError(t, os.Remove(path))
	remote.tokenFile = filepath.Join(fixtureDir, "runtime", "remote-token.private")
	require.NoError(t, os.WriteFile(remote.tokenFile, token, 0o600))
	result, reference := recordRemote(t, env, fixtureDir, remote, "success")
	require.Zero(t, result.code, result.stderr)
	require.Equal(t, "passed", reference["status"])
	exported := filepath.Join(root, "exported")
	mustRun(t, env, root, "", "export", "--fixture-dir", fixtureDir, "--output", exported)
	require.NotContains(t, string(readAllFiles(t, exported)), remote.token)
}

func TestAgentRemoteEarlyCloseBoundsBackpressuredDrain(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	remote := newControlledRemote(t)
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "remote-backpressure"))
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	// Fill the caller's stdout before starting capture. The bridge can observe
	// the small response and remote close while journal output remains blocked.
	fd := int(w.Fd())
	require.NoError(t, syscall.SetNonblock(fd, true))
	for {
		_, err := syscall.Write(fd, bytes.Repeat([]byte("x"), 4096))
		if err != nil {
			require.ErrorIs(t, err, syscall.EAGAIN)
			break
		}
	}
	resultPath := filepath.Join(root, "results.jsonl")
	resultFile, err := os.Create(resultPath)
	require.NoError(t, err)
	defer resultFile.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	args := append([]string{"agent", "record", "--fixture-dir", fixtureDir, "--result-fd", "3"}, remote.args("/early")...)
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Env, cmd.ExtraFiles, cmd.Stdout = env, []*os.File{resultFile}, w
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer stdin.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now()
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	_, err = io.WriteString(stdin, requestOne)
	require.NoError(t, err)
	require.Error(t, cmd.Wait(), stderr.String())
	require.Equal(t, 1, cmd.ProcessState.ExitCode(), stderr.String())
	require.Less(t, time.Since(started), 5*time.Second, "early remote failure waited indefinitely for stdout")
	require.NoError(t, resultFile.Close())
	lines := strings.Split(strings.TrimSpace(string(readFile(t, resultPath))), "\n")
	require.Len(t, lines, 2)
	var reference object
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &reference))
	require.Equal(t, "failed", reference["status"])
	receiptPath := resolveFixtureReference(t, resultPackageRoot(t, fixtureDir, reference), str(reference["receipt"]))
	receipt := readJSON(t, receiptPath)
	require.Contains(t, str(receipt["error"]), "drain exceeded cleanup deadline")
	require.Equal(t, true, receipt["cleanupComplete"])
	require.Equal(t, true, receipt["remoteClosed"])
	require.Equal(t, false, receipt["nativeLaunched"])
	require.Contains(t, string(readFile(t, filepath.Join(filepath.Dir(receiptPath), "raw.jsonl"))), `"large":9007199254740993`)
	remote.assertAlive(t)
	// A fresh execution can use the same caller-owned service after cleanup.
	select {
	case <-remote.disconnected:
	case <-ctx.Done():
		t.Fatal("connection was not released")
	}
	require.Equal(t, strings.TrimSuffix(requestOne, "\n"), <-remote.requests)
	require.Empty(t, remote.requests)
	success, _ := recordRemote(t, env, fixtureDir, remote, "success")
	require.Zero(t, success.code, success.stderr)
}
