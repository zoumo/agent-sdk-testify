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

	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/replay"
)

// Controlled processes exercise the actual replay kernel, independently of SDKs.
// Public installed CLI coverage belongs to test/e2e.
func TestReplayKernelProcesses(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			replayProcessProgress(t, provider)
			replayProcessCancellation(t, provider)
			t.Run("fragmented duplex large frames", func(t *testing.T) {
				wire := `{"unknown":"` + strings.Repeat("x", 100000) + `","number":9007199254740993}`
				source := replayWriteScenario(t, t.TempDir(), replayScenario(">"+wire, "<"+wire))
				p := replayStart(t, provider, source, false)
				input := wire + "\r\n"
				for start := 0; start < len(input); start += 127 {
					_, err := p.input.WriteString(input[start:min(start+127, len(input))])
					require.NoError(t, err)
				}
				line, err := bufio.NewReader(p.output).ReadString('\n')
				require.NoError(t, err)
				require.Equal(t, wire+"\n", line)
				require.NoError(t, p.input.Close())
				p.wait(t, 0)
			})
		})
	}
}

// Finite protocol matrices connect the real reader and replay engine in this
// process. Only descriptor, cancellation and signal behavior needs child peers.
func TestReplayKernelRules(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			replayProcessBasics(t, provider)
			replayProcessConfiguration(t, provider)
		})
	}
	t.Run("duplex native protocols", replayProcessDuplex)
	t.Run("test handshakes", replayProcessHandshakes)
	t.Run("unknown provider", func(t *testing.T) {
		_, diagnostic, code := replayInProcess(t, "unknown", replayScenario("<{}"), "")
		assert.Equal(t, 1, code, diagnostic)
	})
}

func replayInProcess(t *testing.T, provider, source, input string, settings ...replayProcessOptions) (string, string, int) {
	t.Helper()
	options := replayProcessOptions{Limits: recording.Limits{MaxFrames: 10000, MaxFrameBytes: 2 << 20}}
	if len(settings) != 0 {
		options = settings[0]
	}
	document, err := recording.Read(strings.NewReader(source), provider, options.Limits)
	if err != nil {
		return "", err.Error(), 1
	}
	inR, inW, err := os.Pipe()
	require.NoError(t, err)
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	for _, f := range []*os.File{inR, inW, outR, outW} {
		t.Cleanup(func() { _ = f.Close() })
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	written := make(chan struct{})
	go func() { _, _ = inW.WriteString(input); _ = inW.Close(); close(written) }()
	type outputResult struct {
		data []byte
		err  error
	}
	read := make(chan outputResult, 1)
	go func() { data, err := io.ReadAll(outR); read <- outputResult{data, err} }()
	err = replay.Run(ctx, document, options.Limits, options.Handshakes, inR, outW, -1)
	_ = inR.Close()
	_ = outW.Close()
	var output outputResult
	select {
	case output = <-read:
	case <-ctx.Done():
		t.Fatal("replay output did not finish")
	}
	require.NoError(t, output.err)
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("replay input did not finish")
	}
	if err != nil {
		return string(output.data), err.Error(), 1
	}
	return string(output.data), "", 0
}

func replayScenario(actions ...string) string {
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

func replayWriteScenario(t *testing.T, root, data string) string {
	t.Helper()
	path := filepath.Join(root, "scenario.json")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}

// replayProcessOptions contains only inputs to recording.Read and replay.Run.
// Timeouts are explicit test stimuli; the parent process owns the watchdog.
type replayProcessOptions struct {
	Provider   string
	Source     string
	Limits     recording.Limits
	Timeout    time.Duration
	Handshakes string
}

func replayCommand(t *testing.T, provider, source string, settings ...replayProcessOptions) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	options := replayProcessOptions{Limits: recording.Limits{MaxFrames: 10000, MaxFrameBytes: 2 << 20}}
	if len(settings) != 0 {
		options = settings[0]
	}
	options.Provider, options.Source = provider, source
	encoded, err := json.Marshal(options)
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(ctx, executable, "replay-kernel", string(encoded))
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/nonexistent", "GORACE=" + os.Getenv("GORACE")}
	return cmd
}

