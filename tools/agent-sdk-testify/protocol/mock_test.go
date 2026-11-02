package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

// Every command below runs the compiled executable outside the checkout, with
// no Python, SDK, agent, compiler, credentials or normal PATH in its environment.
func TestMockCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "agent-sdk-testify")
	build := exec.Command("go", "build", "-o", binary, "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			mockCLIBasics(t, binary, provider)
			mockCLIProgress(t, binary, provider)
			mockCLICancellation(t, binary, provider)
			mockCLIConfiguration(t, binary, provider)
		})
	}
	t.Run("duplex native protocols", func(t *testing.T) { mockCLIDuplex(t, binary) })
	t.Run("test handshakes", func(t *testing.T) { mockCLIHandshakes(t, binary) })
	t.Run("receipt never overwritten", func(t *testing.T) {
		root := t.TempDir()
		source := mockWriteScenario(t, root, mockScenario("<{}"))
		receipt := filepath.Join(root, "receipt.json")
		require.NoError(t, os.WriteFile(receipt, []byte("existing evidence"), 0600))
		cmd := mockCommand(t, binary, "codex", source, "--receipt", receipt)
		out, stderr, code := mockOutput(cmd, "")
		assert.Equal(t, 2, code, stderr)
		assert.Empty(t, out)
		data, err := os.ReadFile(receipt)
		require.NoError(t, err)
		assert.Equal(t, "existing evidence", string(data))
	})
	t.Run("unknown provider", func(t *testing.T) {
		cmd := mockCommand(t, binary, "unknown", "/missing")
		_, stderr, code := mockOutput(cmd, "")
		assert.Equal(t, 2, code, stderr)
	})
}

func mockScenario(actions ...string) string {
	var rows []string
	for i, action := range actions {
		direction := "c2s"
		if action[0] != '>' {
			direction = "s2c"
		}
		payload := `"wire":` + action[1:]
		if action[0] == '!' {
			payload = `"disconnect":true`
		}
		rows = append(rows, fmt.Sprintf(`{"ordinal":%d,"direction":%q,"epoch":1,"channel":"protocol",%s}`, i+1, direction, payload))
	}
	return `{"version":1,"provenance":"synthetic","frames":[` + strings.Join(rows, ",") + `]}`
}

func mockWriteScenario(t *testing.T, root, data string) string {
	t.Helper()
	path := filepath.Join(root, "scenario.json")
	require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	return path
}

func mockCommand(t *testing.T, binary, provider, source string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, append([]string{"protocol", "mock", "--provider", provider, "--scenario", source}, args...)...)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/nonexistent", "GORACE=atexit_sleep_ms=0"}
	return cmd
}

func mockOutput(cmd *exec.Cmd, input string) (string, string, int) {
	var out, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(input), &out, &stderr
	err := cmd.Run()
	if err == nil {
		return out.String(), stderr.String(), 0
	}
	if cmd.ProcessState == nil {
		return out.String(), stderr.String() + err.Error(), -1
	}
	return out.String(), stderr.String(), cmd.ProcessState.ExitCode()
}

