package conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

type object = map[string]any

const (
	requestOne = `{"id":1,"method":"ping"}` + "\n"
	requestTwo = `{"id":2,"method":"ping"}` + "\n"
)

type commandResult struct {
	stdout string
	stderr string
	code   int
}

func runCLI(t *testing.T, binary string, input io.Reader, args ...string) ([]byte, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = input
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), stderr.String(), 0
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	return stdout.Bytes(), stderr.String(), exit.ExitCode()
}

func runTool(t *testing.T, env []string, dir, input string, args ...string) commandResult {
	return runToolWithFiles(t, env, dir, input, nil, args...)
}

func runToolWithFiles(t *testing.T, env []string, dir, input string, extraFiles []*os.File, args ...string) commandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = strings.NewReader(input)
	cmd.ExtraFiles = extraFiles
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := commandResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return result
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, stderr.String())
	result.code = exit.ExitCode()
	return result
}

func mustRun(t *testing.T, env []string, dir, input string, args ...string) commandResult {
	t.Helper()
	result := runTool(t, env, dir, input, args...)
	require.Zero(t, result.code, "stdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	return result
}

func isolatedCommands(t *testing.T, root, provider string) (agent, driverDir string, env []string) {
	t.Helper()
	home := filepath.Join(root, "host-home")
	config := filepath.Join(root, "host-config")
	driverDir = filepath.Join(root, "drivers")
	commands := filepath.Join(root, "commands")
	for _, dir := range []string{home, config, driverDir, commands} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	agent = filepath.Join(commands, "synthetic-"+provider)
	writeExecutable(t, agent, os.Executable, "conformance-agent")
	configKey := "CODEX_HOME"
	if provider == "claude" {
		configKey = "CLAUDE_CONFIG_DIR"
	}
	env = []string{
		"PATH=" + driverDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		configKey + "=" + config,
		"GORACE=" + os.Getenv("GORACE"),
	}
	return agent, driverDir, env
}

func installDriver(t *testing.T, dir, provider, mode string) {
	t.Helper()
	writeExecutable(t, filepath.Join(dir, "agent-sdk-testify-"+provider), os.Executable, "conformance-driver", mode)
}

func writeExecutable(t *testing.T, path string, executable func() (string, error), args ...string) {
	t.Helper()
	self, err := executable()
	require.NoError(t, err)
	words := []string{shellQuote(self)}
	for _, arg := range args {
		words = append(words, shellQuote(arg))
	}
	script := "#!/bin/sh\n" + testutil.RaceShellExport() + "exec " + strings.Join(words, " ") + " \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
}

func readAllFiles(t *testing.T, root string) []byte {
	t.Helper()
	var contents bytes.Buffer
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents.WriteString(path)
		contents.WriteByte('\n')
		contents.Write(data)
		contents.WriteByte('\n')
		return nil
	}))
	return contents.Bytes()
}

func readJSON(t *testing.T, path string) object {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var value object
	require.NoError(t, json.Unmarshal(data, &value))
	require.NotNil(t, value)
	return value
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digestFile(t *testing.T, path string) string {
	t.Helper()
	return digestBytes(readFile(t, path))
}

func obj(value any) object {
	if value == nil {
		return nil
	}
	valueObject, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return valueObject
}

func array(value any) []any {
	if value == nil {
		return nil
	}
	values, _ := value.([]any)
	return values
}

func stringsOf(value any) []string {
	values := array(value)
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, str(value))
	}
	return result
}

