package mock

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Synthetic material tests the installed CLI, not a real capture or SDK result.
func mockCapture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "raw"), 0o700))
	plan := []byte(`{"provider":"codex","provenance":"synthetic"}` + "\n")
	raw := []byte("{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"protocol\",\"epoch\":1,\"wire\":{\"id\":1,\"method\":\"ping\",\"params\":{\"unknown\":9007199254740993}}}\n" +
		"{\"ordinal\":2,\"direction\":\"s2c\",\"channel\":\"protocol\",\"epoch\":1,\"wire\":{\"id\":1,\"result\":{\"unknown\":9007199254740993}}}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), raw, 0o600))
	digest := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"provenance": "synthetic", "captureStatus": "complete", "planSHA256": digest(plan),
		"files": tool.Object{"raw.jsonl": digest(raw)},
	}))
	return root
}

func TestMockExternalRecording(t *testing.T) {
	root := mockCapture(t)
	cmd, session := preparedMockCommand(t, root, "app-server", "--listen", "stdio://")
	cmd.Stdin = bytes.NewBufferString("{\"id\":\"consumer-id\",\"method\":\"ping\",\"params\":{\"unknown\":9007199254740993}}\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.JSONEq(t, `{"id":"consumer-id","result":{"unknown":9007199254740993}}`, string(out))
	assert.Contains(t, string(out), "9007199254740993")
	assert.Empty(t, stderr.String())
	receipt := mockReceipt(t, session)
	assert.Equal(t, "passed", receipt["status"])
	assert.Equal(t, true, receipt["fullConsumption"])
	assert.Equal(t, true, receipt["clientEOF"])
	assert.Equal(t, true, receipt["cleanupComplete"])
	assert.Equal(t, false, receipt["nativeLaunched"])
	assert.Equal(t, "codex", receipt["provider"])
	assert.InDelta(t, float64(0), receipt["exitCode"], 0)
	assert.NotEmpty(t, receipt["planSHA256"])
	assert.NotEmpty(t, receipt["recordingSHA256"])
}

func TestMockRequiresClientEOFAfterConsumption(t *testing.T) {
	root := mockCapture(t)
	cmd, session := preparedMockCommand(t, root, "--timeout", "0.3")
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = in.Close() })
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	_, err = in.Write([]byte("{\"id\":7,\"method\":\"ping\",\"params\":{\"unknown\":9007199254740993}}\n"))
	require.NoError(t, err)
	line, err := bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":7,"result":{"unknown":9007199254740993}}`, line)
	require.Error(t, cmd.Wait())
	assert.Equal(t, 1, cmd.ProcessState.ExitCode())
	receipt := mockReceipt(t, session)
	assert.Equal(t, "failed", receipt["status"])
	assert.Equal(t, true, receipt["fullConsumption"])
	assert.Equal(t, false, receipt["clientEOF"])
	assert.Equal(t, true, receipt["cleanupComplete"])
	assert.Contains(t, receipt["error"], "deadline exceeded")
}

func TestMockRejectsUnsupportedSourceFormat(t *testing.T) {
	root := mockCapture(t)
	raw, err := os.ReadFile(filepath.Join(root, "raw", "raw.jsonl"))
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "raw", "manifest.json"), tool.Object{
		"recordingFormat": tool.Object{"name": "agent-mock-jsonl", "version": 999, "transport": "stdio-jsonl"},
		"files":           tool.Object{"raw.jsonl": hex.EncodeToString(sum[:])},
	}))
	session := filepath.Join(t.TempDir(), "session")
	prepare := sessionTool(t, os.Environ(), "prepare", "--session-dir", session, "--recording", root)
	data, err := prepare.CombinedOutput()
	require.NoError(t, err, "%s", data)
	cmd := sessionTool(t, os.Environ(), "mock", "--session-dir", session)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Equal(t, 1, cmd.ProcessState.ExitCode())
	assert.Contains(t, string(out), "unsupported recording format")
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	assert.Empty(t, tool.Array(index["executions"]))
}

