package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestLoadPlanRejectsChangedRecordingSource(t *testing.T) {
	root := t.TempDir()
	plan := []byte("{\"provider\":\"codex\",\"caseId\":\"synthetic\"}\n")
	manifest := []byte("{\"captureStatus\":\"complete\"}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), plan, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0o600))
	fixture := &Fixture{
		Source: Source{
			Kind:   "recording",
			Path:   root,
			SHA256: tool.DigestBytes([]byte(tool.DigestBytes(plan) + "\n" + tool.DigestBytes(manifest) + "\n")),
		},
	}
	_, err := LoadPlan(fixture)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), []byte("{}\n"), 0o600))
	_, err = LoadPlan(fixture)
	require.ErrorContains(t, err, "fixture source changed after prepare")
}