func mockCLIBasics(t *testing.T, binary, provider string) {
	for _, tc := range []struct {
		name, source, input, output string
		code                        int
	}{
		{"complete", mockScenario("<{}"), "", "{}\n", 0},
		{"early EOF", mockScenario(">{}"), "", "", 1},
		{"extra frame", mockScenario("<{}"), "{}\n", "{}\n", 1},
		{"extra whitespace", mockScenario("<{}"), " ", "{}\n", 1},
		{"mismatch blocks output", mockScenario(`>{"x":1}`, "<{}"), "{\"x\":2}\n", "", 1},
		{"disconnect", mockScenario("!"), "", "", 4},
		{"disconnect with pending requests", mockScenario(`<{"id":1,"method":"question","type":"control_request","request_id":"q"}`, "!"), "", "{\"id\":1,\"method\":\"question\",\"type\":\"control_request\",\"request_id\":\"q\"}\n", 4},
		{"empty scenario", mockScenario(), "", "", 2},
		{"duplicate client JSON key", mockScenario(">{}", "<{}"), "{\"x\":1,\"x\":2}\n", "", 1},
		{"invalid JSON", mockScenario(">{}"), "{} {}\n", "", 1},
		{"nonobject client", mockScenario(">{}"), "[1,2]\n", "", 1},
		{"raw JSONL", "{\"ordinal\":1,\"direction\":\"s2c\",\"epoch\":1,\"channel\":\"protocol\",\"wire\":{ \"type\":\"notice\", \"x\":0.123456789012345678901234567890 }}\n", "", "{ \"type\":\"notice\", \"x\":0.123456789012345678901234567890 }\n", 0},
		{"legacy JSONL", "{\"ordinal\":1,\"direction\":\"server\",\"channel\":\"protocol\",\"data\":{\"type\":\"future\",\"unknown\":null}}\n", "", "{\"type\":\"future\",\"unknown\":null}\n", 0},
		{"large fragmented frame", mockScenario(`>{"text":"`+strings.Repeat("x", 100000)+`"}`, "<{}"), "{\"text\":\"" + strings.Repeat("x", 100000) + "\"}\n", "{}\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			source := mockWriteScenario(t, root, tc.source)
			receipt := filepath.Join(root, "receipt.json")
			cmd := mockCommand(t, binary, provider, source, "--receipt", receipt)
			out, stderr, code := mockOutput(cmd, tc.input)
			require.Equal(t, tc.code, code, stderr)
			assert.Equal(t, tc.output, out)
			data, err := os.ReadFile(receipt)
			if tc.code != 0 {
				assert.ErrorIs(t, err, os.ErrNotExist)
				assert.NotEmpty(t, stderr)
				return
			}
			assert.Empty(t, stderr)
			require.NoError(t, err)
			var got recording.Receipt
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, 1, got.Version)
			assert.True(t, got.FullConsumption)
			assert.False(t, got.NativeLaunched)
			assert.True(t, got.ClientEOF)
			assert.Equal(t, "Go native mock", got.Peer)
			assert.Equal(t, provider, got.Provider)
			assert.Equal(t, recording.Hash([]byte(tc.source)), got.RecordingSHA256)
			assert.Greater(t, got.Frames, 0)
			info, err := os.Stat(receipt)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
		})
	}
}

type mockProcess struct {
	cmd                     *exec.Cmd
	input, output, progress *os.File
	stderr                  bytes.Buffer
}

func mockStart(t *testing.T, binary, provider, source string, progress bool, args ...string) *mockProcess {
	t.Helper()
	inR, inW, err := os.Pipe()
	require.NoError(t, err)
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	files := []*os.File{inR, inW, outR, outW}
	p := &mockProcess{input: inW, output: outR}
	var progressW *os.File
	if progress {
		p.progress, progressW, err = os.Pipe()
		require.NoError(t, err)
		files = append(files, p.progress, progressW)
		args = append(args, "--progress-fd", "3")
	}
	p.cmd = mockCommand(t, binary, provider, source, args...)
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = inR, outW, &p.stderr
	if progress {
		p.cmd.ExtraFiles = []*os.File{progressW}
	}
	require.NoError(t, p.cmd.Start())
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		for _, f := range files {
			_ = f.Close()
		}
		if p.cmd.ProcessState == nil {
			_ = p.cmd.Wait()
		}
	})
	require.NoError(t, inR.Close())
	require.NoError(t, outW.Close())
	if progress {
		require.NoError(t, progressW.Close())
	}
	require.NoError(t, p.output.SetReadDeadline(time.Now().Add(5*time.Second)))
	if progress {
		require.NoError(t, p.progress.SetReadDeadline(time.Now().Add(5*time.Second)))
	}
	return p
}

