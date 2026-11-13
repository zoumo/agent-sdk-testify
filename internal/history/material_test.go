package history

import (
	"os"
	"path/filepath"
	"regexp"
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

func TestMaterializeClaudeExecutionHistory(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	name := "config/projects/-native-workspace/session.jsonl"
	original := "{\"cwd\":\"/native/workspace\",\"id\":\"keep\",\"future\":1.2500}\n"
	digest := writeHistoryFile(t, source, name, original)
	writeHistoryJSON(t, source, "plan.json", tool.Object{"provider": "claude", "project": "/route/workspace", "_fixtureProject": "/native/workspace"})
	writeHistoryJSON(t, source, "manifest.json", tool.Object{"savedFiles": tool.Object{name: digest}})
	bindings, err := Materialize(source, target)
	require.NoError(t, err)
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(filepath.Join(target, "workspace"), "-")
	derived := filepath.Join("config", "projects", key, "session.jsonl")
	require.Contains(t, bindings, derived)
	require.NoError(t, Check(target, bindings))
	data, err := os.ReadFile(filepath.Join(source, name))
	require.NoError(t, err)
	require.Equal(t, original, string(data))
	require.NoError(t, os.WriteFile(filepath.Join(target, derived), []byte("changed"), 0o600))
	require.Error(t, Check(target, bindings))
}

func TestMaterializeLegacyStartupUsesPublishedWorkspace(t *testing.T) {
	source, staging := t.TempDir(), t.TempDir()
	workspace := filepath.Join(t.TempDir(), "published", "runtime", "routes", "record", "workspace")
	name := "config/projects/-original/session.jsonl"
	original := "{\"cwd\":\"/original\",\"sessionId\":\"session\",\"future\":1.2500}\n"
	digest := writeHistoryFile(t, source, name, original)
	writeHistoryJSON(t, source, "plan.json", tool.Object{"provider": "claude", "project": "/original"})
	writeHistoryJSON(t, source, "manifest.json", tool.Object{"savedFiles": tool.Object{name: digest}})
	files, err := MaterializeStartup(source, staging, workspace)
	require.NoError(t, err)
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(workspace, "-")
	derived := filepath.Join("config", "projects", key, "session.jsonl")
	require.Contains(t, files, derived)
	var row tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(staging, derived), &row))
	require.Equal(t, workspace, row["cwd"])
	data, err := os.ReadFile(filepath.Join(staging, derived))
	require.NoError(t, err)
	require.Contains(t, string(data), "1.2500")
	data, err = os.ReadFile(filepath.Join(source, name))
	require.NoError(t, err)
	require.Equal(t, original, string(data))
	require.NoError(t, Check(staging, files))
	require.NoFileExists(t, filepath.Join(staging, "materialization.json"))
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
