package official

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPersistentDataRootDefaults(t *testing.T) {
	t.Setenv("AGENT_SDK_TESTIFY_DATA_DIR", "")
	for _, xdg := range []bool{true, false} {
		t.Run(map[bool]string{true: "xdg", false: "home"}[xdg], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_DATA_HOME", "")
			base := filepath.Join(root, ".local", "share")
			if xdg {
				base = filepath.Join(root, "data")
				t.Setenv("XDG_DATA_HOME", base)
			}
			path, err := tool.CreateOutput("", "record")
			require.NoError(t, err)
			require.Equal(t, filepath.Join(base, "agent-sdk-testify", "attempts", "record"), filepath.Dir(path))
			info, err := os.Stat(path)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		})
	}
}
