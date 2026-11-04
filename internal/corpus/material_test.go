package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

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
	origin := &OfficialOrigin{RunID: "original-run", SessionID: "original-session", CaseID: "synthetic", AgentType: "codex", SourceSHA256: digest, DriverSHA256: strings.Repeat("d", 64), Connections: []string{connection}}
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
		"status": "passed", "sessionId": material.OfficialOrigin.SessionID, "cleanupComplete": true,
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
