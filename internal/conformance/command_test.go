package conformance

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicCommands(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	out, stderr, code := runCLI(t, binary, nil, "--help")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	rootHelp := string(out)
	for _, name := range []string{"prepare", "record", "verify", "mock", "finish"} {
		require.Contains(t, rootHelp, name)
	}
	for _, name := range []string{"internal-protocol", "internal-peer", "completion"} {
		require.NotContains(t, rootHelp, name)
	}
	require.NotContains(t, strings.ToLower(rootHelp), "replay and finish")
	for _, name := range []string{"simulate", "replay", "corpus", "protocol", "peer"} {
		out, stderr, code = runCLI(t, binary, nil, name)
		require.Equal(t, 2, code, string(out))
		require.Empty(t, out)
		require.Contains(t, stderr, "unknown command")
	}

	for _, args := range [][]string{
		{"--version"},
		{"version"},
	} {
		out, stderr, code = runCLI(t, binary, nil, args...)
		require.Zero(t, code, stderr)
		require.Empty(t, stderr)
		require.Contains(t, string(out), "agent-sdk-testify 0.1.0-dev")
	}

	out, stderr, code = runCLI(t, binary, nil, "--internal-protocol", "--help")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.Contains(t, string(out), "record")
	require.Contains(t, string(out), "mock")
	require.Contains(t, string(out), "connect")

	out, stderr, code = runCLI(t, binary, nil, "--internal-peer", "--help")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.Contains(t, string(out), "--request")

	out, stderr, code = runCLI(t, binary, nil, "--internal-protocol", "version")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.Contains(t, string(out), "agent-sdk-testify 0.1.0-dev")
}

func TestPublicCommandExitContract(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	scenario := func(name, data string) string {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		return path
	}
	complete := scenario("complete.json", `{"version":1,"provenance":"synthetic","frames":[{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"wire":{}}]}`)
	waiting := scenario("waiting.json", `{"version":1,"provenance":"synthetic","frames":[{"ordinal":1,"direction":"c2s","channel":"protocol","epoch":1,"wire":{}}]}`)
	disconnect := scenario("disconnect.json", `{"version":1,"provenance":"synthetic","frames":[{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"disconnect":true,"wire":null}]}`)
	invalid := scenario("invalid.json", `{"version":2,"provenance":"synthetic","frames":[]}`)
	for _, tc := range []struct {
		name, source, stdout, stderr string
		code                         int
		extra                        []string
		holdInput                    bool
	}{
		{name: "success", source: complete, stdout: "{}\n"},
		{name: "failure", source: waiting, stderr: "early client EOF", code: 1},
		{name: "configuration", source: invalid, stderr: "unsupported scenario version", code: 2},
		{name: "canceled", source: waiting, stderr: "deadline exceeded", code: 3, extra: []string{"--timeout", "0.05"}, holdInput: true},
		{name: "disconnect", source: disconnect, stderr: "scripted disconnect", code: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input io.Reader
			if tc.holdInput {
				r, w, err := os.Pipe()
				require.NoError(t, err)
				t.Cleanup(func() {
					_ = r.Close()
					_ = w.Close()
				})
				input = r
			}
			args := []string{"--internal-protocol", "mock", "--provider", "codex", "--scenario", tc.source}
			args = append(args, tc.extra...)
			out, stderr, code := runCLI(t, binary, input, args...)
			require.Equal(t, tc.code, code, stderr)
			require.Equal(t, tc.stdout, string(out))
			require.Contains(t, stderr, tc.stderr)
		})
	}
}

func TestSinglePreparedSessionCLIContract(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	for _, command := range []string{"prepare", "record", "verify", "mock", "finish"} {
		out, stderr, code := runCLI(t, binary, nil, command, "--help")
		require.Zero(t, code, stderr)
		help := string(out)
		for _, retired := range []string{"--prepare", "--run", "--finish", "--output", "--provider", "--consumer", "--materials"} {
			require.NotContains(t, help, retired)
		}
	}
	out, stderr, code := runCLI(t, binary, nil, "mock", "--help")
	require.Zero(t, code, stderr)
	require.Contains(t, string(out), "managed mock alias")
	require.NotContains(t, strings.ToLower(string(out)), "compatibility alias")

	for _, args := range [][]string{
		{"mock", "--prepare"},
		{"mock", "--run", "/absolute/run.json"},
		{"mock", "--finish", "/absolute/run.json"},
		{"mock", "--provider", "codex", "--case", "SDK-CASE-001", "--report", "/tmp/report.json"},
	} {
		out, stderr, code = runCLI(t, binary, nil, args...)
		require.Equal(t, 2, code, stderr)
		require.Empty(t, out)
		require.NotContains(t, strings.ToLower(stderr), "legacy")
	}

	root := t.TempDir()
	out, stderr, code = runCLI(t, binary, nil,
		"verify", "--recording", filepath.Join(root, "capture"), "--output", filepath.Join(root, "output"))
	require.Equal(t, 2, code, stderr)
	require.Empty(t, out)
	require.Contains(t, stderr, "unknown flag: --output")

	out, stderr, code = runCLI(t, binary, nil, "verify", "--recording", filepath.Join(root, "capture"))
	require.Equal(t, 2, code, stderr)
	require.Empty(t, out)
	require.Contains(t, stderr, "--session-dir")
}

