package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadPlanRejectsChangedRecordingSource(t *testing.T) {
	root := t.TempDir()
	plan := []byte("{\"provider\":\"codex\",\"caseId\":\"synthetic\"}\n")
	manifest := []byte("{\"captureStatus\":\"complete\"}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0o600))
	session := &Session{
		Source: Source{
			Kind:   "recording",
			Path:   root,
			SHA256: digestBytes([]byte(digestBytes(plan) + "\n" + digestBytes(manifest) + "\n")),
		},
	}
	_, err := LoadPlan(session)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), []byte("{}\n"), 0o600))
	_, err = LoadPlan(session)
	require.ErrorContains(t, err, "session source changed after prepare")
}