func (p *mockProcess) wait(t *testing.T, code int) {
	t.Helper()
	err := p.cmd.Wait()
	if code == 0 {
		require.NoError(t, err, "%s", p.stderr.String())
	} else {
		require.Error(t, err)
	}
	require.Equal(t, code, p.cmd.ProcessState.ExitCode(), p.stderr.String())
}

func mockCLIProgress(t *testing.T, binary, provider string) {
	for _, ending := range []string{"EOF", "extra", "timeout"} {
		t.Run("progress precedes "+ending, func(t *testing.T) {
			root := t.TempDir()
			source := mockWriteScenario(t, root, mockScenario("<{}"))
			receipt := filepath.Join(root, "receipt.json")
			p := mockStart(t, binary, provider, source, true, "--receipt", receipt, "--timeout", "0.4")
			marker, err := io.ReadAll(p.progress)
			require.NoError(t, err)
			require.Equal(t, "1", string(marker))
			_, err = os.Stat(receipt)
			require.ErrorIs(t, err, os.ErrNotExist)
			code := 0
			switch ending {
			case "EOF":
				require.NoError(t, p.input.Close())
			case "extra":
				_, err = p.input.Write([]byte{' '})
				require.NoError(t, err)
				code = 1
			case "timeout":
				code = 3
			}
			p.wait(t, code)
			_, err = os.Stat(receipt)
			if code == 0 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
	for _, mode := range []string{"pending", "mismatch", "disconnect"} {
		t.Run("no progress on "+mode, func(t *testing.T) {
			action := `<{"id":1,"method":"question","type":"control_request","request_id":"q"}`
			code := 1
			if mode == "mismatch" {
				action = ">{}"
			}
			if mode == "disconnect" {
				action = "!"
				code = 4
			}
			source := mockWriteScenario(t, t.TempDir(), mockScenario(action))
			p := mockStart(t, binary, provider, source, true)
			require.NoError(t, p.input.Close())
			p.wait(t, code)
			marker, err := io.ReadAll(p.progress)
			require.NoError(t, err)
			assert.Empty(t, marker)
		})
	}
	t.Run("broken progress descriptor", func(t *testing.T) {
		source := mockWriteScenario(t, t.TempDir(), mockScenario(">{}"))
		p := mockStart(t, binary, provider, source, true)
		require.NoError(t, p.progress.Close())
		_, err := p.input.WriteString("{}\n")
		require.NoError(t, err)
		require.NoError(t, p.input.Close())
		p.wait(t, 1)
	})
}

func mockCLICancellation(t *testing.T, binary, provider string) {
	for _, mode := range []string{"read", "write", "delay", "eof"} {
		t.Run("timeout "+mode, func(t *testing.T) {
			action := "<{}"
			if mode == "read" {
				action = ">{}"
			}
			if mode == "write" {
				action = `<{"text":"` + strings.Repeat("x", 1000000) + `"}`
			}
			script := mockScenario(action)
			if mode == "delay" {
				script = strings.Replace(script, `"wire":`, `"delayMs":1000,"wire":`, 1)
			}
			source := mockWriteScenario(t, t.TempDir(), script)
			p := mockStart(t, binary, provider, source, false, "--timeout", "0.1")
			p.wait(t, 3)
			assert.Contains(t, p.stderr.String(), "deadline exceeded")
			assert.Contains(t, p.stderr.String(), "ordinal 1")
		})
	}
	for _, signal := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(signal.String(), func(t *testing.T) {
			source := mockWriteScenario(t, t.TempDir(), mockScenario("<{}", ">{}"))
			p := mockStart(t, binary, provider, source, false)
			line, err := bufio.NewReader(p.output).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "{}\n", line)
			require.NoError(t, p.cmd.Process.Signal(signal))
			p.wait(t, 3)
			assert.Contains(t, p.stderr.String(), "context canceled")
		})
	}
	t.Run("scenario timeout", func(t *testing.T) {
		script := strings.Replace(mockScenario(">{}"), `"frames":`, `"timeoutMs":40,"frames":`, 1)
		p := mockStart(t, binary, provider, mockWriteScenario(t, t.TempDir(), script), false)
		p.wait(t, 3)
	})
	t.Run("explicit timeout overrides scenario", func(t *testing.T) {
		script := strings.Replace(mockScenario("<{}"), `"frames":`, `"timeoutMs":1,"frames":`, 1)
		script = strings.Replace(script, `"wire":`, `"delayMs":30,"wire":`, 1)
		cmd := mockCommand(t, binary, provider, mockWriteScenario(t, t.TempDir(), script), "--timeout", "1")
		_, stderr, code := mockOutput(cmd, "")
		assert.Equal(t, 0, code, stderr)
	})
}

func mockCLIConfiguration(t *testing.T, binary, provider string) {
	for _, tc := range []struct {
		name, source string
		args         []string
		code         int
	}{
		{"wrong format version", strings.Replace(mockScenario("<{}"), `"version":1`, `"version":2`, 1), nil, 2},
		{"unknown envelope", strings.Replace(mockScenario("<{}"), `"wire":`, `"unknown":true,"wire":`, 1), nil, 2},
		{"duplicate JSON", `{"version":1,"version":1}`, nil, 2},
		{"bad fault provenance", strings.Replace(mockScenario("!"), `"synthetic"`, `"recording","source":"manifest"`, 1), nil, 2},
		{"max frames", mockScenario("<{}", "<{}"), []string{"--max-frames", "1"}, 2},
		{"source bytes", mockScenario("<{}"), []string{"--max-bytes", "1"}, 2},
		{"frame bytes", mockScenario("<{}"), []string{"--max-frame-bytes", "2"}, 2},
		{"native version mismatch", mockScenario(`<{"claude_code_version":"2.1.1"}`), []string{"--recording-version", "2.1.2"}, 2},
		{"native version exact", mockScenario(`<{"claude_code_version":"2.1.1"}`), []string{"--recording-version", "2.1.1"}, 0},
		{"native version absent", mockScenario("<{}"), []string{"--recording-version", "future"}, 0},
		{"native version invalid type", mockScenario(`<{"claude_code_version":1}`), []string{"--recording-version", "1"}, 2},
		{"zero timeout", mockScenario("<{}"), []string{"--timeout", "0"}, 2},
		{"zero max frames", mockScenario("<{}"), []string{"--max-frames", "0"}, 2},
		{"invalid progress fd", mockScenario("<{}"), []string{"--progress-fd", "9999"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := mockCommand(t, binary, provider, mockWriteScenario(t, t.TempDir(), tc.source), tc.args...)
			out, stderr, code := mockOutput(cmd, "")
			assert.Equal(t, tc.code, code, stderr)
			if code != 0 {
				assert.Empty(t, out)
			}
		})
	}
	args := []string{"app-server", "--listen=stdio://"}
	bad := []string{"--listen=ws://localhost"}
	if provider == "claude" {
		args = []string{"--input-format", "stream-json", "--output-format=stream-json", "--permission-prompt-tool", "stdio", "--verbose", "--include-partial-messages", "--model=x", "--resume=y"}
		bad = []string{"--input-format=json"}
	}
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{{"native startup", args, 0}, {"native args after separator", append([]string{"--"}, args...), 0}, {"wrong transport", bad, 2}, {"duplicate startup", append(append([]string{}, args...), args...), 2}} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := mockCommand(t, binary, provider, mockWriteScenario(t, t.TempDir(), mockScenario("<{}")), tc.args...)
			_, stderr, code := mockOutput(cmd, "")
			assert.Equal(t, tc.code, code, stderr)
		})
	}
}

