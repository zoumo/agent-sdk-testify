package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/builtin"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
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
			Path:   filepath.Join(root, "plan.json"),
			SHA256: tool.DigestBytes(plan),
		},
	}
	_, err := LoadPlan(fixture)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(root, "plan.json"), []byte("{}\n"), 0o600))
	_, err = LoadPlan(fixture)
	require.ErrorContains(t, err, "fixture source changed after prepare")
}

func TestBuiltinMaterialPreservesOfficialCaptureOrigin(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, caseID := range []string{"SDK-CASE-001", "SDK-CASE-037", "SDK-CASE-038"} {
			t.Run(provider+"/"+caseID, func(t *testing.T) {
				selected, _, err := builtin.Select(provider, caseID)
				require.NoError(t, err)
				root, err := builtin.Extract(selected)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
				var sealed corpus.OfficialOrigin
				require.NoError(t, tool.ReadJSON(filepath.Join(root, "official-origin.json"), &sealed))
				material, err := artifactForMaterial(root, "builtin", []string{"main"})
				require.NoError(t, err)
				require.Equal(t, &sealed, material.OfficialOrigin)
				require.NoError(t, corpus.ValidateOfficialCapture(*material))
				inventory, err := corpus.Inventory(root, false)
				require.NoError(t, err)
				if caseID != "SDK-CASE-001" {
					require.NotContains(t, inventory, "result.json")
					require.NotContains(t, inventory, "summary.json")
				}
				require.NoError(t, corpus.ValidateVerificationSource(*material, inventory))
				_, err = artifactForMaterial(root, "builtin", []string{"other"})
				require.ErrorContains(t, err, "sealed official capture origin differs")
			})
		}
	}
}