func TestMockStrictFailuresRetainReceiptsAndSourceBytes(t *testing.T) {
	for _, tc := range []struct {
		name, input, diagnostic string
		consumed                bool
	}{
		{"early EOF", "", "early client EOF", false},
		{"strict handshake", `{"id":9,"method":"initialize","params":{"unknown":9007199254740993}}` + "\n", "client mismatch", false},
		{"unknown field changed", `{"id":9,"method":"ping","params":{"unknown":9007199254740992}}` + "\n", "client mismatch", false},
		{"unknown field missing", `{"id":9,"method":"ping","params":{}}` + "\n", "client mismatch", false},
		{"extra input", `{"id":9,"method":"ping","params":{"unknown":9007199254740993}}` + "\n ", "extra client byte", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mockCapture(t)
			before := mockSourceBytes(t, root)
			cmd, session := preparedMockCommand(t, root)
			cmd.Stdin = strings.NewReader(tc.input)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			_, err := cmd.Output()
			require.Error(t, err)
			assert.Equal(t, 1, cmd.ProcessState.ExitCode())
			assert.Contains(t, stderr.String(), tc.diagnostic)
			receipt := mockReceipt(t, session)
			assert.Equal(t, "failed", receipt["status"])
			assert.Equal(t, tc.consumed, receipt["fullConsumption"])
			assert.Equal(t, false, receipt["clientEOF"])
			assert.Equal(t, true, receipt["cleanupComplete"])
			assert.InDelta(t, float64(1), receipt["exitCode"], 0)
			assert.Contains(t, receipt["error"], tc.diagnostic)
			assert.Equal(t, before, mockSourceBytes(t, root))
		})
	}
}

func mockSourceBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = string(data)
		return err
	}))
	return files
}

func TestMockRejectsInvalidSourcesBeforeProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		change           func(*testing.T, string)
		args             []string
	}{
		{"plan drift", "source hash mismatch: plan.json", func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), []byte(`{"provider":"claude"}`), 0o600))
		}, nil},
		{"raw drift", "source hash mismatch: raw.jsonl", func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte("{}\n"), 0o600))
		}, nil},
		{"missing raw", "raw.jsonl", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "raw", "raw.jsonl")))
		}, nil},
		{"missing manifest", "manifest.json", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "manifest.json")))
		}, nil},
		{"missing source bindings", "planSHA256", func(t *testing.T, root string) {
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{"provenance": "synthetic"}))
		}, nil},
		{"incomplete capture", "source recording capture is partial", func(t *testing.T, root string) {
			var m tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(root, "manifest.json"), &m))
			m["captureStatus"] = "partial"
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), m))
		}, nil},
		{"agent mismatch", "unsupported native startup argument", nil, []string{"--agent", "claude"}},
		{"native transport", "unsupported native transport", nil, []string{"app-server", "--listen", "http://localhost:1234"}},
		{"no matcher relaxation", "unsupported native startup argument", nil, []string{"--sdk-test-handshakes", "answer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := mockCapture(t)
			if tc.change != nil {
				tc.change(t, root)
			}
			before := mockSourceBytes(t, root)
			var cmd *exec.Cmd
			if tc.change != nil {
				session := filepath.Join(t.TempDir(), "session")
				prepare := sessionTool(t, os.Environ(), "prepare", "--session-dir", session, "--recording", root)
				data, err := prepare.CombinedOutput()
				if err != nil {
					assert.Equal(t, 1, prepare.ProcessState.ExitCode())
					assert.Contains(t, string(data), tc.diagnostic)
					assert.Equal(t, before, mockSourceBytes(t, root))
					return
				}
				cmd = sessionTool(t, os.Environ(), "mock", "--session-dir", session)
			} else {
				cmd, _ = preparedMockCommand(t, root, tc.args...)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			require.Error(t, err)
			assert.Equal(t, 1, cmd.ProcessState.ExitCode())
			assert.Empty(t, out)
			assert.Contains(t, stderr.String(), tc.diagnostic)
			assert.Equal(t, before, mockSourceBytes(t, root))
		})
	}
}

func TestMockCancellationRetainsReceipt(t *testing.T) {
	root := mockCapture(t)
	cmd, session := preparedMockCommand(t, root)
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = in.Close() })
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	_, err = in.Write([]byte("{\"id\":7,\"method\":\"ping\",\"params\":{\"unknown\":9007199254740993}}\n"))
	require.NoError(t, err)
	_, err = bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	require.Error(t, cmd.Wait())
	assert.Equal(t, 1, cmd.ProcessState.ExitCode())
	receipt := mockReceipt(t, session)
	assert.Equal(t, "failed", receipt["status"])
	assert.Equal(t, false, receipt["clientEOF"])
	assert.Equal(t, true, receipt["cleanupComplete"])
	assert.Contains(t, receipt["error"], "context canceled")
}