func str(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func sessionIndex(t *testing.T, session string) object {
	t.Helper()
	return readJSON(t, filepath.Join(session, "executions", "index.json"))
}

func executionByID(t *testing.T, session, id string) object {
	t.Helper()
	return readJSON(t, filepath.Join(session, "executions", id, "execution.json"))
}

func connectionStatus(t *testing.T, session string, execution object, id string) any {
	t.Helper()
	for _, value := range array(sessionIndex(t, session)["executions"]) {
		row := obj(value)
		if str(row["id"]) != str(execution["executionId"]) {
			continue
		}
		for _, item := range array(row["connections"]) {
			connection := obj(item)
			if str(connection["id"]) == id {
				return connection["status"]
			}
		}
	}
	t.Fatalf("execution index has no %s connection", id)
	return nil
}

func latestExecution(t *testing.T, session, command string) object {
	t.Helper()
	index := sessionIndex(t, session)
	rows := array(index["executions"])
	for i := range slices.Backward(rows) {
		row := obj(rows[i])
		if str(row["command"]) == command {
			return executionByID(t, session, str(row["id"]))
		}
	}
	t.Fatalf("session has no %s execution", command)
	return nil
}

func artifactByID(t *testing.T, session, id string) object {
	t.Helper()
	index := materialIndex(t, session)
	for _, value := range array(index["materials"]) {
		artifact := obj(value)
		if str(artifact["id"]) == id {
			return artifact
		}
	}
	t.Fatalf("session has no artifact %s", id)
	return nil
}

func latestCapture(t *testing.T, session string) object {
	t.Helper()
	id := currentMaterialID(t, session)
	require.NotEmpty(t, id)
	return artifactByID(t, session, id)
}

func prepareSession(t *testing.T, env []string, args ...string) string {
	t.Helper()
	base := t.TempDir()
	session := filepath.Join(base, "session")
	args = append(args, "--session-dir", session)
	result := mustRun(t, env, base, "", append([]string{"prepare"}, args...)...)
	require.Empty(t, result.stderr)
	t.Cleanup(func() {
		if t.Failed() {
			for _, parent := range []string{"official-records", "verifications"} {
				var ids []string
				if readJSONFile(filepath.Join(session, parent, "index.json"), &ids) != nil || len(ids) == 0 {
					continue
				}
				path := filepath.Join(session, parent, ids[len(ids)-1], "driver", "stderr.log")
				if data, err := os.ReadFile(path); err == nil {
					t.Logf("official driver diagnostics: %s", data)
				}
			}
		}
		var descriptor object
		data, err := os.ReadFile(filepath.Join(session, "session.json"))
		if err == nil && json.Unmarshal(data, &descriptor) == nil {
			_ = os.RemoveAll(str(descriptor["resourceRoot"]))
		}
	})
	return session
}

func writeConsumerPlan(t *testing.T, base, caseID string) string {
	t.Helper()
	path := filepath.Join(base, "consumer-plan.json")
	writeJSON(t, path, object{
		"version":       1,
		"agentType":     "codex",
		"caseId":        caseID,
		"driverProfile": "consumer",
		"project":       filepath.Join(base, "consumer-project"),
		"model":         "synthetic-model",
		"cliVersion":    "synthetic-conformance-native",
		"scenario": object{
			"files":      object{"fixture.txt": "fixture\n"},
			"parameters": object{"keep": 9007199254740993},
		},
	})
	return path
}

func recordConsumer(t *testing.T, env []string, session, agent, request string) object {
	t.Helper()
	result := mustRun(t, env, t.TempDir(), request,
		"agent", "record", "--session-dir", session, "--cmd", agent, "--",
		"app-server", "--listen", "stdio://",
	)
	var responses []object
	for line := range strings.SplitSeq(strings.TrimSpace(result.stdout), "\n") {
		if line == "" {
			continue
		}
		var response object
		require.NoError(t, json.Unmarshal([]byte(line), &response))
		responses = append(responses, response)
	}
	require.Len(t, responses, 1)
	require.Equal(t, true, obj(responses[0]["result"])["synthetic"])
	return latestExecution(t, session, "record")
}

func runPreparedMock(t *testing.T, env []string, session, connection, request string, extra ...string) commandResult {
	t.Helper()
	args := []string{"agent", "mock", "--session-dir", session}
	if connection != "" {
		args = append(args, "--connection", connection)
	}
	args = append(args, extra...)
	args = append(args, "--", "app-server", "--listen", "stdio://")
	result := runTool(t, env, t.TempDir(), request, args...)
	require.Zero(t, result.code, "stdout:\n%s\nstderr:\n%s", result.stdout, result.stderr)
	return result
}

func finishSession(t *testing.T, env []string, session string) (commandResult, object) {
	t.Helper()
	result := runTool(t, env, t.TempDir(), "", "finish", "--session-dir", session)
	var report object
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &report), result.stdout)
	return result, report
}

