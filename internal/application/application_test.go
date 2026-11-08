package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPrepareRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(existing, 0o700))
	for _, test := range []struct {
		name    string
		options PrepareOptions
		want    string
	}{
		{
			name: "missing fixture",
			want: "prepare requires --fixture-dir",
		},
		{
			name:    "relative fixture",
			options: PrepareOptions{FixtureDir: "relative"},
			want:    "--fixture-dir must be an absolute path",
		},
		{
			name:    "negative limit",
			options: PrepareOptions{FixtureDir: filepath.Join(root, "new"), ProtocolBytes: -1},
			want:    "prepare limits must be nonnegative",
		},
		{
			name:    "existing fixture",
			options: PrepareOptions{FixtureDir: existing},
			want:    "--fixture-dir already exists: " + existing,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Prepare(test.options)
			require.EqualError(t, err, test.want)
		})
	}
}

func TestExportRequiresAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name    string
		options ExportOptions
	}{
		{name: "missing paths"},
		{name: "relative output", options: ExportOptions{FixtureDir: root, Output: "relative"}},
		{name: "relative fixture", options: ExportOptions{FixtureDir: "relative", Output: filepath.Join(root, "output")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.EqualError(t, Export(test.options), "export requires absolute --fixture-dir and --output paths")
		})
	}
}

func TestRecordRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	fixtureDir := filepath.Join(root, "fixture")
	command := filepath.Join(root, "agent")
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	valid := RecordOptions{
		FixtureDir: fixtureDir,
		Command:    command,
		NativeArgs: []string{"app-server"},
		ResultFD:   -1,
	}
	for _, test := range []struct {
		name   string
		mutate func(*RecordOptions)
		want   string
	}{
		{
			name:   "missing local arguments",
			mutate: func(o *RecordOptions) { o.NativeArgs = nil },
			want:   "local record requires --cmd and native arguments after --",
		},
		{
			name: "remote command",
			mutate: func(o *RecordOptions) {
				o.URL = "ws://example.invalid"
			},
			want: "remote target excludes --cmd and native arguments",
		},
		{
			name: "remote native arguments",
			mutate: func(o *RecordOptions) {
				o.URL, o.Command = "ws://example.invalid", ""
			},
			want: "remote target excludes --cmd and native arguments",
		},
		{
			name:   "relative fixture",
			mutate: func(o *RecordOptions) { o.FixtureDir = "relative" },
			want:   "--fixture-dir must be absolute",
		},
		{
			name:   "negative timeout",
			mutate: func(o *RecordOptions) { o.Timeout = -time.Second },
			want:   "timeouts must be nonnegative",
		},
		{
			name:   "local connect timeout",
			mutate: func(o *RecordOptions) { o.ConnectTimeout = time.Second },
			want:   "connect-timeout requires a remote target",
		},
		{
			name:   "result descriptor",
			mutate: func(o *RecordOptions) { o.ResultFD = 2 },
			want:   "result-fd must be >=3",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			options.NativeArgs = append([]string(nil), valid.NativeArgs...)
			test.mutate(&options)
			_, err := Record(t.Context(), options, Streams{})
			require.EqualError(t, err, test.want)
		})
	}
}

func TestRecordRequiresStreamsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "agent")
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	options := RecordOptions{
		FixtureDir: filepath.Join(root, "fixture"),
		Command:    command,
		NativeArgs: []string{"app-server"},
		ResultFD:   -1,
	}
	_, err := Record(t.Context(), options, Streams{})
	require.EqualError(t, err, "record requires input, output and error streams")
}

func TestMockRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name    string
		options MockOptions
		want    string
	}{
		{
			name:    "result descriptor",
			options: MockOptions{ResultFD: 2, ProgressFD: -1},
			want:    "result-fd must be >=3",
		},
		{
			name:    "shared notification descriptor",
			options: MockOptions{ResultFD: 3, ProgressFD: 3},
			want:    "result-fd and progress-fd must use separate descriptors",
		},
		{
			name:    "missing fixture",
			options: MockOptions{ResultFD: -1, ProgressFD: -1},
			want:    "mock requires --fixture-dir",
		},
		{
			name:    "relative fixture",
			options: MockOptions{FixtureDir: "relative", ResultFD: -1, ProgressFD: -1},
			want:    "--fixture-dir must be an absolute path",
		},
		{
			name:    "negative timeout",
			options: MockOptions{FixtureDir: filepath.Join(root, "fixture"), Timeout: -time.Second, ResultFD: -1, ProgressFD: -1},
			want:    "mock timeout must be nonnegative",
		},
		{
			name:    "progress descriptor",
			options: MockOptions{FixtureDir: filepath.Join(root, "fixture"), ResultFD: -1, ProgressFD: 2},
			want:    "progress-fd must be >=3",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Mock(t.Context(), test.options, Streams{})
			require.EqualError(t, err, test.want)
		})
	}
}

func TestMockRequiresStreamsBeforeExecution(t *testing.T) {
	options := MockOptions{FixtureDir: filepath.Join(t.TempDir(), "fixture"), ResultFD: -1, ProgressFD: -1}
	_, err := Mock(t.Context(), options, Streams{})
	require.EqualError(t, err, "mock requires input, output and error streams")
}

