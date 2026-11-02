package command_test

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/command"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func invoke(t *testing.T, args []string, handlers command.Handlers) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	in, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	require.NoError(t, err)
	defer out.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	require.NoError(t, err)
	defer stderr.Close()
	code := command.Execute(context.Background(), args, handlers, command.IO{In: in, Out: out, Err: stderr})
	a, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	b, err := os.ReadFile(stderr.Name())
	require.NoError(t, err)
	return code, string(a), string(b)
}

func TestHelpVersionAndExplicitUnimplemented(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"record", "--help"}, {"mock", "--help"}, {"version"}, {"--version"}} {
		t.Run(args[0]+args[len(args)-1], func(t *testing.T) {
			code, out, stderr := invoke(t, args, command.Handlers{})
			assert.Zero(t, code)
			assert.NotEmpty(t, out)
			assert.Empty(t, stderr)
		})
	}
	for _, args := range [][]string{{"record", "--output", "new", "--", "never-executed"}, {"mock", "--provider", "codex", "--scenario", "never-read"}} {
		code, out, stderr := invoke(t, args, command.Handlers{})
		assert.Equal(t, command.ExitUnimplemented, code)
		assert.Empty(t, out)
		assert.Contains(t, stderr, "not implemented")
	}
}

func TestRecordOptions(t *testing.T) {
	called := false
	h := command.Handlers{Record: func(_ context.Context, o command.RecordOptions, _ command.IO) error {
		called = true
		assert.Equal(t, "out", o.Output)
		assert.Equal(t, "project", o.CWD)
		assert.Equal(t, []string{"KEY=value", "EMPTY="}, o.Env)
		assert.True(t, o.ClearEnv)
		assert.Equal(t, []string{"program", "--native", "arg"}, o.Command)
		assert.Equal(t, 125*time.Millisecond, o.Timeout)
		assert.Equal(t, 2*time.Second, o.CleanupTimeout)
		assert.Equal(t, int64(0), o.ProtocolBytes)
		assert.Equal(t, int64(12), o.MaxFrames)
		assert.Equal(t, "caller-version", o.NativeVersion)
		assert.Equal(t, "close", o.StdinEOFAction)
		return nil
	}}
	code, out, stderr := invoke(t, []string{"record", "--output", "out", "--cwd", "project", "--env", "KEY=value", "--env", "EMPTY=", "--clear-env", "--timeout", "0.125", "--max-frames", "12", "--native-version", "caller-version", "--", "program", "--native", "arg"}, h)
	assert.Zero(t, code)
	assert.Empty(t, out)
	assert.Empty(t, stderr)
	assert.True(t, called)
}

func TestMockOptionsWithInterleavedStartupArguments(t *testing.T) {
	h := command.Handlers{Mock: func(_ context.Context, o command.MockOptions, _ command.IO) error {
		assert.Equal(t, "codex", o.Provider)
		assert.Equal(t, "input", o.Scenario)
		assert.Equal(t, "receipt", o.Receipt)
		assert.Equal(t, []string{"app-server", "--listen=stdio://"}, o.NativeArgs)
		assert.Equal(t, "answer-and-assert", o.Handshakes)
		assert.Equal(t, 3, o.ProgressFD)
		assert.Equal(t, 5, o.Limits.MaxFrames)
		assert.Equal(t, int64(8<<20), o.Limits.MaxBytes)
		assert.Equal(t, int64(2<<20), o.Limits.MaxFrameBytes)
		assert.Equal(t, 100*time.Millisecond, o.Timeout)
		return nil
	}}
	code, out, stderr := invoke(t, []string{"mock", "app-server", "--provider", "codex", "--listen=stdio://", "--scenario", "input", "--receipt", "receipt", "--sdk-test-handshakes", "answer-and-assert", "--progress-fd", "3", "--max-frames", "5", "--timeout", "0.1"}, h)
	assert.Zero(t, code)
	assert.Empty(t, out)
	assert.Empty(t, stderr)
	code, _, stderr = invoke(t, []string{"mock", "--provider", "claude", "--scenario", "input", "--recording-version", "2.1.258", "--", "--output-format", "stream-json", "--model=x"}, command.Handlers{Mock: func(_ context.Context, o command.MockOptions, _ command.IO) error {
		assert.Equal(t, "2.1.258", o.RecordingVersion)
		assert.Zero(t, o.Timeout)
		assert.Equal(t, int64(16<<20), o.Limits.MaxBytes)
		assert.Equal(t, -1, o.ProgressFD)
		return nil
	}})
	assert.Zero(t, code, stderr)
}

