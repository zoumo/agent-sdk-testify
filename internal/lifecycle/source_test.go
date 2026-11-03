package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
