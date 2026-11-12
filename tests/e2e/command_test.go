package conformance

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/version"
)

func TestPublicCommands(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	out, stderr, code := runCLI(t, binary, nil, "--help")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	rootHelp := string(out)
	for _, name := range []string{"prepare", "agent", "official", "export"} {
		require.Contains(t, rootHelp, name)
	}
	for _, name := range []string{"internal-protocol", "internal-peer", "completion", "finish"} {
		require.NotContains(t, rootHelp, name)
	}
	require.NotContains(t, strings.ToLower(rootHelp), "replay and finish")
	for _, name := range []string{"simulate", "replay", "corpus", "protocol", "peer", "record", "mock", "verify", "internal-protocol", "finish"} {
		out, stderr, code = runCLI(t, binary, nil, name)
		require.Equal(t, 1, code, string(out))
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
		expected := os.Getenv("AGENT_SDK_TESTIFY_VERSION")
		if expected == "" {
			expected = version.String()
		}
		require.Equal(t, "agent-sdk-testify "+expected+"\n", string(out))
	}

	out, stderr, code = runCLI(t, binary, nil, "--internal-peer", "--help")
	require.Zero(t, code, stderr)
	require.Empty(t, stderr)
	require.Contains(t, string(out), "--request")
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
		{name: "configuration", source: invalid, stderr: "unsupported scenario version", code: 1},
		{name: "canceled", source: waiting, stderr: "deadline exceeded", code: 1, extra: []string{"--timeout", "0.05"}, holdInput: true},
		{name: "disconnect", source: disconnect, stderr: "scripted disconnect", code: 1},
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
			fixtureDir := prepareSyntheticMock(t, binary, tc.source)
			args := []string{"agent", "mock", "--fixture-dir", fixtureDir}
			args = append(args, tc.extra...)
			out, stderr, code := runCLI(t, binary, input, args...)
			require.Equal(t, tc.code, code, stderr)
			require.Equal(t, tc.stdout, string(out))
			require.Contains(t, stderr, tc.stderr)
		})
	}
}

func TestSinglePreparedFixtureCLIContract(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	for _, command := range [][]string{{"prepare"}, {"agent", "record"}, {"official", "record"}, {"official", "verify"}, {"agent", "mock"}} {
		out, stderr, code := runCLI(t, binary, nil, append(command, "--help")...)
		require.Zero(t, code, stderr)
		help := string(out)
		for _, retired := range []string{"--prepare", "--run", "--finish", "--output", "--provider", "--consumer", "--materials"} {
			require.NotContains(t, help, retired)
		}
	}
	out, stderr, code := runCLI(t, binary, nil, "agent", "mock", "--help")
	require.Zero(t, code, stderr)
	require.Contains(t, string(out), "current recording")
	require.NotContains(t, string(out), "managed mock alias")
	require.NotContains(t, strings.ToLower(string(out)), "compatibility alias")

	for _, args := range [][]string{
		{"agent", "mock", "--prepare"},
		{"agent", "mock", "--run", "/absolute/run.json"},
		{"agent", "mock", "--finish", "/absolute/run.json"},
		{"agent", "mock", "--provider", "codex", "--case", "SDK-CASE-001", "--report", "/tmp/report.json"},
	} {
		out, stderr, code = runCLI(t, binary, nil, args...)
		require.Equal(t, 1, code, stderr)
		require.Empty(t, out)
		require.NotContains(t, strings.ToLower(stderr), "legacy")
	}

	root := t.TempDir()
	out, stderr, code = runCLI(t, binary, nil,
		"official", "verify", "--recording", filepath.Join(root, "capture"), "--output", filepath.Join(root, "output"))
	require.Equal(t, 1, code, stderr)
	require.Empty(t, out)
	require.Contains(t, stderr, "unknown flag: --recording")

	out, stderr, code = runCLI(t, binary, nil, "official", "verify", "--recording", filepath.Join(root, "capture"))
	require.Equal(t, 1, code, stderr)
	require.Empty(t, out)
	require.Contains(t, stderr, "unknown flag: --recording")
}

