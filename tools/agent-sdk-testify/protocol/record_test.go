package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/command"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

// The subprocess runs the protocol dispatcher (including signal context
// and exit codes), or an explicitly synthetic native peer. Under -race both
// the proxy and peer are instrumented, without requiring a nested go build.
func TestRecordCLIHelper(t *testing.T) {
	role := os.Getenv("AGENT_MOCK_RECORD_TEST_ROLE")
	if role == "" {
		return
	}
	at := 0
	for i, arg := range os.Args {
		if arg == "--" {
			at = i + 1
			break
		}
	}
	if at == 0 {
		os.Exit(99)
	}
	if role == "cli" {
		os.Args = append([]string{os.Args[0]}, os.Args[at:]...)
		protocolTestMain()
		return
	}
	args := os.Args[at:]
	if role != "peer" || len(args) == 0 {
		os.Exit(98)
	}
	switch args[0] {
	case "eof-action":
		recordEOFActionPeer(args[1])
	case "echo":
		r := bufio.NewReader(os.Stdin)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				_, _ = os.Stdout.Write(line)
			}
			if err != nil {
				break
			}
		}
		fmt.Fprint(os.Stderr, "native stderr\x00exact\n")
		fmt.Fprint(os.Stdout, "{\"afterEOF\":true}\n")
	case "fragment":
		for _, part := range []string{" {\"unknown\":", "[1e+09,9007199254740993,", "-0.00],\"escape\":\"\\u0061\"}", "\r", "\n"} {
			fmt.Fprint(os.Stdout, part)
			time.Sleep(time.Millisecond)
		}
	case "one-line":
		line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
		_, _ = os.Stdout.Write(line)
	case "emit":
		fmt.Fprint(os.Stdout, args[1])
	case "nonzero":
		fmt.Fprint(os.Stdout, "{\"beforeExit\":true}\n")
		os.Exit(17)
	case "idle":
		fmt.Fprint(os.Stdout, "{\"ready\":true}\n")
		for {
			time.Sleep(time.Hour)
		}
	case "stdout-closed":
		_ = os.Stdout.Close()
		for {
			time.Sleep(time.Hour)
		}
	case "no-read":
		fmt.Fprint(os.Stdout, "{\"ready\":true}\n")
		for {
			time.Sleep(time.Hour)
		}
	case "flood":
		line := "{\"data\":\"" + strings.Repeat("x", 128*1024) + "\"}\n"
		for {
			if _, err := io.WriteString(os.Stdout, line); err != nil {
				os.Exit(0)
			}
		}
	case "duplex":
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(done) }()
		line := "{\"server\":\"" + strings.Repeat("s", 256*1024) + "\"}\n"
		for i := 0; i < 8; i++ {
			_, _ = io.WriteString(os.Stdout, line)
		}
		<-done
		fmt.Fprint(os.Stdout, "{\"done\":true}\n")
	case "identity":
		cwd, _ := os.Getwd()
		data, _ := json.Marshal(map[string]string{"cwd": cwd, "override": os.Getenv("RECORD_OVERRIDE"), "empty": os.Getenv("RECORD_EMPTY"), "inherited": os.Getenv("RECORD_INHERITED")})
		fmt.Fprintf(os.Stdout, "%s\n", data)
	case "grandchild":
		child := exec.Command(os.Args[0], "-test.run=^TestRecordCLIHelper$", "--", "ignore-term")
		child.Env = os.Environ()
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(96)
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		fmt.Fprintf(os.Stdout, "{\"parent\":%d,\"child\":%d}\n", os.Getpid(), child.Process.Pid)
		<-signals
		_ = child.Process.Kill()
		_ = child.Wait()
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Fprint(os.Stdout, "{\"ready\":true}\n")
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(95)
	}
	os.Exit(0)
}

type recordProcess struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    io.ReadCloser
	stderr bytes.Buffer
	dir    string
	done   chan error
}