// builtinInputs reads the raw bytes imported by the installed prepare CLI.
func builtinInputs(t *testing.T, provider, caseID string) map[string]string {
	t.Helper()
	session := prepareSession(t, os.Environ(), "--agent", provider, "--case", caseID)
	material := str(obj(array(materialIndex(t, session)["materials"])[0])["path"])
	inputs := map[string]string{}
	for _, phase := range []struct{ id, path string }{{"main", "raw/raw.jsonl"}, {"probe", "remote-probe/raw/raw.jsonl"}} {
		data, err := os.ReadFile(filepath.Join(material, phase.path))
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		var input strings.Builder
		for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
			var frame struct {
				Direction string          `json:"direction"`
				Wire      json.RawMessage `json:"wire"`
			}
			require.NoError(t, json.Unmarshal(line, &frame))
			if frame.Direction == "c2s" {
				input.Write(frame.Wire)
				input.WriteByte('\n')
			}
		}
		inputs[phase.id] = input.String()
	}
	finished, _ := finishSession(t, os.Environ(), session)
	require.Zero(t, finished.code, finished.stderr)
	require.NotEmpty(t, inputs)
	return inputs
}

type syntheticCorpus struct {
	index, scenarios, archive string
}

func newSyntheticCorpus(t *testing.T) syntheticCorpus {
	t.Helper()
	repository, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	root := t.TempDir()
	index := readJSON(t, filepath.Join(repository, "data", "index.json"))
	for _, value := range array(index["cases"]) {
		row := obj(value)
		delete(row, "materials")
		delete(row, "adoptionSHA256")
		row["adoption"] = "missing"
		row["verification"] = "unverified"
		row["verificationReason"] = "synthetic conformance suite has no admitted evidence"
	}
	corpus := syntheticCorpus{
		index:     filepath.Join(root, "corpus", "index.json"),
		scenarios: filepath.Join(repository, "data", "scenarios"),
		archive:   filepath.Join(root, "private-archive"),
	}
	writeJSON(t, corpus.index, index)
	return corpus
}

func assertFileHashes(t *testing.T, root string, hashes object) {
	t.Helper()
	require.NotEmpty(t, hashes)
	for name, value := range hashes {
		expected := str(value)
		require.Len(t, expected, 64, name)
		require.Equal(t, expected, digestFile(t, filepath.Join(root, filepath.FromSlash(name))), name)
	}
}

func requireDistinct(t *testing.T, values ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, value := range values {
		require.NotEmpty(t, value)
		require.False(t, seen[value], "duplicate path %s", value)
		seen[value] = true
	}
}

func materialIndex(t *testing.T, session string) object {
	t.Helper()
	return readJSON(t, filepath.Join(session, "materials", "index.json"))
}

func currentMaterialID(t *testing.T, session string) string {
	t.Helper()
	return str(readJSON(t, filepath.Join(session, "current.json"))["materialId"])
}

func publicAction(action string) []string {
	if action == "verify" {
		return []string{"official", action}
	}
	return []string{"agent", action}
}

func latestVerificationRoot(t *testing.T, session string) string {
	t.Helper()
	root, _ := latestOfficialVerification(t, session)
	return root
}

// Cancellation is triggered at the observed protocol boundary. The existing
// process watchdog remains 45s; native startup cost cannot cancel the wrong phase.
func cancelOfficialDuringProtocol(t *testing.T, env []string, session string, args ...string) commandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Env, cmd.Dir = env, t.TempDir()
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ready := false
	for !ready {
		var active object
		if readJSONFile(filepath.Join(session, "official-active.json"), &active) == nil {
			var checkpoint object
			ready = readJSONFile(filepath.Join(str(active["dir"]), "driver", "checkpoint.json"), &checkpoint) == nil
		}
		if ready {
			break
		}
		select {
		case err := <-done:
			require.NoError(t, err, stderr.String())
			t.Fatal("driver exited before protocol cancellation boundary")
		case <-ctx.Done():
			t.Fatal("protocol cancellation boundary was not reached")
		case <-time.After(10 * time.Millisecond):
		}
	}
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	err := <-done
	require.Error(t, err, stderr.String())
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}
}
