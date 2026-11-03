package history

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestBindingsMergeManifestPlanSeedAndAdoption(t *testing.T) {
	root := t.TempDir()
	manifestFile := "config/sessions/manifest.jsonl"
	seedFile := "history-seed/config/sessions/seed.jsonl"
	adoptedFile := "config/projects/adopted.jsonl"
	manifestHash := writeHistoryFile(t, root, manifestFile, "manifest\n")
	seedHash := writeHistoryFile(t, root, seedFile, "seed\n")
	adoptedHash := writeHistoryFile(t, root, adoptedFile, "adopted\n")
	writeHistoryJSON(t, root, "plan.json", tool.Object{
		"historySeed": tool.Object{
			"files": tool.Object{seedFile: tool.Object{"sha256": seedHash}},
		},
	})
	writeHistoryJSON(t, root, "manifest.json", tool.Object{
		"savedFiles": tool.Object{manifestFile: manifestHash},
	})
	writeHistoryJSON(t, root, "adoption.json", tool.Object{
		"adoption":        "admitted",
		"transformations": []any{},
		"files":           tool.Object{adoptedFile: adoptedHash},
	})

	bindings, err := Bindings(root)
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		manifestFile: manifestHash,
		seedFile:     seedHash,
		adoptedFile:  adoptedHash,
	}, bindings)
}

func TestBindingsRejectConflictingSources(t *testing.T) {
	root := t.TempDir()
	name := "config/sessions/conflict.jsonl"
	actual := writeHistoryFile(t, root, name, "actual\n")
	writeHistoryJSON(t, root, "plan.json", tool.Object{
		"historySeed": tool.Object{
			"files": tool.Object{name: tool.Object{"sha256": tool.DigestBytes([]byte("plan\n"))}},
		},
	})
	writeHistoryJSON(t, root, "manifest.json", tool.Object{
		"savedFiles": tool.Object{name: actual},
	})

	_, err := Bindings(root)
	require.ErrorContains(t, err, "history bindings conflict")
}

func writeHistoryFile(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return tool.DigestBytes([]byte(content))
}

func writeHistoryJSON(t *testing.T, root, name string, value any) {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, tool.WriteJSON(path, value))
}