func replayProcessBasics(t *testing.T, provider string) {
	for _, tc := range []struct {
		name, source, input, output string
		code                        int
	}{
		{"complete", replayScenario("<{}"), "", "{}\n", 0},
		{"early EOF", replayScenario(">{}"), "", "", 1},
		{"extra frame", replayScenario("<{}"), "{}\n", "{}\n", 1},
		{"extra whitespace", replayScenario("<{}"), " ", "{}\n", 1},
		{"mismatch blocks output", replayScenario(`>{"x":1}`, "<{}"), "{\"x\":2}\n", "", 1},
		{"disconnect", replayScenario("!"), "", "", 1},
		{"disconnect with pending requests", replayScenario(`<{"id":1,"method":"question","type":"control_request","request_id":"q"}`, "!"), "", "{\"id\":1,\"method\":\"question\",\"type\":\"control_request\",\"request_id\":\"q\"}\n", 1},
		{"empty scenario", replayScenario(), "", "", 1},
		{"duplicate client JSON key", replayScenario(">{}", "<{}"), "{\"x\":1,\"x\":2}\n", "", 1},
		{"invalid JSON", replayScenario(">{}"), "{} {}\n", "", 1},
		{"nonobject client", replayScenario(">{}"), "[1,2]\n", "", 1},
		{"raw JSONL", "{\"ordinal\":1,\"direction\":\"s2c\",\"epoch\":1,\"channel\":\"protocol\",\"wire\":{ \"type\":\"notice\", \"x\":0.123456789012345678901234567890 }}\n", "", "{ \"type\":\"notice\", \"x\":0.123456789012345678901234567890 }\n", 0},
		{"legacy JSONL", "{\"ordinal\":1,\"direction\":\"server\",\"channel\":\"protocol\",\"data\":{\"type\":\"future\",\"unknown\":null}}\n", "", "{\"type\":\"future\",\"unknown\":null}\n", 0},
		{"large fragmented frame", replayScenario(`>{"text":"`+strings.Repeat("x", 100000)+`"}`, "<{}"), "{\"text\":\"" + strings.Repeat("x", 100000) + "\"}\n", "{}\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := replayInProcess(t, provider, tc.source, tc.input)
			require.Equal(t, tc.code, code, stderr)
			assert.Equal(t, tc.output, out)
			if tc.code != 0 {
				assert.NotEmpty(t, stderr)
			} else {
				assert.Empty(t, stderr)
			}
		})
	}
}

type replayProcess struct {
	cmd                     *exec.Cmd
	input, output, progress *os.File
	stderr                  bytes.Buffer
}

func replayStart(t *testing.T, provider, source string, progress bool, settings ...replayProcessOptions) *replayProcess {
	t.Helper()
	inR, inW, err := os.Pipe()
	require.NoError(t, err)
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	files := []*os.File{inR, inW, outR, outW}
	p := &replayProcess{input: inW, output: outR}
	var progressW *os.File
	if progress {
		p.progress, progressW, err = os.Pipe()
		require.NoError(t, err)
		files = append(files, p.progress, progressW)
	}
	p.cmd = replayCommand(t, provider, source, settings...)
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = inR, outW, &p.stderr
	if progress {
		p.cmd.ExtraFiles = []*os.File{progressW}
		p.cmd.Args = append(p.cmd.Args, "progress")
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

func (p *replayProcess) wait(t *testing.T, code int) {
	t.Helper()
	err := p.cmd.Wait()
	if code == 0 {
		require.NoError(t, err, "%s", p.stderr.String())
	} else {
		require.Error(t, err)
	}
	require.Equal(t, code, p.cmd.ProcessState.ExitCode(), p.stderr.String())
}

func replayProcessProgress(t *testing.T, provider string) {
	for _, ending := range []string{"EOF", "extra", "timeout"} {
		t.Run("progress precedes "+ending, func(t *testing.T) {
			root := t.TempDir()
			source := replayWriteScenario(t, root, replayScenario("<{}"))
			p := replayStart(t, provider, source, true, replayProcessOptions{Timeout: 400 * time.Millisecond})
			marker, err := io.ReadAll(p.progress)
			require.NoError(t, err)
			require.Equal(t, "1", string(marker))
			code := 0
			switch ending {
			case "EOF":
				require.NoError(t, p.input.Close())
			case "extra":
				_, err = p.input.Write([]byte{' '})
				require.NoError(t, err)
				code = 1
			case "timeout":
				code = 1
			}
			p.wait(t, code)
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
				code = 1
			}
			source := replayWriteScenario(t, t.TempDir(), replayScenario(action))
			p := replayStart(t, provider, source, true)
			require.NoError(t, p.input.Close())
			p.wait(t, code)
			marker, err := io.ReadAll(p.progress)
			require.NoError(t, err)
			assert.Empty(t, marker)
		})
	}
	t.Run("broken progress descriptor", func(t *testing.T) {
		source := replayWriteScenario(t, t.TempDir(), replayScenario(">{}"))
		p := replayStart(t, provider, source, true)
		require.NoError(t, p.progress.Close())
		_, err := p.input.WriteString("{}\n")
		require.NoError(t, err)
		require.NoError(t, p.input.Close())
		p.wait(t, 1)
	})
}

func replayProcessCancellation(t *testing.T, provider string) {
	for _, mode := range []string{"read", "write", "delay", "eof"} {
		t.Run("timeout "+mode, func(t *testing.T) {
			action := "<{}"
			if mode == "read" {
				action = ">{}"
			}
			if mode == "write" {
				action = `<{"text":"` + strings.Repeat("x", 1000000) + `"}`
			}
			script := replayScenario(action)
			if mode == "delay" {
				script = strings.Replace(script, `"wire":`, `"delayMs":1000,"wire":`, 1)
			}
			source := replayWriteScenario(t, t.TempDir(), script)
			p := replayStart(t, provider, source, false, replayProcessOptions{Timeout: 100 * time.Millisecond, Limits: recording.Limits{MaxFrameBytes: 2 << 20}})
			p.wait(t, 1)
			assert.Contains(t, p.stderr.String(), "deadline exceeded")
			assert.Contains(t, p.stderr.String(), "ordinal 1")
		})
	}
	for _, signal := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(signal.String(), func(t *testing.T) {
			source := replayWriteScenario(t, t.TempDir(), replayScenario("<{}", ">{}"))
			p := replayStart(t, provider, source, false)
			line, err := bufio.NewReader(p.output).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "{}\n", line)
			require.NoError(t, p.cmd.Process.Signal(signal))
			p.wait(t, 1)
			assert.Contains(t, p.stderr.String(), "context canceled")
		})
	}
}

