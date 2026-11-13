package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPortableMaterialOmitsAndRejectsMachineLocalPath(t *testing.T) {
	material := Material{FormatVersion: MaterialFormatVersion, ID: "material", Kind: "capture", Action: "record", Provenance: "consumer", PlanSHA256: "plan", Connections: []string{"main"}}
	material.Path = "/machine/local/material"
	encoded, err := json.Marshal(material)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"path"`)

	var decoded Material
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, MaterialFormatVersion, decoded.FormatVersion)
	require.Empty(t, decoded.Path)

	for _, tc := range []struct {
		name    string
		value   string
		message string
	}{
		{"format two path", `{"formatVersion":2,"id":"material","path":"/machine/local/material"}`, "machine-local path"},
		{"unknown format", `{"formatVersion":4,"id":"material"}`, "unsupported material formatVersion 4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value Material
			require.ErrorContains(t, json.Unmarshal([]byte(tc.value), &value), tc.message)
		})
	}

	var legacy Material
	require.NoError(t, json.Unmarshal([]byte(`{"id":"material","path":"/machine/local/material"}`), &legacy))
	require.Zero(t, legacy.FormatVersion)
	require.Equal(t, "/machine/local/material", legacy.Path)
}

func TestMaterialSameDescriptorIgnoresLocatedPath(t *testing.T) {
	base := Material{FormatVersion: MaterialFormatVersion, ID: "material", Kind: "capture", Action: "record", Provenance: "consumer", PlanSHA256: "plan", ManifestSHA256: "manifest", RecordingSHA256: "recording", Connections: []string{"main"}}
	located := base
	located.Path = "/another/root/materials/material"
	require.True(t, base.SameDescriptor(located))
	changed := located
	changed.RecordingSHA256 = "changed"
	require.False(t, base.SameDescriptor(changed))
}

func TestReadMaterialDescriptorSelectsSupportedFormatsAtCurrentRoot(t *testing.T) {
	root, files := syntheticMaterialPackage(t)
	base := Material{
		ID: "legacy-material", Kind: "material", Action: "mock", Provenance: "consumer",
		PlanSHA256: files["plan.json"], ManifestSHA256: files["manifest.json"],
		RecordingSHA256: files["raw/raw.jsonl"], Connections: []string{"main"}, Files: files,
	}

	t.Run("format two without location", func(t *testing.T) {
		material := base
		material.FormatVersion = 2
		require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), material))

		loaded, err := ReadMaterialDescriptor(root)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		require.Equal(t, 2, loaded.FormatVersion)
		require.Equal(t, root, loaded.Path)
	})

	t.Run("legacy descriptor ignores stored path", func(t *testing.T) {
		legacy := tool.Object{
			"id": base.ID, "kind": base.Kind, "action": base.Action, "provenance": base.Provenance,
			"path": "/stale/machine/material", "planSHA256": base.PlanSHA256,
			"manifestSHA256": base.ManifestSHA256, "recordingSHA256": base.RecordingSHA256,
			"connections": base.Connections, "files": base.Files,
		}
		require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), legacy))

		loaded, err := ReadMaterialDescriptor(root)
		require.NoError(t, err)
		require.NotNil(t, loaded)
		require.Zero(t, loaded.FormatVersion)
		require.Equal(t, root, loaded.Path)
		raw, err := os.ReadFile(filepath.Join(root, "material.json"))
		require.NoError(t, err)
		require.Contains(t, string(raw), `"/stale/machine/material"`)
	})

	t.Run("no descriptor", func(t *testing.T) {
		require.NoError(t, os.Remove(filepath.Join(root, "material.json")))
		loaded, err := ReadMaterialDescriptor(root)
		require.NoError(t, err)
		require.Nil(t, loaded)
	})

	t.Run("unknown format", func(t *testing.T) {
		require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), tool.Object{"formatVersion": 4, "id": base.ID}))
		_, err := ReadMaterialDescriptor(root)
		require.ErrorContains(t, err, "unsupported material formatVersion 4")
	})

	t.Run("missing inventory", func(t *testing.T) {
		require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), tool.Object{"id": base.ID}))
		_, err := ReadMaterialDescriptor(root)
		require.ErrorContains(t, err, "omits its file inventory")
	})

	t.Run("hash drift", func(t *testing.T) {
		require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), base))
		require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte("changed\n"), 0o600))
		_, err := ReadMaterialDescriptor(root)
		require.ErrorContains(t, err, "material closure hash mismatch")
	})

	t.Run("missing file", func(t *testing.T) {
		require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), base))
		require.NoError(t, os.Remove(filepath.Join(root, "raw", "raw.jsonl")))
		_, err := ReadMaterialDescriptor(root)
		require.ErrorContains(t, err, "material closure hash mismatch")
	})
}

func TestDescriptorlessInventoryRequiresNoDescriptor(t *testing.T) {
	root, files := syntheticMaterialPackage(t)
	material := Material{
		ID: "legacy-material", Kind: "material", Action: "mock", Provenance: "consumer",
		PlanSHA256: files["plan.json"], ManifestSHA256: files["manifest.json"],
		RecordingSHA256: files["raw/raw.jsonl"], Connections: []string{"main"}, Files: files,
		Path: root,
	}
	require.NoError(t, CheckMaterialFiles(root, material))
	_, err := InspectMaterialWithoutDescriptor(material)
	require.NoError(t, err)

	require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), material))
	err = CheckMaterialFiles(root, material)
	require.ErrorContains(t, err, "descriptorless material contains material.json")

	require.NoError(t, os.Remove(filepath.Join(root, "material.json")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("added\n"), 0o600))
	err = CheckMaterialFiles(root, material)
	require.ErrorContains(t, err, "material closure hash mismatch")
}

func syntheticMaterialPackage(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "plan.json"), tool.Object{
		"version": 1, "agentType": "codex", "caseId": "legacy-synthetic",
		"driverProfile": "consumer", "project": "/recorded/project",
	}))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"provenance": "consumer", "planSHA256": tool.DigestBytes(mustReadFile(t, filepath.Join(root, "plan.json"))),
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte(`{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","wire":{}}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("legacy evidence\n"), 0o600))
	files, err := history.HashTree(root)
	require.NoError(t, err)
	return root, files
}

func TestVerifyLegacySeedChecksItsCompleteDeclaredInventory(t *testing.T) {
	for _, format := range []int{0, 2} {
		t.Run(fmt.Sprintf("format-%d", format), func(t *testing.T) {
			root, files := syntheticMaterialPackage(t)
			material := Material{
				FormatVersion: format, ID: "legacy-seed", Kind: "material", Action: "mock", Provenance: "consumer",
				PlanSHA256: files["plan.json"], ManifestSHA256: files["manifest.json"], RecordingSHA256: files["raw/raw.jsonl"], Files: files,
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), material))
			require.NoError(t, VerifyMaterials(root, true))
			require.NoError(t, os.Remove(filepath.Join(root, "evidence.txt")))
			require.ErrorContains(t, VerifyMaterials(root, true), "material closure hash mismatch")
		})
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestRecordingIdentityUsesManifestAndRejectsConflict(t *testing.T) {
	manifest := tool.Object{"agentType": "claude"}
	rawManifest := tool.Object{
		"native": tool.Object{"executable": "/opt/native/claude"},
	}
	agent, err := manifestAgentType(manifest, rawManifest)
	require.NoError(t, err)
	require.Equal(t, "claude", agent)

	_, err = manifestAgentType(
		tool.Object{"agentType": "claude"},
		tool.Object{"provider": "codex"},
	)
	require.ErrorContains(t, err, "conflicting agent identities")
}

func TestMaterialSelectionRejectsMissingOrConflictingProvenance(t *testing.T) {
	for _, tc := range []struct {
		name           string
		plan, manifest tool.Object
		message        string
	}{
		{"missing", tool.Object{}, tool.Object{}, "provenance is missing"},
		{"official consumer", tool.Object{"driverProfile": "consumer"}, tool.Object{"provenance": "official"}, "provenance conflicts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SelectionProvenance(tc.plan, tc.manifest)
			require.ErrorContains(t, err, tc.message)
		})
	}
}