func TestApplicationRecordPreflightOwnsTargetAndFixtureInputs(t *testing.T) {
	root := t.TempDir()
	isolateApplicationEnvironment(t, root)
	fixtureDir := prepareApplicationPlanFixture(t, root)
	fixtureDir, err := filepath.EvalSymlinks(fixtureDir)
	require.NoError(t, err)
	executable := filepath.Join(root, "agent")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	require.NoError(t, err)

	prepared, err := prepareRecord(RecordOptions{
		FixtureDir: fixtureDir,
		Command:    executable,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		Timeout:    time.Second,
		ResultFD:   -1,
	})
	require.NoError(t, err)
	require.Equal(t, fixtureDir, prepared.FixtureDir)
	require.Equal(t, "main", prepared.Connection)
	require.Equal(t, resolvedExecutable, prepared.Command)
	require.Equal(t, "local", prepared.Target)
	require.Equal(t, []string{"--fixture-dir", fixtureDir, "--cmd", resolvedExecutable, "--", "app-server", "--listen", "stdio://"}, prepared.Args)

	_, err = prepareRecord(RecordOptions{
		FixtureDir: fixtureDir,
		Command:    executable,
		NativeArgs: []string{"unsupported"},
		ResultFD:   -1,
	})
	require.ErrorContains(t, err, `unsupported native startup argument "unsupported"`)

	_, err = prepareRecord(RecordOptions{
		FixtureDir: fixtureDir,
		Connection: "probe",
		Command:    executable,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		ResultFD:   -1,
	})
	require.EqualError(t, err, "undeclared record connection probe")

	tokenFile := filepath.Join(root, "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("synthetic-token\n"), 0o600))
	remote, err := prepareRecord(RecordOptions{
		FixtureDir: fixtureDir,
		URL:        "ws://example.invalid",
		TokenFile:  tokenFile,
		ResultFD:   -1,
	})
	require.NoError(t, err)
	require.NotNil(t, remote.Remote)
	require.Equal(t, "ws://example.invalid", remote.Remote.URL)
	require.Empty(t, remote.Command)
	require.Len(t, remote.Target, 64)
	require.Equal(t, []string{"--fixture-dir", fixtureDir, "--url", "<private>", "--token-file", "<private>"}, remote.Args)
}

func TestApplicationMockPreflightSelectsCurrentAndValidatesConnection(t *testing.T) {
	root := t.TempDir()
	isolateApplicationEnvironment(t, root)
	fixtureDir := filepath.Join(root, "fixture")
	_, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)
	fixtureDir, err = filepath.EvalSymlinks(fixtureDir)
	require.NoError(t, err)

	prepared, err := prepareMock(MockOptions{
		FixtureDir: fixtureDir,
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		ResultFD:   -1,
		ProgressFD: -1,
	})
	require.NoError(t, err)
	require.Equal(t, fixtureDir, prepared.FixtureDir)
	require.Equal(t, "main", prepared.Connection)
	require.Equal(t, "codex", prepared.Agent)
	require.NotEmpty(t, prepared.Artifact.RecordingSHA256)

	_, err = prepareMock(MockOptions{
		FixtureDir: fixtureDir,
		Connection: "probe",
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
		ResultFD:   -1,
		ProgressFD: -1,
	})
	require.EqualError(t, err, "undeclared mock connection probe")

	_, err = prepareMock(MockOptions{
		FixtureDir: fixtureDir,
		NativeArgs: []string{"unsupported"},
		ResultFD:   -1,
		ProgressFD: -1,
	})
	require.ErrorContains(t, err, `unsupported native startup argument "unsupported"`)

	emptyFixture := filepath.Join(root, "empty-fixture")
	plan := filepath.Join(root, "plan.json")
	data, err := json.Marshal(map[string]any{
		"version":       1,
		"agentType":     "codex",
		"caseId":        "application-boundary",
		"driverProfile": "consumer",
		"project":       filepath.Join(root, "project"),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(plan, append(data, '\n'), 0o600))
	_, err = Prepare(PrepareOptions{FixtureDir: emptyFixture, Plan: plan})
	require.NoError(t, err)
	_, err = prepareMock(MockOptions{FixtureDir: emptyFixture, ResultFD: -1, ProgressFD: -1})
	require.EqualError(t, err, "fixture has no current recording")
}

func isolateApplicationEnvironment(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "home"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "config"), 0o700))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "config"))
}

func prepareApplicationPlanFixture(t *testing.T, root string) string {
	t.Helper()
	plan := filepath.Join(root, "record-plan.json")
	data, err := json.Marshal(map[string]any{
		"version":       1,
		"agentType":     "codex",
		"caseId":        "application-record",
		"driverProfile": "consumer",
		"project":       filepath.Join(root, "project"),
		"scenario": map[string]any{
			"files": map[string]string{"fixture.txt": "fixture\n"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(plan, append(data, '\n'), 0o600))
	fixtureDir := filepath.Join(root, "fixture")
	_, err = Prepare(PrepareOptions{FixtureDir: fixtureDir, Plan: plan})
	require.NoError(t, err)
	return fixtureDir
}
