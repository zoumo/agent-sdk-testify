package application

import (
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
			name: "missing session",
			want: "prepare requires --session-dir",
		},
		{
			name:    "relative session",
			options: PrepareOptions{SessionDir: "relative"},
			want:    "--session-dir must be an absolute path",
		},
		{
			name:    "negative limit",
			options: PrepareOptions{SessionDir: filepath.Join(root, "new"), ProtocolBytes: -1},
			want:    "prepare limits must be nonnegative",
		},
		{
			name:    "existing session",
			options: PrepareOptions{SessionDir: existing},
			want:    "--session-dir already exists: " + existing,
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
		{name: "relative output", options: ExportOptions{SessionDir: root, Output: "relative"}},
		{name: "relative session", options: ExportOptions{SessionDir: "relative", Output: filepath.Join(root, "output")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.EqualError(t, Export(test.options), "export requires absolute --session-dir and --output paths")
		})
	}
}

func TestRecordRejectsInvalidInput(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session")
	command := filepath.Join(root, "agent")
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	valid := RecordOptions{
		SessionDir: sessionDir,
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
			name:   "relative session",
			mutate: func(o *RecordOptions) { o.SessionDir = "relative" },
			want:   "--session-dir must be absolute",
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
			require.EqualError(t, Record(t.Context(), options, Streams{}), test.want)
		})
	}
}

func TestRecordRequiresStreamsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "agent")
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	options := RecordOptions{
		SessionDir: filepath.Join(root, "session"),
		Command:    command,
		NativeArgs: []string{"app-server"},
		ResultFD:   -1,
	}
	require.EqualError(t, Record(t.Context(), options, Streams{}), "record requires input, output and error streams")
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
			name:    "missing session",
			options: MockOptions{ResultFD: -1, ProgressFD: -1},
			want:    "mock requires --session-dir",
		},
		{
			name:    "relative session",
			options: MockOptions{SessionDir: "relative", ResultFD: -1, ProgressFD: -1},
			want:    "--session-dir must be an absolute path",
		},
		{
			name:    "negative timeout",
			options: MockOptions{SessionDir: filepath.Join(root, "session"), Timeout: -time.Second, ResultFD: -1, ProgressFD: -1},
			want:    "mock timeout must be nonnegative",
		},
		{
			name:    "progress descriptor",
			options: MockOptions{SessionDir: filepath.Join(root, "session"), ResultFD: -1, ProgressFD: 2},
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
	options := MockOptions{SessionDir: filepath.Join(t.TempDir(), "session"), ResultFD: -1, ProgressFD: -1}
	_, err := Mock(t.Context(), options, Streams{})
	require.EqualError(t, err, "mock requires input, output and error streams")
}
