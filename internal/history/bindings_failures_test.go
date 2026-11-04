package history

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type syntheticMaterialChange func(*testing.T, string, tool.Object)

func TestBindingsRejectUnboundMissingDriftLinksAndEscapes(t *testing.T) {
	const history = "config/sessions/history.jsonl"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		mutate  syntheticMaterialChange
		message string
	}{
		{
			name:    "unbound",
			files:   map[string]string{history: "bound\n"},
			message: "unbound history",
			mutate: func(t *testing.T, _ string, manifest tool.Object) {
				delete(tool.Obj(manifest["savedFiles"]), history)
			},
		},
		{
			name:    "missing",
			files:   map[string]string{history: "bound\n"},
			message: "history.jsonl",
			mutate: func(t *testing.T, root string, _ tool.Object) {
				require.NoError(t, os.Remove(filepath.Join(root, history)))
			},
		},
		{
			name:    "drift",
			files:   map[string]string{history: "bound\n"},
			message: "hash mismatch",
			mutate: func(t *testing.T, root string, _ tool.Object) {
				require.NoError(t, os.WriteFile(filepath.Join(root, history), []byte("changed\n"), 0o600))
			},
		},
		{
			name:    "parent symlink",
			files:   map[string]string{"config/sessions/target/history.jsonl": "bound\n"},
			message: "symlink",
			mutate: func(t *testing.T, root string, _ tool.Object) {
				target := filepath.Join(root, "config", "sessions", "target")
				linked := filepath.Join(root, "config", "sessions", "linked")
				require.NoError(t, os.Rename(target, linked))
				require.NoError(t, os.Symlink(linked, target))
			},
		},
		{
			name:    "path escape",
			files:   map[string]string{history: "bound\n"},
			message: "escapes material",
			mutate: func(t *testing.T, root string, manifest tool.Object) {
				outside := filepath.Join(filepath.Dir(root), "outside.jsonl")
				require.NoError(t, os.WriteFile(outside, []byte("outside\n"), 0o600))
				tool.Obj(manifest["savedFiles"])["../outside.jsonl"] = tool.DigestBytes([]byte("outside\n"))
			},
		},
		{
			name:    "unapproved subtree",
			files:   map[string]string{"config/auth.json": "secret\n"},
			message: "outside allowed history",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			saved := tool.Object{}
			for name, content := range tc.files {
				saved[name] = writeHistoryFile(t, root, name, content)
			}
			writeHistoryJSON(t, root, "plan.json", tool.Object{})
			writeHistoryJSON(t, root, "manifest.json", tool.Object{"savedFiles": saved})
			if tc.mutate != nil {
				var manifest tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest))
				tc.mutate(t, root, manifest)
				require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest))
			}
			_, err := Bindings(root)
			require.ErrorContains(t, err, tc.message)
		})
	}
}
