package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPersistentAttemptsKeepFailureAndNeverOverwrite(t *testing.T) {
	data, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("AGENT_SDK_TESTIFY_DATA_DIR", data)
	o := sample(t, "failed")
	o.Output = ""
	first, err := run(context.Background(), o)
	require.ErrorContains(t, err, "driver reported failed")
	path := str(first["output"])
	require.Equal(t, filepath.Join(data, "attempts", "verify"), filepath.Dir(path))
	before, err := os.ReadFile(filepath.Join(path, "summary.json"))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(path, "result.json"))
	require.FileExists(t, filepath.Join(path, "replay-peer.json"))
	second, err := run(context.Background(), o)
	require.Error(t, err)
	require.NotEqual(t, path, second["output"])
	o.Output = path
	_, err = run(context.Background(), o)
	require.ErrorIs(t, err, os.ErrExist)
	after, err := os.ReadFile(filepath.Join(path, "summary.json"))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestPersistentDataRootDefaults(t *testing.T) {
	t.Setenv("AGENT_SDK_TESTIFY_DATA_DIR", "")
	for _, xdg := range []bool{true, false} {
		t.Run(map[bool]string{true: "xdg", false: "home"}[xdg], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_DATA_HOME", "")
			base := filepath.Join(root, ".local", "share")
			if xdg {
				base = filepath.Join(root, "data")
				t.Setenv("XDG_DATA_HOME", base)
			}
			path, err := createOutput("", "record")
			require.NoError(t, err)
			require.Equal(t, filepath.Join(base, "agent-sdk-testify", "attempts", "record"), filepath.Dir(path))
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0700), info.Mode().Perm())
		})
	}
}
