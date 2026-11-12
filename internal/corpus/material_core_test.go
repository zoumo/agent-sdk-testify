package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func formatThreeCore(t *testing.T) Material {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	source := []byte("{\"version\":1,\"agentType\":\"codex\",\"caseId\":\"synthetic\",\"driverProfile\":\"consumer\",\"project\":\"/recorded/project\",\"unknown\":{\"large\":9007199254740993}}\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "source-plan.json"), source, 0o600))
	plan, err := MaterialPlanBytes(source, tool.Object{
		"sourcePlanSHA256": tool.DigestBytes(source), "historyBindings": []any{},
	})
	require.NoError(t, err)
	require.Contains(t, string(plan), "9007199254740993")
	for name, data := range map[string][]byte{
		"plan.json": plan, "raw/raw.jsonl": []byte("{}\n"),
		"raw/stderr.raw": {}, "raw/manifest.json": []byte("{}\n"),
		"raw/transport.json": []byte("{}\n"), "report.json": []byte("{}\n"),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), data, 0o600))
	}
	planHash := tool.DigestBytes(plan)
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"provenance": "consumer", "sourcePlanSHA256": tool.DigestBytes(source), "planSHA256": planHash,
	}))
	require.NoError(t, SealCoreInventory(root))
	manifestHash, err := tool.FileDigest(filepath.Join(root, "manifest.json"))
	require.NoError(t, err)
	return Material{
		FormatVersion: 3, ID: "synthetic-material", Kind: "capture", Action: "record",
		Path: root, Provenance: "consumer", Connections: []string{"main"},
		SourcePlanSHA256: tool.DigestBytes(source), PlanSHA256: planHash,
		ManifestSHA256: manifestHash, RecordingSHA256: tool.DigestBytes([]byte("{}\n")),
	}
}

func TestFormatThreeInventoryAndDescriptorHaveOneOwner(t *testing.T) {
	material := formatThreeCore(t)
	require.NoError(t, ValidateCore(material.Path, material))
	require.NoError(t, tool.WriteJSON(filepath.Join(material.Path, "material.json"), material))
	read, err := ReadMaterialDescriptor(material.Path)
	require.NoError(t, err)
	require.True(t, material.SameDescriptor(*read))
	var wire tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(material.Path, "material.json"), &wire))
	require.NotContains(t, wire, "files")
	require.NotContains(t, wire, "path")
	require.NotContains(t, wire, "officialOrigin")
	require.Contains(t, wire, "sourcePlanSHA256")
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(material.Path, "manifest.json"), &manifest))
	files := tool.Obj(manifest["files"])
	require.Contains(t, files, "raw/stderr.raw")
	require.NotContains(t, files, "manifest.json")
	require.NotContains(t, files, "material.json")
	require.NotContains(t, manifest, "savedFiles")
}

func TestFormatThreeRejectsBrokenCore(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *Material)
	}{
		{"missing file", func(t *testing.T, m *Material) {
			require.NoError(t, os.Remove(filepath.Join(m.Path, "raw", "stderr.raw")))
		}},
		{"extra file", func(t *testing.T, m *Material) {
			require.NoError(t, os.WriteFile(filepath.Join(m.Path, "extra"), nil, 0o600))
		}},
		{"source hash", func(_ *testing.T, m *Material) { m.SourcePlanSHA256 = tool.DigestBytes(nil) }},
		{"derived hash", func(_ *testing.T, m *Material) { m.PlanSHA256 = m.SourcePlanSHA256 }},
		{"symlink", func(t *testing.T, m *Material) {
			require.NoError(t, os.Symlink(filepath.Join(m.Path, "raw"), filepath.Join(m.Path, "linked")))
		}},
		{"corrupt source", func(t *testing.T, m *Material) {
			require.NoError(t, os.WriteFile(filepath.Join(m.Path, "source-plan.json"), []byte("{}"), 0o600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			material := formatThreeCore(t)
			tc.mutate(t, &material)
			require.Error(t, ValidateCore(material.Path, material))
		})
	}
}

func TestFormatThreeRejectsLegacyInlineFields(t *testing.T) {
	for _, field := range []string{`"files":{"plan.json":"hash"}`, `"officialOrigin":{"runId":"run"}`, `"path":"/machine"`, `"files":{}`, `"officialOrigin":null`, `"path":""`} {
		var m Material
		require.Error(t, json.Unmarshal([]byte(`{"formatVersion":3,`+field+`}`), &m))
	}
}

func TestFormatThreeOriginUsesOneCanonicalObject(t *testing.T) {
	for _, fault := range []string{"", "origin hash", "embedded origin", "unknown proof version", "independent origin version", "wrong run"} {
		t.Run(fault, func(t *testing.T) {
			m := formatThreeCore(t)
			source := m.SourcePlanSHA256
			origin := OfficialOrigin{RunID: "run", FixtureID: "fixture", CaseID: "synthetic", AgentType: "codex", RecordSourceSHA256: source, SourceSHA256: source, DriverSHA256: tool.DigestBytes(nil), Connections: []string{"main"}}
			var plan tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(m.Path, "plan.json"), &plan))
			plan["provider"] = "codex"
			require.NoError(t, tool.WriteJSON(filepath.Join(m.Path, "plan.json"), plan))
			require.NoError(t, tool.WriteJSON(filepath.Join(m.Path, "official-origin.json"), origin))
			if fault == "independent origin version" {
				var fields tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(m.Path, "official-origin.json"), &fields))
				fields["schemaVersion"] = 1
				require.NoError(t, tool.WriteJSON(filepath.Join(m.Path, "official-origin.json"), fields))
			}
			hash, err := tool.FileDigest(filepath.Join(m.Path, "official-origin.json"))
			require.NoError(t, err)
			m.OriginSHA256 = hash
			proof := tool.Object{"schemaVersion": 2, "originSHA256": hash, "runId": "run", "executionId": "execution", "connectionId": "main", "protocolPid": 1, "supervisorPid": 2, "toolSHA256": tool.DigestBytes(nil)}
			require.NoError(t, os.MkdirAll(filepath.Join(m.Path, "origin"), 0o700))
			switch fault {
			case "origin hash":
				proof["originSHA256"] = tool.DigestBytes(nil)
			case "embedded origin":
				proof["origin"] = tool.Object{}
			case "unknown proof version":
				proof["schemaVersion"] = 3
			case "wrong run":
				proof["runId"] = "other"
			}
			require.NoError(t, tool.WriteJSON(filepath.Join(m.Path, "origin", "main.json"), proof))
			if fault == "" {
				require.NoError(t, ValidateOfficialCapture(m))
			} else {
				require.Error(t, ValidateOfficialCapture(m))
			}
		})
	}
}

func TestRecordingLayoutClassificationRejectsConflicts(t *testing.T) {
	material := formatThreeCore(t)
	require.NoError(t, tool.WriteJSON(filepath.Join(material.Path, "material.json"), material))
	root, full, err := RecordingRoot(material.Path)
	require.NoError(t, err)
	require.False(t, full)
	require.Equal(t, material.Path, root)
	pkg := t.TempDir()
	require.NoError(t, os.Rename(material.Path, filepath.Join(pkg, "material")))
	root, full, err = RecordingRoot(pkg)
	require.NoError(t, err)
	require.True(t, full)
	require.Equal(t, filepath.Join(pkg, "material"), root)
	require.NoError(t, os.WriteFile(filepath.Join(pkg, "plan.json"), []byte("{}"), 0o600))
	_, _, err = RecordingRoot(pkg)
	require.ErrorContains(t, err, "conflicting")
}
