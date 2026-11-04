package tool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateCommand(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "agent")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700))
	plain := filepath.Join(root, "plain")
	require.NoError(t, os.WriteFile(plain, nil, 0o600))
	link := filepath.Join(root, "agent-link")
	require.NoError(t, os.Symlink(executable, link))
	broken := filepath.Join(root, "broken")
	require.NoError(t, os.Symlink(filepath.Join(root, "missing"), broken))
	resolved, err := filepath.EvalSymlinks(executable)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path, want string
	}{
		{"empty", "", "--cmd must be an absolute executable path"},
		{"relative", "agent", "--cmd must be an absolute executable path"},
		{"missing", filepath.Join(root, "missing"), "no such file"},
		{"directory", root, "--cmd must be a regular executable"},
		{"not-executable", plain, "--cmd must be a regular executable"},
		{"broken-symlink", broken, "no such file"},
		{"executable", executable, ""},
		{"symlink", link, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := ValidateCommand(tc.path)
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				require.Empty(t, actual)
				return
			}
			require.NoError(t, err)
			require.Equal(t, resolved, actual)
		})
	}
}
