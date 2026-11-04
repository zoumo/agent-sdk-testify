package native_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	nativeprep "github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestRemoteCleanupReceiptExplicitlyExcludesForcedCleanup(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	service, err := nativeprep.ReserveRecordService(ctx, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = service.Close() })
	self, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, service.Start(ctx, nativeprep.Options{RuntimeEnv: map[string]string{"GORACE": os.Getenv("GORACE")}}, tool.Object{"cli": self, "project": root, "args": []string{"synthetic-remote-cleanup"}}))
	require.Positive(t, service.ProcessID())
	require.False(t, service.Stopped())
	require.NoError(t, service.Close())
	require.True(t, service.Stopped())
	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "remote-service.json"), &receipt))
	require.Equal(t, true, receipt["cleanupComplete"])
	require.InDelta(t, float64(0), receipt["serverExitCode"], 0)
	require.Equal(t, false, receipt["forcedCleanup"])
	require.NoFileExists(t, service.TokenFile())
}
