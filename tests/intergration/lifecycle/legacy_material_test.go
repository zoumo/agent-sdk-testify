package lifecycle_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestLegacyDescriptorlessImportMocksAndExportsWithoutSource(t *testing.T) {
	root := t.TempDir()
	home, config := filepath.Join(root, "home"), filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)

	source := writeLegacyPackage(t, root)
	sourceBytes := legacyBytes(t, source)
	sourceHashes := legacyHashes(t, source)
	fixtureDir := filepath.Join(root, "fixture")
	fixture, err := lifecycle.Prepare(lifecycle.PrepareOptions{FixtureDir: fixtureDir, Recording: source})
	require.NoError(t, err)
	current, present, err := lifecycle.CurrentArtifact(fixtureDir, fixture)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, sourceHashes, current.Files)
	require.NoFileExists(t, filepath.Join(current.Path, "material.json"))

	require.NoError(t, os.RemoveAll(source))
	opened, err := agenttestify.Open(fixtureDir)
	require.NoError(t, err)
	inputReader, inputWriter, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, inputWriter.Close())
	t.Cleanup(func() { _ = inputReader.Close() })
	output, err := os.CreateTemp(root, "mock-output-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })
	diagnostics, err := os.CreateTemp(root, "mock-stderr-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = diagnostics.Close() })
	result, err := opened.MockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.Streams{In: inputReader, Out: output, Err: diagnostics})
	require.NoError(t, err)
	require.Equal(t, agenttestify.ExecutionStatusPassed, result.ExecutionStatus)
	require.JSONEq(t, "{}\n", string(legacyRead(t, output.Name())))

	exported := filepath.Join(root, "exported")
	require.NoError(t, lifecycle.Export(fixtureDir, exported))
	require.Equal(t, sourceBytes, legacyBytes(t, exported))
	require.Equal(t, sourceHashes, legacyHashes(t, exported))

	reimportedDir := filepath.Join(root, "reimported")
	reimported, err := lifecycle.Prepare(lifecycle.PrepareOptions{FixtureDir: reimportedDir, Recording: exported})
	require.NoError(t, err)
	reimportedMaterial, present, err := lifecycle.CurrentArtifact(reimportedDir, reimported)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, sourceBytes, legacyBytes(t, reimportedMaterial.Path))
}

func writeLegacyPackage(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "legacy-source")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "raw"), 0o700))
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "plan.json"), tool.Object{
		"version": 1, "agentType": "codex", "caseId": "legacy-integration",
		"driverProfile": "consumer", "project": filepath.Join(root, "recorded-project"),
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "evidence"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "evidence", "legacy.txt"), []byte("legacy integration\n"), 0o600))
	raw := `{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","wire":{}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(source, "raw", "raw.jsonl"), []byte(raw), 0o600))
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), tool.Object{
		"provenance": "consumer",
		"planSHA256": tool.DigestBytes(legacyRead(t, filepath.Join(source, "plan.json"))),
		"files":      tool.Object{"raw.jsonl": tool.DigestBytes([]byte(raw))},
	}))
	return source
}

func legacyHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes, err := history.HashTree(root)
	require.NoError(t, err)
	return hashes
}

func legacyBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(name)] = legacyRead(t, path)
		return nil
	}))
	return files
}

func legacyRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
