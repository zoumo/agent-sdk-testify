package official

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestRemoteCleanupReceiptExplicitlyExcludesForcedCleanup(t *testing.T) {
	root := t.TempDir()
	log, err := os.Create(filepath.Join(root, "stderr.log"))
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "synthetic-remote-cleanup")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", ready)
	service := &remoteService{
		cmd: cmd, done: make(chan error, 1), log: log,
		root: root, token: filepath.Join(root, "token.private"),
	}
	go func() { service.done <- cmd.Wait() }()
	require.NoError(t, service.close())
	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "remote-service.json"), &receipt))
	require.Equal(t, true, receipt["cleanupComplete"])
	require.InDelta(t, float64(0), receipt["serverExitCode"], 0)
	require.Equal(t, false, receipt["forcedCleanup"])
}
