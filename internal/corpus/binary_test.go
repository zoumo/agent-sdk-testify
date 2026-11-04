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
	cmd, _, session := verifyFixture(t, "passed")
	binary := filepath.Join(t.TempDir(), "agent-sdk-testify")
	require.NoError(t, tool.CopyFile(os.Getenv("AGENT_SDK_TESTIFY_BIN"), binary))
	require.NoError(t, os.Chmod(binary, 0o700))
	cmd.Path = binary
	cmd.Args[0] = binary
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	output := verifyOutput(t, session)
	var peer tool.Command
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "peer-main.json"), &peer))
	require.Equal(t, binary, peer.Command)
	require.Equal(t, "--internal-protocol", peer.Args[0])
	prepared := filepath.Join(t.TempDir(), "session")
	out, err := exec.CommandContext(t.Context(), binary, "prepare", "--session-dir", prepared, "--agent", "codex", "--case", "SDK-CASE-001").CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.FileExists(t, filepath.Join(prepared, "session.json"))
	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(prepared, "session.json"), &descriptor))
	require.Equal(t, "SDK-CASE-001", tool.Obj(descriptor["source"])["id"])
	t.Cleanup(func() { _ = os.RemoveAll(tool.Str(descriptor["resourceRoot"])) })
}
