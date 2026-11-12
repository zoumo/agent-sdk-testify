package corpus

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestBinaryUsesItselfOutsideCheckout(t *testing.T) {
	cmd, _, fixtureDir := verifyFixture(t, "passed")
	binary := filepath.Join(t.TempDir(), "agent-sdk-testify")
	require.NoError(t, tool.CopyFile(os.Getenv("AGENT_SDK_TESTIFY_BIN"), binary))
	require.NoError(t, os.Chmod(binary, 0o700))
	cmd.Path = binary
	cmd.Args[0] = binary
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	output := verifyOutput(t, fixtureDir)
	var peer tool.Command
	require.NoError(t, tool.ReadJSON(filepath.Join(peerRequestRoot(t, fixtureDir, output), "peer-main.json"), &peer))
	require.Equal(t, binary, peer.Command)
	require.Equal(t, []string{"agent", "mock"}, peer.Args[:2])
	prepared := filepath.Join(t.TempDir(), "fixture")
	out, err := exec.CommandContext(t.Context(), binary, "prepare", "--fixture-dir", prepared, "--agent", "codex", "--case", "SDK-CASE-001").CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.FileExists(t, filepath.Join(prepared, "fixture.json"))
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(prepared, "fixture.json"), &descriptor))
	require.Equal(t, "SDK-CASE-001", tool.Obj(descriptor["source"])["id"])
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(prepared, "runtime")) })
}

func peerRequestRoot(t *testing.T, fixtureDir, verification string) string {
	t.Helper()
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
	return filepath.Join(fixtureDir, "runtime", "official", filepath.Base(verification))
}