func replayProcessConfiguration(t *testing.T, provider string) {
	for _, tc := range []struct {
		name, source string
		options      replayProcessOptions
		code         int
	}{
		{"wrong format version", strings.Replace(replayScenario("<{}"), `"version":1`, `"version":2`, 1), replayProcessOptions{}, 1},
		{"unknown envelope", strings.Replace(replayScenario("<{}"), `"wire":`, `"unknown":true,"wire":`, 1), replayProcessOptions{}, 1},
		{"duplicate JSON", `{"version":1,"version":1}`, replayProcessOptions{}, 1},
		{"bad fault provenance", strings.Replace(replayScenario("!"), `"synthetic"`, `"recording","source":"manifest"`, 1), replayProcessOptions{}, 1},
		{"max frames", replayScenario("<{}", "<{}"), replayProcessOptions{Limits: recording.Limits{MaxFrames: 1}}, 1},
		{"source bytes", replayScenario("<{}"), replayProcessOptions{Limits: recording.Limits{MaxBytes: 1}}, 1},
		{"frame bytes", replayScenario("<{}"), replayProcessOptions{Limits: recording.Limits{MaxFrameBytes: 2}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := replayInProcess(t, provider, tc.source, "", tc.options)
			assert.Equal(t, tc.code, code, stderr)
			if code != 0 {
				assert.Empty(t, out)
			}
		})
	}
}

func replayProcessDuplex(t *testing.T) {
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
			out, stderr, code := replayInProcess(t, provider, replayScenario(actions...), input)
			require.Equal(t, 0, code, stderr)
			assert.Equal(t, want, out)
		})
	}
}

func replayProcessHandshakes(t *testing.T) {
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
			out, stderr, code := replayInProcess(t, "codex", replayScenario(actions...), tc.input, replayProcessOptions{Handshakes: tc.mode})
			require.Equal(t, tc.code, code, stderr)
			if code == 0 {
				assert.Less(t, strings.Index(out, `"id":8`), strings.Index(out, "serverRequest/resolved"))
			}
		})
	}
	t.Run("Claude rejects handshake no-op", func(t *testing.T) {
		_, stderr, code := replayInProcess(t, "claude", replayScenario("<{}"), "", replayProcessOptions{Handshakes: "answer"})
		assert.Equal(t, 1, code, stderr)
	})
}

func TestReplayOpaqueNativeVersionsAndStrictUnknownFields(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, wire := range []string{`{ "claude_code_version":"2.1.1" }`, `{"claude_code_version":"future"}`, `{"unknown":null}`, `{"claude_code_version":1}`} {
			t.Run(provider+"/"+wire, func(t *testing.T) {
				out, diagnostic, code := replayInProcess(t, provider, replayScenario("<"+wire), "")
				require.Zero(t, code, diagnostic)
				require.Equal(t, wire+"\n", out)
			})
		}
	}
	for _, tc := range []struct{ name, input string }{
		{"strict handshake", `{"id":9,"method":"initialize","params":{"unknown":9007199254740993}}`},
		{"unknown field changed", `{"id":9,"method":"ping","params":{"unknown":9007199254740992}}`},
		{"unknown field missing", `{"id":9,"method":"ping","params":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, code := replayInProcess(t, "codex", replayScenario(`>{"id":1,"method":"ping","params":{"unknown":9007199254740993}}`, `<{"id":1,"result":{}}`), tc.input+"\n")
			require.Equal(t, 1, code)
			require.Contains(t, diagnostic, "client mismatch")
			require.Empty(t, out)
		})
	}
}
