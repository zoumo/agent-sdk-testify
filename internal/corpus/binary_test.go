package corpus

import (
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBinaryUsesItselfOutsideCheckout(t *testing.T) {
	cmd, _, output := verifyFixture(t, "passed")
	binary := filepath.Join(t.TempDir(), "agent-sdk-testify")
	require.NoError(t, copyFile(os.Getenv("AGENT_SDK_TESTIFY_BIN"), binary))
	require.NoError(t, os.Chmod(binary, 0700))
	cmd.Path = binary
	cmd.Args[0] = binary
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var peer command
	require.NoError(t, readJSON(filepath.Join(output, "peer-main.json"), &peer))
	require.Equal(t, binary, peer.Command)
	require.Equal(t, "--internal-protocol", peer.Args[0])
	out, err := exec.Command(binary, "mock", "--list", "--json").CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(out), "SDK-CASE-001")
}
