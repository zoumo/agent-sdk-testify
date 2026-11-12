package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestLegacyMaterialWithoutDescriptorUsesFixtureIndexIdentity(t *testing.T) {
	isolateLegacyMaterialHome(t)
	source, sourceHashes, sourceBytes := lifecycleLegacyPackage(t, nil)
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	fixture, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Recording: source})
	require.NoError(t, err)
	require.Equal(t, sourceBytes, legacyTreeBytes(t, source))

	material, present, err := CurrentArtifact(fixtureDir, fixture)
	require.NoError(t, err)
	require.True(t, present)
	canonical, err := filepath.EvalSymlinks(fixtureDir)
	require.NoError(t, err)
	require.Equal(t, MaterialRoot(canonical, material.ID), material.Path)
	require.Zero(t, material.FormatVersion)
	require.Equal(t, sourceHashes, material.Files)
	require.NoFileExists(t, filepath.Join(material.Path, "material.json"))

	var index MaterialIndex
	require.NoError(t, tool.ReadJSON(MaterialIndexPath(fixtureDir), &index))
	require.Equal(t, []string{material.ID}, index.Descriptorless)
	require.Len(t, index.Materials, 1)
	require.Empty(t, index.Materials[0].FormatVersion)
	require.Equal(t, sourceHashes, index.Materials[0].Files)

	require.NoError(t, os.RemoveAll(source))
	_, present, err = CurrentArtifact(fixtureDir, fixture)
	require.NoError(t, err)
	require.True(t, present)

	exported := filepath.Join(t.TempDir(), "exported")
	require.NoError(t, Export(fixtureDir, exported))
	require.Equal(t, sourceHashes, legacyTreeHashes(t, exported))
	require.Equal(t, sourceBytes, legacyTreeBytes(t, exported))
	require.NoFileExists(t, filepath.Join(exported, "material.json"))

	reimportedDir := filepath.Join(t.TempDir(), "reimported")
	reimported, err := Prepare(PrepareOptions{FixtureDir: reimportedDir, Recording: exported})
	require.NoError(t, err)
	reimportedMaterial, present, err := CurrentArtifact(reimportedDir, reimported)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, sourceHashes, reimportedMaterial.Files)
	require.Equal(t, sourceHashes, legacyTreeHashes(t, reimportedMaterial.Path))
	require.Equal(t, sourceBytes, legacyTreeBytes(t, reimportedMaterial.Path))
}

func TestLegacyMaterialDescriptorIsReadOnlyAndStoredPathIsIgnored(t *testing.T) {
	isolateLegacyMaterialHome(t)
	source, sourceHashes, sourceBytes := lifecycleLegacyPackageWithStoredPath(t)
	descriptorBytes := sourceBytes["material.json"]
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	fixture, err := Prepare(PrepareOptions{FixtureDir: fixtureDir, Recording: source})
	require.NoError(t, err)
	require.Equal(t, sourceBytes, legacyTreeBytes(t, source))

	material, present, err := CurrentArtifact(fixtureDir, fixture)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, sourceHashes, material.Files)
	require.Equal(t, descriptorBytes, mustReadLegacyFile(t, filepath.Join(material.Path, "material.json")))

	var index MaterialIndex
	require.NoError(t, tool.ReadJSON(MaterialIndexPath(fixtureDir), &index))
	require.Empty(t, index.Descriptorless)
	require.NotContains(t, index.Materials[0].Path, "/stale")

	require.NoError(t, os.RemoveAll(source))
	exported := filepath.Join(t.TempDir(), "exported")
	require.NoError(t, Export(fixtureDir, exported))
	require.Equal(t, sourceBytes, legacyTreeBytes(t, exported))
}

func lifecycleLegacyPackage(t *testing.T, descriptor tool.Object) (string, map[string]string, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "plan.json"), tool.Object{
		"version": 1, "agentType": "codex", "caseId": "legacy-lifecycle",
		"driverProfile": "consumer", "project": "/recorded/project",
	}))
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "manifest.json"), tool.Object{
		"provenance": "consumer",
		"planSHA256": tool.DigestBytes(mustReadLegacyFile(t, filepath.Join(root, "plan.json"))),
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "raw"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "raw", "raw.jsonl"), []byte(`{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","wire":{}}`+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "evidence.txt"), []byte("legacy lifecycle evidence\n"), 0o600))
	hashes, err := history.HashTree(root)
	require.NoError(t, err)
	if descriptor == nil {
		return root, hashes, legacyTreeBytes(t, root)
	}
	descriptor["files"] = hashes
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), descriptor))
	return root, hashes, legacyTreeBytes(t, root)
}

func lifecycleLegacyPackageWithStoredPath(t *testing.T) (string, map[string]string, map[string][]byte) {
	t.Helper()
	root, hashes, _ := lifecycleLegacyPackage(t, nil)
	descriptor := tool.Object{
		"id": "legacy-material", "kind": "material", "action": "mock", "provenance": "consumer",
		"path": "/stale/machine/material", "planSHA256": hashes["plan.json"],
		"manifestSHA256": hashes["manifest.json"], "recordingSHA256": hashes["raw/raw.jsonl"],
		"connections": []string{"main"}, "files": hashes,
	}
	require.NoError(t, tool.WriteJSON(filepath.Join(root, "material.json"), descriptor))
	return root, hashes, legacyTreeBytes(t, root)
}

func isolateLegacyMaterialHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	home, config := filepath.Join(root, "home"), filepath.Join(root, "config")
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.MkdirAll(config, 0o700))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", config)
}

func legacyTreeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes, err := history.HashTree(root)
	require.NoError(t, err)
	return hashes
}

func legacyTreeBytes(t *testing.T, root string) map[string][]byte {
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
		files[filepath.ToSlash(name)] = mustReadLegacyFile(t, path)
		return nil
	}))
	return files
}

func mustReadLegacyFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