func sealedCapture(t *testing.T, connection string) Material {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source-plan.json")
	require.NoError(t, tool.WriteJSON(source, tool.Object{"caseId": "synthetic"}))
	digest, err := tool.FileDigest(source)
	require.NoError(t, err)
	origin := &OfficialOrigin{RunID: "original-run", FixtureID: "original-fixture", CaseID: "synthetic", AgentType: "codex", SourceSHA256: digest, DriverSHA256: strings.Repeat("d", 64), Connections: []string{connection}}
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "official-origin.json"), origin))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "plan.json"), tool.Object{"caseId": origin.CaseID, "provider": origin.AgentType, "sourcePlanSHA256": digest}))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{"sourcePlanSHA256": digest}))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "origin", filepath.Dir(connection)), 0o700))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "origin", connection+".json"), tool.Object{"executionId": "original-execution", "origin": origin, "connectionId": connection, "protocolPid": 10, "supervisorPid": 9, "toolSHA256": strings.Repeat("a", 64)}))
	return Material{ID: "material", Path: root, OfficialOrigin: origin, Connections: []string{connection}, PlanSHA256: "plan", ManifestSHA256: "manifest", RecordingSHA256: "recording"}
}

func TestSealedOwnershipRequiresSingleConnectionName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{{"main", true}, {"nested/main", false}} {
		t.Run(tc.name, func(t *testing.T) {
			material := sealedCapture(t, tc.name)
			err := ValidateOfficialCapture(material)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "invalid official connection identity")
			}
		})
	}
}

func TestSealedOwnershipDoesNotQualifyUnsettledBusiness(t *testing.T) {
	material := sealedCapture(t, "main")
	require.NoError(t, ValidateOfficialCapture(material))
	run := t.TempDir()
	require.Error(t, ValidateOfficialRecord(run, material))
	result := tool.Object{
		"status": "passed", "fixtureId": material.OfficialOrigin.FixtureID, "cleanupComplete": true,
		"driverProcess": tool.Object{"reaped": true, "exitCode": 0}, "business": tool.Object{"status": "failed"},
		"driver":      tool.Object{"sha256": material.OfficialOrigin.DriverSHA256},
		"connections": []any{tool.Object{"connectionId": "main", "material": material, "finished": tool.Object{"status": "passed"}}},
	}
	require.NoError(t, os.Mkdir(filepath.Join(run, "driver"), 0o700))
	require.NoError(t, tool.WriteJSON(filepath.Join(run, "driver-files.json"), map[string]string{}))
	require.NoError(t, tool.WriteJSON(filepath.Join(run, "result.json"), result))
	require.ErrorContains(t, ValidateOfficialRecord(run, material), "no successful settled orchestration")
	result["business"] = tool.Object{"status": "passed"}
	require.NoError(t, tool.WriteJSON(filepath.Join(run, "result.json"), result))
	require.NoError(t, ValidateOfficialRecord(run, material))
	material.RecordingSHA256 = "changed"
	require.ErrorContains(t, ValidateOfficialRecord(run, material), "does not bind exact capture")
}
