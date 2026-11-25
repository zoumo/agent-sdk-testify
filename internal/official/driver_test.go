package official

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveDriverUsesInstalledExecutableTarget(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	t.Setenv("PATH", bin)
	cli := filepath.Join(root, "cli.js")
	require.NoError(t, os.WriteFile(cli, []byte("#!/usr/bin/env node\n"), 0o700))
	require.NoError(t, os.Symlink("../cli.js", filepath.Join(bin, "agent-sdk-testify-claude")))
	want, err := filepath.EvalSymlinks(cli)
	require.NoError(t, err)

	resolved, err := ResolveDriver("claude")
	require.NoError(t, err)
	require.Equal(t, want, resolved)
}
