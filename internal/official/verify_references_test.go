package official

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPeerExitRequiresSuccessfulReapedCleanup(t *testing.T) {
	started, finished := tool.Object{"status": "running"}, tool.Object{"status": "passed"}
	require.NoError(t, validatePeerExit(started, finished, tool.Object{"started": true, "exitCode": float64(0), "reaped": true, "cleanupComplete": true}))
	for _, tc := range []struct {
		name   string
		change tool.Object
	}{
		{"bad lifecycle", tool.Object{"exitCode": float64(9)}},
		{"not started", tool.Object{"started": false}},
		{"not reaped", tool.Object{"reaped": false}},
		{"no cleanup", tool.Object{"cleanupComplete": false}},
		{"error", tool.Object{"error": "synthetic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			process := tool.Object{"started": true, "exitCode": float64(0), "reaped": true, "cleanupComplete": true}
			maps.Copy(process, tc.change)
			require.ErrorContains(t, validatePeerExit(started, finished, process), "protocol process failed or was not reaped")
		})
	}
}