func TestMockClaudeNativeStdio(t *testing.T) {
	root := mockCapture(t)
	plan := []byte(`{"provider":"claude","provenance":"synthetic"}`)
	raw := []byte("{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"control\",\"epoch\":1,\"wire\":{\"type\":\"control_request\",\"request_id\":\"recorded\",\"request\":{\"subtype\":\"initialize\",\"unknown\":true}}}\n" +
		"{\"ordinal\":2,\"direction\":\"s2c\",\"channel\":\"control\",\"epoch\":1,\"wire\":{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"recorded\",\"response\":{\"unknown\":true}}}}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), raw, 0o600))
	planSum, rawSum := sha256.Sum256(plan), sha256.Sum256(raw)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"planSHA256": hex.EncodeToString(planSum[:]), "provenance": "synthetic", "captureStatus": "complete",
		"files": tool.Object{"raw.jsonl": hex.EncodeToString(rawSum[:])},
	}))
	before := mockSourceBytes(t, root)
	cmd, session := preparedMockCommand(t, root,
		"--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--permission-prompt-tool", "stdio")
	cmd.Stdin = strings.NewReader(`{"type":"control_request","request_id":"consumer","request":{"subtype":"initialize","unknown":true}}` + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s", stderr.String())
	assert.JSONEq(t, `{"type":"control_response","response":{"subtype":"success","request_id":"consumer","response":{"unknown":true}}}`, string(out))
	receipt := mockReceipt(t, session)
	assert.Equal(t, "passed", receipt["status"])
	assert.Equal(t, "synthetic", receipt["provenance"])
	assert.Equal(t, "claude", receipt["provider"])
	assert.Equal(t, before, mockSourceBytes(t, root))
}

func TestMockIndependentConnections(t *testing.T) {
	root := mockCapture(t)
	before := mockSourceBytes(t, root)
	var commands []*exec.Cmd
	var reports []string
	for _, id := range []string{"connection-one", "connection-two"} {
		cmd, session := preparedMockCommand(t, root)
		cmd.Stdin = strings.NewReader(`{"id":"` + id + `","method":"ping","params":{"unknown":9007199254740993}}` + "\n")
		require.NoError(t, cmd.Start())
		commands = append(commands, cmd)
		reports = append(reports, session)
	}
	for i, cmd := range commands {
		require.NoError(t, cmd.Wait())
		var receipt tool.Object
		require.NoError(t, readMockReceipt(reports[i], &receipt))
		assert.Equal(t, "passed", receipt["status"])
		assert.Equal(t, true, receipt["fullConsumption"])
	}
	assert.Equal(t, before, mockSourceBytes(t, root))
}

func TestMockPreservesExistingReportsAndConsumerFiles(t *testing.T) {
	root := mockCapture(t)
	for _, report := range []string{filepath.Join(t.TempDir(), "existing.json"), filepath.Join(root, "new-report.json")} {
		if filepath.Dir(report) != root {
			require.NoError(t, os.WriteFile(report, []byte("consumer evidence\n"), 0o600))
		}
		before := mockSourceBytes(t, root)
		cmd, _ := preparedMockCommand(t, root, "--report", report)
		out, err := cmd.CombinedOutput()
		require.Error(t, err)
		assert.Equal(t, 1, cmd.ProcessState.ExitCode(), "%s", out)
		if filepath.Dir(report) != root {
			data, err := os.ReadFile(report)
			require.NoError(t, err)
			assert.Equal(t, "consumer evidence\n", string(data))
		}
		assert.Equal(t, before, mockSourceBytes(t, root))
	}
}

func TestMockConsumptionNotificationBeforeEOF(t *testing.T) {
	root := mockCapture(t)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	cmd, _ := preparedMockCommand(t, root, "--progress-fd", "3")
	cmd.ExtraFiles = []*os.File{w}
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer in.Close()
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	require.NoError(t, cmd.Start())
	w.Close()
	_, err = in.Write([]byte(`{"id":7,"method":"ping","params":{"unknown":9007199254740993}}` + "\n"))
	require.NoError(t, err)
	var marker [1]byte
	n, err := r.Read(marker[:])
	in.Close()
	waitErr := cmd.Wait()
	require.NoError(t, err, diagnostics.String())
	require.Equal(t, 1, n)
	require.Equal(t, byte('1'), marker[0])
	require.NoError(t, waitErr, diagnostics.String())
}

func preparedMockCommand(t *testing.T, root string, args ...string) (*exec.Cmd, string) {
	t.Helper()
	session, env := preparedSessionWithArgs(t, "prepare", "--recording", root)
	return sessionTool(t, env, append([]string{"mock", "--session-dir", session}, args...)...), session
}

func readMockReceipt(session string, receipt *tool.Object) error {
	var index struct {
		Executions []struct {
			ID string `json:"id"`
		} `json:"executions"`
	}
	if err := tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index); err != nil {
		return err
	}
	if len(index.Executions) == 0 {
		return errors.New("mock did not create an execution")
	}
	id := index.Executions[len(index.Executions)-1].ID
	var execution struct {
		Connections []struct {
			Receipt string `json:"receipt"`
		} `json:"connections"`
	}
	if err := tool.ReadJSON(filepath.Join(session, "executions", id, "execution.json"), &execution); err != nil {
		return err
	}
	return tool.ReadJSON(execution.Connections[0].Receipt, receipt)
}

func mockReceipt(t *testing.T, session string) tool.Object {
	t.Helper()
	var receipt tool.Object
	require.NoError(t, readMockReceipt(session, &receipt))
	return receipt
}