func TestInvalidArgumentsNeverReachHandlers(t *testing.T) {
	h := command.Handlers{Record: func(context.Context, command.RecordOptions, command.IO) error { t.Error("record called"); return nil }, Mock: func(context.Context, command.MockOptions, command.IO) error { t.Error("mock called"); return nil }}
	cases := [][]string{
		{"unknown"}, {"--typo"}, {"version", "extra"}, {"record"}, {"record", "--output", "x", "program"},
		{"record", "--output", "x", "before", "--", "program"}, {"record", "--output", "x", "--timeout", "NaN", "--", "x"},
		{"record", "--output", "x", "--protocol-bytes", "-1", "--", "x"}, {"record", "--output", "x", "--cleanup-timeout", "0", "--", "x"},
		{"record", "--output", "x", "--env", "bad", "--", "x"}, {"record", "--output", "x", "--env", "A=1", "--env", "A=2", "--", "x"},
		{"record", "--output", "x", "--stdin-eof-action", "ignore", "--", "x"},
		{"mock"}, {"mock", "--provider", "unknown", "--scenario", "x"}, {"mock", "--provider", "codex", "--scenario"},
	}
	for _, extra := range [][]string{{"--timeout", "0"}, {"--timeout", "-1"}, {"--timeout", "NaN"}, {"--timeout", "Inf"}, {"--timeout", "1e30"}, {"--max-frames", "0"}, {"--max-frames", "10001"}, {"--max-bytes", "0"}, {"--max-frame-bytes", "0"}, {"--progress-fd", "1"}, {"--progress-fd", "-2"}, {"--sdk-test-handshakes", "other"}, {"--unknown"}, {"--listen", "ws://localhost"}, {"app-server", "app-server"}} {
		cases = append(cases, append([]string{"mock", "--provider", "codex", "--scenario", "x"}, extra...))
	}
	cases = append(cases, []string{"mock", "--provider", "claude", "--scenario", "x", "--sdk-test-handshakes", "answer"})
	for _, args := range cases {
		t.Run(fmtArgs(args), func(t *testing.T) {
			code, out, stderr := invoke(t, args, h)
			assert.Equal(t, command.ExitConfiguration, code)
			assert.Empty(t, out)
			assert.NotEmpty(t, stderr)
		})
	}
}
func fmtArgs(args []string) string {
	result := ""
	for _, arg := range args {
		result += arg + " "
	}
	return result
}

func TestNativeArgumentContract(t *testing.T) {
	for _, tt := range []struct {
		provider string
		args     []string
		valid    bool
	}{
		{"codex", nil, true}, {"claude", nil, true}, {"unknown", nil, false},
		{"codex", []string{"app-server", "--listen", "stdio://"}, true},
		{"claude", []string{"--output-format", "stream-json", "--input-format=stream-json", "--permission-prompt-tool", "stdio", "--verbose", "--include-partial-messages", "--model=x", "--resume=y"}, true},
		{"claude", []string{"--verbose", "--verbose"}, false}, {"claude", []string{"--model", "x"}, false}, {"claude", []string{"--output-format"}, false},
		{"claude", []string{"--dangerously-skip-permissions"}, false}, {"codex", []string{"app-server=x"}, false},
	} {
		t.Run(tt.provider+fmtArgs(tt.args), func(t *testing.T) {
			err := command.ValidateNativeArgs(tt.provider, tt.args)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestRuntimeErrorsRetainExitClassification(t *testing.T) {
	for _, code := range []int{1, 2, 3, 4, 5} {
		got, out, stderr := invoke(t, []string{"mock", "--provider", "codex", "--scenario", "x"}, command.Handlers{Mock: func(context.Context, command.MockOptions, command.IO) error {
			return command.Fail(code, errors.New("synthetic error"))
		}})
		assert.Equal(t, code, got)
		assert.Empty(t, out)
		assert.Contains(t, stderr, "synthetic error")
	}
	got, _, _ := invoke(t, []string{"record", "--output", "x", "--", "x"}, command.Handlers{Record: func(context.Context, command.RecordOptions, command.IO) error { return errors.New("I/O error") }})
	assert.Equal(t, command.ExitFailure, got)
}
