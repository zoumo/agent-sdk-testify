package record

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type prepareOncePolicy struct {
	command  string
	prepares int
}

func (p *prepareOncePolicy) Prepare(ctx context.Context, run lifecycle.RecordContext, trackProbe lifecycle.RecordProbeTracker) (tool.Object, error) {
	p.prepares++
	return prepareOfficialCapture(ctx, run, p.command, []string{"app-server"}, nil, trackProbe)
}

func (*prepareOncePolicy) Capture(context.Context, lifecycle.RecordContext) error {
	return errors.New("synthetic capture stop")
}

func (*prepareOncePolicy) Package(context.Context, lifecycle.RecordContext) (*corpus.Material, error) {
	return nil, errors.New("unexpected package")
}

func TestOfficialPreparationRunsNativeProbeOncePerExecution(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
	countPath := filepath.Join(root, "probe-count")
	t.Setenv("PROBE_COUNT", countPath)
	command := filepath.Join(root, "synthetic-agent")
	require.NoError(t, os.WriteFile(command, []byte("#!/bin/sh\nprintf 'probe\\n' >> \"$PROBE_COUNT\"\nprintf 'synthetic-native\\n'\n"), 0o700))

	sessionDir := filepath.Join(root, "session")
	_, err := lifecycle.Prepare(lifecycle.PrepareOptions{SessionDir: sessionDir, Agent: "codex", Case: "SDK-CASE-032"})
	require.NoError(t, err)

	policy := &prepareOncePolicy{command: command}
	for _, connection := range []string{"main", "probe"} {
		err := lifecycle.RunRecord(t.Context(), lifecycle.RecordRequest{
			SessionDir: sessionDir,
			Connection: connection,
			Args:       []string{"--session-dir", sessionDir, "--cmd", command, "--"},
			Timeout:    time.Second,
			Target:     "local",
		}, policy)
		require.ErrorContains(t, err, "synthetic capture stop")
	}

	require.Equal(t, 2, policy.prepares, "both connections must load the effective prepared plan")
	count, err := os.ReadFile(countPath)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(count), "probe\n"))
}
