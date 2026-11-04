package tool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyFileRejectsEvidenceSymlink(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	private := filepath.Join(t.TempDir(), "private.txt")
	require.NoError(t, os.WriteFile(private, []byte("private evidence"), 0o600))
	require.NoError(t, os.Symlink(private, filepath.Join(source, "report.json")))
	err := CopyFile(filepath.Join(source, "report.json"), filepath.Join(target, "report.json"))
	require.ErrorContains(t, err, "source is not a regular file")
}
