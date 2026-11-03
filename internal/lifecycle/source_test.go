package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPlanAndRecordingRejectIgnoredModelSelection(t *testing.T) {
	root := t.TempDir()
	plan := filepath.Join(root, "plan.json")
	require.NoError(t, os.WriteFile(plan, []byte(`{"version":1,"agentType":"codex","caseId":"synthetic","driverProfile":"consumer","project":"/synthetic/project"}`+"\n"), 0o600))

	_, err := resolvePlan(PrepareOptions{Plan: plan, Model: "ignored"})
	require.ErrorContains(t, err, "model belongs to the consumer plan")

	_, err = resolveRecording(PrepareOptions{Recording: filepath.Join(root, "recording"), Model: "ignored"})
	require.ErrorContains(t, err, "model selection belongs to the recording source")
}

func TestRecordingIdentityUsesManifestAndRejectsConflict(t *testing.T) {
	manifest := tool.Object{"agentType": "claude"}
	rawManifest := tool.Object{
		"native": tool.Object{"executable": "/opt/native/claude"},
	}
	agent, err := materialManifestAgentType(manifest, rawManifest)
	require.NoError(t, err)
	require.Equal(t, "claude", agent)

	_, err = materialManifestAgentType(
		tool.Object{"agentType": "claude"},
		tool.Object{"provider": "codex"},
	)
	require.ErrorContains(t, err, "conflicting agent identities")
}