func mockCLIDuplex(t *testing.T, binary string) {
	for _, provider := range []string{"codex", "claude"} {
		actions := []string{`>{"id":1,"method":"one"}`, `>{"id":2,"method":"two"}`, `<{"id":2,"error":{"code":-32000,"message":"synthetic"}}`, `<{"id":1,"method":"question"}`, `>{"id":1,"result":{}}`, `<{"id":1,"result":{"id":1}}`}
		input := "{\"id\":\"live\",\"method\":\"one\"}\n{\"id\":9007199254740993,\"method\":\"two\"}\n{\"id\":1,\"result\":{}}\n"
		want := "{\"id\":9007199254740993,\"error\":{\"code\":-32000,\"message\":\"synthetic\"}}\n{\"id\":1,\"method\":\"question\"}\n{\"id\":\"live\",\"result\":{\"id\":1}}\n"
		if provider == "claude" {
			actions = []string{`>{"type":"control_request","request_id":"old","request":{"request_id":"literal"}}`, `<{ "type":"control_response", "response":{"request_id":"old","payload":{"request_id":"old"}}}`, `<{"type":"control_request","request_id":"q","request":{}}`, `>{"type":"control_response","response":{"request_id":"q","response":{}}}`, `<{"type":"control_request","request_id":2,"request":{}}`, `<{"type":"control_cancel_request","request_id":2}`}
			input = "{\"type\":\"control_request\",\"request_id\":\"live\",\"request\":{\"request_id\":\"literal\"}}\n" + actions[3][1:] + "\n"
			want = strings.Replace(actions[1][1:], `"request_id":"old"`, `"request_id":"live"`, 1) + "\n" + actions[2][1:] + "\n" + actions[4][1:] + "\n" + actions[5][1:] + "\n"
		}
		t.Run(provider, func(t *testing.T) {
			cmd := mockCommand(t, binary, provider, mockWriteScenario(t, t.TempDir(), mockScenario(actions...)))
			out, stderr, code := mockOutput(cmd, input)
			require.Equal(t, 0, code, stderr)
			assert.Equal(t, want, out)
		})
	}
}

