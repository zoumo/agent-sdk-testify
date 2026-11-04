package official

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestScrubNativeRemovesPrivateInputsAndPreservesHistory(t *testing.T) {
	root := t.TempDir()
	private := []string{"native.env.private.json", "config/auth.json", "config/.credentials.json", "config/config.toml", "config/settings.json"}
	for _, name := range private {
		require.NoError(t, tool.WriteFiles(root, tool.Object{name: "synthetic private input"}))
	}
	const history = "config/sessions/synthetic.jsonl"
	require.NoError(t, tool.WriteFiles(root, tool.Object{history: "original history\n"}))
	require.NoError(t, scrubNative(root))
	for _, name := range private {
		require.NoFileExists(t, filepath.Join(root, name))
	}
	data, err := os.ReadFile(filepath.Join(root, history))
	require.NoError(t, err)
	require.Equal(t, "original history\n", string(data))
	require.NoError(t, scrubNative(root))
}
