package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyMaterialFormatsPreserveBytesAcrossImportMockExport(t *testing.T) {
	for _, tc := range []struct {
		name          string
		format        string
		formatVersion int
	}{
		{name: "descriptorless legacy", format: "none"},
		{name: "legacy descriptor with stored path", format: "legacy", formatVersion: 0},
		{name: "format two without location", format: "current", formatVersion: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			_, _, env := isolatedCommands(t, root, "codex")
			source, sourceHashes, sourceContents := writeLegacyMaterial(t, root, tc.format)
			descriptorless := tc.format == "none"

			fixtureDir := prepareFixture(t, env, "--recording", source)
			require.Equal(t, sourceContents, legacyMaterialContents(t, source), "prepare changed its source")
			var index object
			require.NoError(t, readJSONFile(filepath.Join(fixtureDir, "packages", "index.json"), &index))
			material := obj(array(index["materials"])[0])
			materialRoot := fixtureMaterialRoot(t, fixtureDir, material)
			require.Equal(t, sourceHashes, packageHashes(t, materialRoot))
			if descriptorless {
				require.Equal(t, []string{str(material["id"])}, stringsOf(index["descriptorless"]))
				require.NotContains(t, material, "formatVersion")
				require.NoFileExists(t, filepath.Join(materialRoot, "material.json"))
			} else {
				require.Empty(t, stringsOf(index["descriptorless"]))
				if tc.formatVersion == 0 {
					require.NotContains(t, material, "formatVersion")
				} else {
					require.EqualValues(t, tc.formatVersion, material["formatVersion"])
				}
				require.NotContains(t, material, "path")
				require.Equal(t, sourceContents["material.json"], readFile(t, filepath.Join(materialRoot, "material.json")))
			}

			require.NoError(t, os.RemoveAll(source))
			require.NoError(t, os.RemoveAll(filepath.Join(root, "stale-location")))
			mock := runPreparedMock(t, env, fixtureDir, "", "")
			require.JSONEq(t, "{}\n", mock.stdout)

			exported := filepath.Join(root, "exported")
			mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", exported)
			require.Equal(t, sourceContents, legacyMaterialContents(t, exported))
			require.Equal(t, sourceHashes, packageHashes(t, exported))

			require.NoError(t, os.RemoveAll(fixtureDir))
			reimported := prepareFixture(t, env, "--recording", exported)
			var reimportedIndex object
			require.NoError(t, readJSONFile(filepath.Join(reimported, "packages", "index.json"), &reimportedIndex))
			reimportedMaterial := obj(array(reimportedIndex["materials"])[0])
			require.Equal(t, sourceHashes, packageHashes(t, fixtureMaterialRoot(t, reimported, reimportedMaterial)))
			if descriptorless {
				require.Equal(t, []string{str(reimportedMaterial["id"])}, stringsOf(reimportedIndex["descriptorless"]))
			}
			require.JSONEq(t, "{}\n", runPreparedMock(t, env, reimported, "", "").stdout)

			secondExport := filepath.Join(root, "second-export")
			mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", reimported, "--output", secondExport)
			require.Equal(t, sourceContents, legacyMaterialContents(t, secondExport))
		})
	}
}

func writeLegacyMaterial(t *testing.T, root, format string) (string, object, map[string][]byte) {
	t.Helper()
	source := filepath.Join(root, "legacy-material")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "raw"), 0o700))
	plan := object{
		"version": 1, "agentType": "codex", "caseId": "legacy-cli-" + format,
		"driverProfile": "consumer", "project": filepath.Join(root, "recorded-project"),
		"model": "synthetic-model", "cliVersion": "synthetic-conformance-native",
		"scenario": object{"files": object{"fixture.txt": "fixture\n"}},
	}
	writeJSON(t, filepath.Join(source, "plan.json"), plan)
	raw := `{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","wire":{}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(source, "raw", "raw.jsonl"), []byte(raw), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "evidence"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "evidence", "legacy.txt"), []byte("legacy evidence bytes\n"), 0o600))
	writeJSON(t, filepath.Join(source, "manifest.json"), object{
		"provenance": "consumer", "planSHA256": digestFile(t, filepath.Join(source, "plan.json")),
		"files": object{"raw.jsonl": digestBytes([]byte(raw))},
	})
	hashes := packageHashes(t, source)
	if format != "none" {
		material := object{
			"id": "legacy-cli-material", "kind": "material", "action": "mock", "provenance": "consumer",
			"planSHA256": hashes["plan.json"], "manifestSHA256": hashes["manifest.json"],
			"recordingSHA256": hashes["raw/raw.jsonl"], "connections": []string{"main"}, "files": hashes,
		}
		if format == "legacy" {
			material["path"] = filepath.Join(root, "stale-location")
		} else {
			material["formatVersion"] = 2
		}
		writeJSON(t, filepath.Join(source, "material.json"), material)
	}
	return source, packageHashes(t, source), legacyMaterialContents(t, source)
}

func legacyMaterialContents(t *testing.T, root string) map[string][]byte {
	t.Helper()
	contents := map[string][]byte{}
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
		contents[filepath.ToSlash(name)] = readFile(t, path)
		return nil
	}))
	return contents
}