func mockCLIHandshakes(t *testing.T, binary string) {
	actions := []string{`<{"id":"q","method":"question","params":{"threadId":"t"}}`, `>{"id":"q","result":{}}`, `<{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":"q"}}`}
	answer := actions[1][1:] + "\n"
	settle := "{\"id\":8,\"method\":\"replay/answer\"}\n"
	assertion := "{\"id\":9,\"method\":\"replay/assert\"}\n"
	for _, tc := range []struct {
		name, mode, input string
		code              int
	}{
		{"answer", "answer", answer + settle, 0},
		{"answer and assert", "answer-and-assert", answer + settle + assertion, 0},
		{"missing settlement", "answer", answer, 1},
		{"missing assertion", "answer-and-assert", answer + settle, 1},
		{"extra params", "answer", answer + "{\"id\":8,\"method\":\"replay/answer\",\"params\":{}}\n", 1},
		{"wrong order", "answer-and-assert", answer + assertion + settle, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := mockCommand(t, binary, "codex", mockWriteScenario(t, t.TempDir(), mockScenario(actions...)), "--sdk-test-handshakes", tc.mode)
			out, stderr, code := mockOutput(cmd, tc.input)
			require.Equal(t, tc.code, code, stderr)
			if code == 0 {
				assert.Less(t, strings.Index(out, `"id":8`), strings.Index(out, "serverRequest/resolved"))
			}
		})
	}
	t.Run("Claude rejects handshake no-op", func(t *testing.T) {
		cmd := mockCommand(t, binary, "claude", mockWriteScenario(t, t.TempDir(), mockScenario("<{}")), "--sdk-test-handshakes", "answer")
		_, stderr, code := mockOutput(cmd, "")
		assert.Equal(t, 2, code, stderr)
	})
}