func TestPublicCommandParseFailuresDoNotStartHandlers(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	marker := filepath.Join(t.TempDir(), "launched")
	launcher := filepath.Join(t.TempDir(), "launcher")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700))
	for _, extra := range [][]string{{"--bogus"}, {"--timeout", "not-a-duration"}} {
		args := append([]string{"record", "--session-dir", t.TempDir(), "--cmd", launcher}, extra...)
		args = append(args, "--", "app-server")
		out, stderr, code := runCLI(t, binary, nil, args...)
		require.Equal(t, 2, code)
		require.Empty(t, out)
		require.NotEmpty(t, stderr)
		require.Contains(t, stderr, "Usage:")
		require.NoFileExists(t, marker)
	}
}

func TestPublicCommandConfigurationClassification(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	root := t.TempDir()
	missing := filepath.Join(root, "missing-session")
	occupied := filepath.Join(root, "occupied-session")
	empty := filepath.Join(root, "empty-session")
	require.NoError(t, os.Mkdir(occupied, 0o700))
	require.NoError(t, os.Mkdir(empty, 0o700))
	launcher := filepath.Join(root, "launcher")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "prepare-existing-session",
			args: []string{"prepare", "--session-dir", occupied, "--agent", "codex", "--case", "SDK-CASE-008"},
		},
		{
			name: "record-missing-session",
			args: []string{"record", "--session-dir", missing, "--cmd", launcher, "--", "app-server"},
		},
		{
			name: "verify-missing-session",
			args: []string{"verify", "--session-dir", missing},
		},
		{
			name: "finish-missing-session",
			args: []string{"finish", "--session-dir", missing},
		},
		{
			name: "finish-incomplete-session",
			args: []string{"finish", "--session-dir", empty},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := runCLI(t, binary, nil, tc.args...)
			require.Equal(t, 2, code, stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "Usage:")
		})
	}
}

func TestInternalProtocolArgumentErrors(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	cases := [][]string{
		{"--internal-protocol", "unknown"},
		{"--internal-protocol", "--typo"},
		{"--internal-protocol", "version", "extra"},
		{"--internal-protocol", "record"},
		{"--internal-protocol", "record", "--output", "x", "program"},
		{"--internal-protocol", "record", "--output", "x", "before", "--", "program"},
		{"--internal-protocol", "record", "--output", "x", "--timeout", "NaN", "--", "x"},
		{"--internal-protocol", "record", "--output", "x", "--protocol-bytes", "-1", "--", "x"},
		{"--internal-protocol", "record", "--output", "x", "--cleanup-timeout", "0", "--", "x"},
		{"--internal-protocol", "record", "--output", "x", "--env", "bad", "--", "x"},
		{"--internal-protocol", "record", "--output", "x", "--env", "A=1", "--env", "A=2", "--", "x"},
		{"--internal-protocol", "record", "--output", "x", "--stdin-eof-action", "ignore", "--", "x"},
		{"--internal-protocol", "mock"},
		{"--internal-protocol", "mock", "--provider", "unknown", "--scenario", "x"},
		{"--internal-protocol", "mock", "--provider", "codex", "--scenario"},
	}
	for _, extra := range [][]string{
		{"--timeout", "0"},
		{"--timeout", "-1"},
		{"--timeout", "NaN"},
		{"--timeout", "Inf"},
		{"--timeout", "1e30"},
		{"--max-frames", "0"},
		{"--max-frames", "10001"},
		{"--max-bytes", "0"},
		{"--max-frame-bytes", "0"},
		{"--progress-fd", "1"},
		{"--progress-fd", "-2"},
		{"--sdk-test-handshakes", "other"},
		{"--unknown"},
		{"--listen", "ws://localhost"},
		{"app-server", "app-server"},
	} {
		cases = append(cases, append([]string{"--internal-protocol", "mock", "--provider", "codex", "--scenario", "x"}, extra...))
	}
	cases = append(cases, []string{"--internal-protocol", "mock", "--provider", "claude", "--scenario", "x", "--sdk-test-handshakes", "answer"})
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, stderr, code := runCLI(t, binary, nil, args...)
			require.Equal(t, 2, code, stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "Usage:")
		})
	}
}