func TestPublicCommandParseFailuresDoNotStartHandlers(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	marker := filepath.Join(t.TempDir(), "launched")
	launcher := filepath.Join(t.TempDir(), "launcher")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700))
	for _, extra := range [][]string{{"--bogus"}, {"--timeout", "not-a-duration"}} {
		args := append([]string{"official", "record", "--fixture-dir", t.TempDir(), "--cmd", launcher}, extra...)
		args = append(args, "--", "app-server")
		out, stderr, code := runCLI(t, binary, nil, args...)
		require.Equal(t, 1, code)
		require.Empty(t, out)
		require.NotEmpty(t, stderr)
		require.Contains(t, stderr, "agent-sdk-testify:")
		require.NotContains(t, stderr, "Usage:")
		require.NoFileExists(t, marker)
	}
}

func TestPublicCommandFixtureErrors(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	root := t.TempDir()
	missing := filepath.Join(root, "missing-fixture")
	occupied := filepath.Join(root, "occupied-fixture")
	require.NoError(t, os.Mkdir(occupied, 0o700))
	launcher := filepath.Join(root, "launcher")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0o700))

	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "prepare-existing-fixture",
			args: []string{"prepare", "--fixture-dir", occupied, "--agent", "codex", "--case", "SDK-CASE-008"},
		},
		{
			name: "record-missing-fixture",
			args: []string{"official", "record", "--fixture-dir", missing, "--cmd", launcher, "--", "app-server"},
		},
		{
			name: "verify-missing-fixture",
			args: []string{"official", "verify", "--fixture-dir", missing},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := runCLI(t, binary, nil, tc.args...)
			require.Equal(t, 1, code, stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "agent-sdk-testify:")
			require.NotContains(t, stderr, "Usage:")
		})
	}
}

func TestRetiredSessionDirFlagRejected(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "prepare", args: []string{"prepare", "--agent", "codex", "--case", "SDK-CASE-001"}},
		{name: "agent-record", args: []string{"agent", "record"}},
		{name: "agent-mock", args: []string{"agent", "mock"}},
		{name: "official-record", args: []string{"official", "record"}},
		{name: "official-verify", args: []string{"official", "verify"}},
		{name: "export", args: []string{"export", "--output", filepath.Join(t.TempDir(), "output")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "fixture")
			args := append(append([]string{}, test.args...), "--session-dir", fixture)
			out, stderr, code := runCLI(t, binary, nil, args...)
			require.Equal(t, 1, code, stderr)
			require.Empty(t, out)
			require.NotContains(t, stderr, "Usage:")
			require.NoDirExists(t, fixture)
			if test.name != "agent-mock" {
				require.Contains(t, stderr, "unknown flag: --session-dir")
			}
		})
	}
}

func TestRemovedProtocolNamespaceRejected(t *testing.T) {
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
			require.Equal(t, 1, code, stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "agent-sdk-testify:")
			require.NotContains(t, stderr, "Usage:")
		})
	}
}

func prepareSyntheticMock(t *testing.T, binary, source string) string {
	t.Helper()
	root := t.TempDir()
	material := filepath.Join(root, "material")
	require.NoError(t, os.MkdirAll(filepath.Join(material, "raw"), 0o700))
	raw := readFile(t, source)
	require.NoError(t, os.WriteFile(filepath.Join(material, "raw", "raw.jsonl"), raw, 0o600))
	writeJSON(t, filepath.Join(material, "plan.json"), object{"provider": "codex", "caseId": "synthetic-protocol", "driverProfile": "continuation", "project": filepath.Join(root, "project"), "scenario": object{"files": object{}}})
	writeJSON(t, filepath.Join(material, "manifest.json"), object{"provenance": "synthetic", "planSHA256": digestFile(t, filepath.Join(material, "plan.json")), "files": object{"raw.jsonl": digestBytes(raw)}})
	return prepareFixture(t, nil, "--recording", material)
}