func startRecord(t *testing.T, flags []string, mode ...string) *recordProcess {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "capture")
	return startRecordAt(t, dir, flags, mode...)
}
func startRecordAt(t *testing.T, dir string, flags []string, mode ...string) *recordProcess {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	native := append([]string{executable, "-test.run=^TestRecordCLIHelper$", "--"}, mode...)
	return startRecordCommand(t, dir, flags, native)
}
func startRecordCommand(t *testing.T, dir string, flags, native []string) *recordProcess {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	args := []string{"-test.run=^TestRecordCLIHelper$", "--", "record", "--output", dir, "--cleanup-timeout", "0.3", "--env", "AGENT_MOCK_RECORD_TEST_ROLE=peer"}
	args = append(args, flags...)
	args = append(args, "--")
	args = append(args, native...)
	p := &recordProcess{cmd: exec.Command(executable, args...), dir: dir, done: make(chan error, 1)}
	p.cmd.Env = append(os.Environ(), "AGENT_MOCK_RECORD_TEST_ROLE=cli", "RECORD_INHERITED=outer", "GORACE=atexit_sleep_ms=0")
	p.in, err = p.cmd.StdinPipe()
	require.NoError(t, err)
	// An explicit pipe remains readable after Cmd.Wait; StdoutPipe can close
	// before a concurrently waiting test has drained the final bytes.
	out, writeOut, err := os.Pipe()
	require.NoError(t, err)
	p.out = out
	p.cmd.Stdout = writeOut
	p.cmd.Stderr = &p.stderr
	require.NoError(t, p.cmd.Start())
	require.NoError(t, writeOut.Close())
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() { _ = p.in.Close(); _ = p.out.Close(); _ = p.cmd.Process.Kill() })
	return p
}
func (p *recordProcess) wait(t *testing.T, want int) {
	t.Helper()
	select {
	case err := <-p.done:
		if want == 0 {
			require.NoError(t, err, p.stderr.String())
		} else {
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, p.stderr.String())
			assert.Equal(t, want, exit.ExitCode(), p.stderr.String())
		}
	case <-time.After(12 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatal("record CLI did not exit")
	}
}
func (p *recordProcess) reports(t *testing.T) (recording.TransportReport, recording.Manifest) {
	t.Helper()
	var report recording.TransportReport
	data, err := os.ReadFile(filepath.Join(p.dir, "transport.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &report))
	var manifest recording.Manifest
	data, err = os.ReadFile(filepath.Join(p.dir, "manifest.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &manifest))
	assert.Equal(t, report.CaptureStatus, manifest.CaptureStatus)
	assert.Equal(t, recording.CurrentFormat(), manifest.RecordingFormat)
	for name, hash := range manifest.Files {
		data, err = os.ReadFile(filepath.Join(p.dir, name))
		require.NoError(t, err)
		assert.Equal(t, hash, recording.Hash(data), name)
		stat, err := os.Stat(filepath.Join(p.dir, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), stat.Mode().Perm())
	}
	stat, err := os.Stat(p.dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), stat.Mode().Perm())
	assert.True(t, report.CleanupComplete, report.Error)
	return report, manifest
}
func recordReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return data
}

func TestRecordCLIBidirectionalRawAndEOF(t *testing.T) {
	p := startRecord(t, nil, "echo")
	wire := " {\"id\":1e+09,\"large\":9007199254740993,\"unknown\":{\"escaped\":\"\\u0061\"},\"body\":\"" + strings.Repeat("z", 3*1024*1024) + "\"} \r\n"
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(p.in, wire)
		if err == nil {
			err = p.in.Close()
		}
		written <- err
	}()
	output := recordReadAll(t, p.out)
	require.NoError(t, <-written)
	p.wait(t, 0)
	assert.Equal(t, wire+"{\"afterEOF\":true}\n", string(output))
	assert.Empty(t, p.stderr.String())
	report, manifest := p.reports(t)
	assert.Equal(t, "complete", report.CaptureStatus)
	assert.Equal(t, "EOF", report.Reason)
	assert.EqualValues(t, 3, report.Frames)
	assert.True(t, report.Reaped)
	require.NotNil(t, report.NativeExitCode)
	assert.Zero(t, *report.NativeExitCode)
	assert.EqualValues(t, len(wire)*2+len("{\"afterEOF\":true}\n"), report.ProtocolBytes)
	stderr, err := os.ReadFile(filepath.Join(p.dir, "stderr.raw"))
	require.NoError(t, err)
	assert.Equal(t, "native stderr\x00exact\n", string(stderr))
	assert.EqualValues(t, len(stderr), report.StderrBytes)
	raw, err := os.ReadFile(filepath.Join(p.dir, "raw.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, 2, bytes.Count(raw, []byte(strings.TrimSuffix(wire, "\r\n"))))
	assert.Contains(t, manifest.Files, "transport.json")
}
func TestRecordCLIFragmentedAndEarlyExit(t *testing.T) {
	p := startRecord(t, nil, "fragment")
	output := recordReadAll(t, p.out)
	p.wait(t, 0) // client stdin deliberately stays open
	assert.Equal(t, " {\"unknown\":[1e+09,9007199254740993,-0.00],\"escape\":\"\\u0061\"}\r\n", string(output))
	report, _ := p.reports(t)
	assert.EqualValues(t, 1, report.Frames)
}
func TestRecordCLIDuplexBackpressure(t *testing.T) {
	p := startRecord(t, []string{"--timeout", "5"}, "duplex")
	written := make(chan error, 1)
	go func() {
		line := "{\"client\":\"" + strings.Repeat("c", 256*1024) + "\"}\n"
		var err error
		for i := 0; i < 8 && err == nil; i++ {
			_, err = io.WriteString(p.in, line)
		}
		if err == nil {
			err = p.in.Close()
		}
		written <- err
	}()
	output := recordReadAll(t, p.out)
	require.NoError(t, <-written)
	p.wait(t, 0)
	assert.True(t, bytes.HasSuffix(output, []byte("{\"done\":true}\n")))
	report, _ := p.reports(t)
	assert.EqualValues(t, 17, report.Frames)
}
func TestRecordCLIMalformedAndLimits(t *testing.T) {
	cases := []struct {
		name, wire, why string
		flags           []string
		keep            string
	}{
		{"duplicate", "{\"x\":1,\"x\":2}\n", "transport-error", nil, "{\"x\":1,\"x\":2}\n"},
		{"unicode", "{\"x\":\"\\ud800\"}\n", "transport-error", nil, "{\"x\":\"\\ud800\"}\n"},
		{"non-object", "[1,2]\n", "transport-error", nil, "[1,2]\n"},
		{"bad-json", "{broken}\n", "transport-error", nil, "{broken}\n"},
		{"unterminated", "{\"x\":1}", "transport-error", nil, "{\"x\":1}"},
		{"frame-limit", "{}\n{}\n", "limit", []string{"--max-frames", "1"}, "{}\n"},
		{"byte-limit", "{\"abcd\":12345}\n", "limit", []string{"--protocol-bytes", "5"}, "{\"abc"},
		{"payload-limit", "{\"abcd\":12345}\n", "limit", []string{"--max-frame-bytes", "5"}, "{\"abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := startRecord(t, tc.flags, "emit", tc.wire)
			_ = recordReadAll(t, p.out)
			p.wait(t, 1)
			report, manifest := p.reports(t)
			assert.Equal(t, "failed", report.CaptureStatus)
			assert.Equal(t, tc.why, report.Reason)
			partial, err := os.ReadFile(filepath.Join(p.dir, "s2c.partial.raw"))
			require.NoError(t, err)
			assert.Equal(t, tc.keep, string(partial))
			assert.Contains(t, manifest.Files, "s2c.partial.raw")
		})
	}
}
func TestRecordCLIInclusivePayloadAndBytes(t *testing.T) {
	p := startRecord(t, []string{"--max-frame-bytes", "2", "--protocol-bytes", "4", "--max-frames", "1"}, "emit", "{}\r\n")
	assert.Equal(t, "{}\r\n", string(recordReadAll(t, p.out)))
	p.wait(t, 0)
	p.reports(t)
}
func TestRecordCLIIncompleteClient(t *testing.T) {
	p := startRecord(t, nil, "echo")
	_, err := io.WriteString(p.in, "{\"partial\":")
	require.NoError(t, err)
	require.NoError(t, p.in.Close())
	_ = recordReadAll(t, p.out)
	p.wait(t, 1)
	report, manifest := p.reports(t)
	assert.Equal(t, "transport-error", report.Reason)
	assert.Contains(t, manifest.Files, "c2s.partial.raw")
	data, err := os.ReadFile(filepath.Join(p.dir, "c2s.partial.raw"))
	require.NoError(t, err)
	assert.Equal(t, "{\"partial\":", string(data))
}
func TestRecordCLINonzeroExit(t *testing.T) {
	p := startRecord(t, nil, "nonzero")
	assert.Equal(t, "{\"beforeExit\":true}\n", string(recordReadAll(t, p.out)))
	p.wait(t, 1)
	report, _ := p.reports(t)
	assert.Equal(t, "native-error", report.Reason)
	require.NotNil(t, report.NativeExitCode)
	assert.Equal(t, 17, *report.NativeExitCode)
}
func TestRecordCLIDeadlineHalfCloseAndBlockedIO(t *testing.T) {
	for _, mode := range []string{"idle", "stdout-closed", "no-read", "flood", "ignore-term"} {
		t.Run(mode, func(t *testing.T) {
			p := startRecord(t, []string{"--timeout", "0.25"}, mode)
			var writer chan error
			if mode == "no-read" {
				writer = make(chan error, 1)
				go func() {
					_, err := io.WriteString(p.in, "{\"large\":\""+strings.Repeat("x", 2*1024*1024)+"\"}\n")
					writer <- err
				}()
			}
			// Do not drain stdout: flood must cancel while its client write is blocked.
			p.wait(t, 3)
			if writer != nil {
				select {
				case <-writer:
				case <-time.After(time.Second):
					t.Fatal("client writer remained blocked")
				}
			}
			report, _ := p.reports(t)
			assert.Equal(t, "deadline", report.Reason)
			assert.Equal(t, "failed", report.CaptureStatus)
			assert.True(t, report.Reaped)
		})
	}
}
func TestRecordCLICancelCleansGroup(t *testing.T) {
	p := startRecord(t, []string{"--cleanup-timeout", "1"}, "grandchild")
	line, err := bufio.NewReader(p.out).ReadBytes('\n')
	require.NoError(t, err)
	var ids struct{ Parent, Child int }
	require.NoError(t, json.Unmarshal(line, &ids))
	require.Positive(t, ids.Parent)
	require.Positive(t, ids.Child)
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	_ = recordReadAll(t, p.out)
	p.wait(t, 3)
	report, _ := p.reports(t)
	assert.Equal(t, "canceled", report.Reason)
	assert.ErrorIs(t, syscall.Kill(ids.Parent, 0), syscall.ESRCH)
	assert.ErrorIs(t, syscall.Kill(ids.Child, 0), syscall.ESRCH)
}
func TestRecordCLIIdentityAndEnvironment(t *testing.T) {
	cwd := t.TempDir()
	p := startRecord(t, []string{"--cwd", cwd, "--clear-env", "--env", "RECORD_OVERRIDE=private-value", "--env", "RECORD_EMPTY=", "--native-version", "caller-label"}, "identity")
	output := recordReadAll(t, p.out)
	p.wait(t, 0)
	var got map[string]string
	require.NoError(t, json.Unmarshal(output, &got))
	resolved, err := filepath.EvalSymlinks(cwd)
	require.NoError(t, err)
	assert.Equal(t, resolved, got["cwd"])
	assert.Equal(t, "private-value", got["override"])
	assert.Empty(t, got["empty"])
	assert.Empty(t, got["inherited"])
	_, manifest := p.reports(t)
	assert.Equal(t, resolved, manifest.Native.CWD)
	assert.Equal(t, "caller-label", manifest.Native.Version)
	executable, err := os.ReadFile(manifest.Native.Executable)
	require.NoError(t, err)
	assert.Equal(t, recording.Hash(executable), manifest.Native.SHA256)
	data, err := os.ReadFile(filepath.Join(p.dir, "manifest.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "private-value")
}
func TestRecordCLIExclusiveOutput(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "keep")
	require.NoError(t, os.WriteFile(sentinel, []byte("untouched"), 0600))
	p := startRecordAt(t, dir, nil, "echo")
	_ = recordReadAll(t, p.out)
	p.wait(t, 2)
	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "untouched", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}
func TestRecordCLIClosedClientOutput(t *testing.T) {
	p := startRecord(t, nil, "flood")
	require.NoError(t, p.out.Close())
	p.wait(t, 1)
	report, _ := p.reports(t)
	assert.Equal(t, "transport-error", report.Reason)
	assert.Equal(t, "failed", report.CaptureStatus)
}
func TestRecordCLIStderrLimit(t *testing.T) {
	p := startRecord(t, []string{"--stderr-bytes", "4"}, "echo")
	require.NoError(t, p.in.Close())
	_ = recordReadAll(t, p.out)
	p.wait(t, 1)
	report, _ := p.reports(t)
	assert.Equal(t, "limit", report.Reason)
	data, err := os.ReadFile(filepath.Join(p.dir, "stderr.raw"))
	require.NoError(t, err)
	assert.Equal(t, "nati", string(data))
}
func TestRecordCLIArtifactFailure(t *testing.T) {
	p := startRecord(t, nil, "idle")
	_, err := bufio.NewReader(p.out).ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(p.dir, "transport.json"), 0700))
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
	_ = recordReadAll(t, p.out)
	p.wait(t, 3)
	assert.Contains(t, p.stderr.String(), "write transport.json")
	data, err := os.ReadFile(filepath.Join(p.dir, "manifest.json"))
	require.NoError(t, err)
	var manifest recording.Manifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	assert.Equal(t, "failed", manifest.CaptureStatus)
	assert.FileExists(t, filepath.Join(p.dir, "raw.jsonl"))
}

func TestRecordCLIZeroFrames(t *testing.T) {
	p := startRecord(t, nil, "emit", "")
	assert.Empty(t, recordReadAll(t, p.out))
	p.wait(t, 0)
	report, _ := p.reports(t)
	assert.EqualValues(t, 0, report.Frames)
	assert.Equal(t, "complete", report.CaptureStatus)
}
func TestRecordCLIPreflightAndFailedLaunch(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "capture")
		p := startRecordCommand(t, dir, nil, []string{filepath.Join(t.TempDir(), "does-not-exist")})
		assert.Empty(t, recordReadAll(t, p.out))
		p.wait(t, 2)
		assert.NoDirExists(t, dir)
	})
	t.Run("launch", func(t *testing.T) {
		executable := filepath.Join(t.TempDir(), "missing-interpreter")
		require.NoError(t, os.WriteFile(executable, []byte("#!/no/such/agent-sdk-testify-interpreter\n"), 0700))
		p := startRecordCommand(t, filepath.Join(t.TempDir(), "capture"), nil, []string{executable})
		assert.Empty(t, recordReadAll(t, p.out))
		p.wait(t, 1)
		report, manifest := p.reports(t)
		assert.Equal(t, "launch-error", report.Reason)
		assert.False(t, report.NativeLaunched)
		assert.False(t, report.Reaped)
		assert.Nil(t, report.NativeExitCode)
		assert.NotEmpty(t, manifest.Native.SHA256)
	})
}
func TestRecordCLIManifestFailureCannotClaimComplete(t *testing.T) {
	p := startRecord(t, nil, "echo")
	_, err := io.WriteString(p.in, "{}\n")
	require.NoError(t, err)
	_, err = bufio.NewReader(p.out).ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(p.dir, "manifest.json"), 0700))
	require.NoError(t, p.in.Close())
	_ = recordReadAll(t, p.out)
	p.wait(t, 1)
	data, err := os.ReadFile(filepath.Join(p.dir, "transport.json"))
	require.NoError(t, err)
	var report recording.TransportReport
	require.NoError(t, json.Unmarshal(data, &report))
	assert.Equal(t, "failed", report.CaptureStatus)
	assert.Equal(t, "transport-error", report.Reason)
	assert.Contains(t, report.Error, "write manifest.json")
}
func TestRecordCLIRejectsBadClientAndPreservesPriorFrame(t *testing.T) {
	p := startRecord(t, nil, "echo")
	_, err := io.WriteString(p.in, "{\"ok\":1}\n{\"duplicate\":0,\"duplicate\":1}\n")
	require.NoError(t, err)
	_ = recordReadAll(t, p.out)
	p.wait(t, 1)
	report, manifest := p.reports(t)
	assert.Equal(t, "transport-error", report.Reason)
	assert.Contains(t, report.Error, "c2s frame")
	raw, err := os.ReadFile(filepath.Join(p.dir, "raw.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "{\"ok\":1}")
	assert.NotContains(t, string(raw), "duplicate")
	assert.Contains(t, manifest.Files, "c2s.partial.raw")
}
func TestRecordCLICancelRetainsPartialInput(t *testing.T) {
	p := startRecord(t, []string{"--timeout", "0.3"}, "idle")
	_, err := bufio.NewReader(p.out).ReadString('\n')
	require.NoError(t, err)
	_, err = io.WriteString(p.in, "{\"pending\":")
	require.NoError(t, err)
	_ = recordReadAll(t, p.out)
	p.wait(t, 3)
	report, manifest := p.reports(t)
	assert.Equal(t, "deadline", report.Reason)
	assert.Contains(t, manifest.Files, "c2s.partial.raw")
	data, err := os.ReadFile(filepath.Join(p.dir, "c2s.partial.raw"))
	require.NoError(t, err)
	assert.Equal(t, "{\"pending\":", string(data))
}

func TestRecordCLICancelEscalatesToKill(t *testing.T) {
	p := startRecord(t, []string{"--cleanup-timeout", "0.05"}, "ignore-term")
	_, err := bufio.NewReader(p.out).ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, p.cmd.Process.Signal(syscall.SIGINT))
	_ = recordReadAll(t, p.out)
	p.wait(t, 3)
	report, _ := p.reports(t)
	assert.Equal(t, "canceled", report.Reason)
	assert.Nil(t, report.NativeExitCode)
	assert.Equal(t, syscall.SIGKILL.String(), report.NativeSignal)
}

func TestRecordCLIEarlyExitRejectsPendingClientFragment(t *testing.T) {
	p := startRecord(t, nil, "one-line")
	_, err := io.WriteString(p.in, "{}\n{\"partial\":")
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(recordReadAll(t, p.out)))
	p.wait(t, 1) // Native exit must interrupt the still-open client input.
	report, manifest := p.reports(t)
	assert.Equal(t, "transport-error", report.Reason)
	require.NotNil(t, report.NativeExitCode)
	assert.Zero(t, *report.NativeExitCode)
	assert.Contains(t, manifest.Files, "c2s.partial.raw")
	data, err := os.ReadFile(filepath.Join(p.dir, "c2s.partial.raw"))
	require.NoError(t, err)
	assert.Equal(t, "{\"partial\":", string(data))
}

// Exercise the same protocol dispatcher with an instrumented subprocess.
func protocolTestMain() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := Execute(ctx, os.Args[1:], command.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
	cancel()
	os.Exit(code)
}
