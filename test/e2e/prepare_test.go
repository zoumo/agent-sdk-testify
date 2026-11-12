package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelfContainedRecordingPrepare(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	producer := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "self-contained"))
	recordConsumer(t, env, producer, agent, requestOne)
	original := fixtureMaterialRoot(t, producer, latestCapture(t, producer))
	raw := readFile(t, filepath.Join(original, "raw", "raw.jsonl"))
	history := readFile(t, filepath.Join(original, "config", "sessions", "conformance-history.jsonl"))
	fixtureDir := prepareFixture(t, env, "--recording", original)
	descriptorPath := filepath.Join(fixtureDir, "fixture.json")
	descriptor := readFile(t, descriptorPath)
	source := str(obj(readJSON(t, descriptorPath)["source"])["path"])
	require.Equal(t, "materials/"+currentMaterialID(t, fixtureDir), source)
	sourceRoot := resolveFixtureReference(t, fixtureDir, source)
	require.Equal(t, raw, readFile(t, filepath.Join(sourceRoot, "raw", "raw.jsonl")))
	require.Equal(t, history, readFile(t, filepath.Join(sourceRoot, "config", "sessions", "conformance-history.jsonl")))
	current := readFile(t, filepath.Join(fixtureDir, "current.json"))
	materialDescriptor := readFile(t, filepath.Join(sourceRoot, "material.json"))
	require.NoError(t, os.RemoveAll(producer))

	result := runPreparedMock(t, env, fixtureDir, "", requestOne)
	require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, result.stdout)
	require.Equal(t, "materials/"+currentMaterialID(t, fixtureDir), source)
	require.Equal(t, currentMaterialID(t, fixtureDir), obj(latestExecution(t, fixtureDir, "mock")["artifact"])["id"])
	require.Equal(t, descriptor, readFile(t, descriptorPath))
	require.Equal(t, current, readFile(t, filepath.Join(fixtureDir, "current.json")))
	require.Equal(t, materialDescriptor, readFile(t, filepath.Join(sourceRoot, "material.json")))
	info, err := os.Stat(descriptorPath)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o222)
	require.FileExists(t, filepath.Join(sourceRoot, "raw", "raw.jsonl"))
	require.Equal(t, descriptor, readFile(t, descriptorPath))
}

func TestPrepareWithoutCurrent(t *testing.T) {
	for _, source := range []string{"case", "plan"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			_, _, env := isolatedCommands(t, root, "codex")
			args := []string{"--agent", "codex", "--case", "SDK-CASE-003"}
			if source == "plan" {
				args = []string{"--plan", writeConsumerPlan(t, root, "empty-current")}
			}
			fixtureDir := prepareFixture(t, env, args...)
			require.Empty(t, readJSON(t, filepath.Join(fixtureDir, "current.json"))["materialId"])
			descriptor := readFile(t, filepath.Join(fixtureDir, "fixture.json"))
			index := readFile(t, filepath.Join(fixtureDir, "executions", "index.json"))
			for _, action := range []string{"mock", "verify"} {
				result := runTool(t, env, t.TempDir(), "", append(publicAction(action), "--fixture-dir", fixtureDir)...)
				require.NotZero(t, result.code)
				require.Contains(t, result.stderr, "no current recording")
				require.Equal(t, index, readFile(t, filepath.Join(fixtureDir, "executions", "index.json")))
				require.Equal(t, descriptor, readFile(t, filepath.Join(fixtureDir, "fixture.json")))
				entries, err := os.ReadDir(filepath.Join(fixtureDir, "executions"))
				require.NoError(t, err)
				require.Len(t, entries, 1)
			}
		})
	}
}

func TestPreparedMaterialTamperFailsBeforeExecution(t *testing.T) {
	for _, name := range []string{"plan.json", "manifest.json", "raw/raw.jsonl", "raw/stderr.raw", "material.json", "unexpected.txt"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, _, env := isolatedCommands(t, root, "codex")
			fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
			material := obj(array(materialIndex(t, fixtureDir)["materials"])[0])
			path := filepath.Join(fixtureMaterialRoot(t, fixtureDir, material), filepath.FromSlash(name))
			require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
			index := readFile(t, filepath.Join(fixtureDir, "executions", "index.json"))
			for _, action := range []string{"mock", "verify"} {
				result := runTool(t, env, t.TempDir(), "", append(publicAction(action), "--fixture-dir", fixtureDir)...)
				require.NotZero(t, result.code)
				require.Equal(t, index, readFile(t, filepath.Join(fixtureDir, "executions", "index.json")))
				entries, err := os.ReadDir(filepath.Join(fixtureDir, "executions"))
				require.NoError(t, err)
				require.Len(t, entries, 1)
			}
		})
	}
}

func TestPlanAndSeedSurviveOriginalRemoval(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	plan := writeConsumerPlan(t, root, "deleted-plan")
	seed := filepath.Join(root, "seed")
	require.NoError(t, os.Mkdir(seed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(seed, "history.jsonl"), []byte("seed\n"), 0o600))
	fixtureDir := prepareFixture(t, env, "--plan", plan, "--history-seed", seed)
	descriptor := readFile(t, filepath.Join(fixtureDir, "fixture.json"))
	require.NoError(t, os.Remove(plan))
	require.NoError(t, os.RemoveAll(seed))
	recordConsumer(t, env, fixtureDir, agent, requestOne)
	runPreparedMock(t, env, fixtureDir, "", requestOne)
	require.Equal(t, descriptor, readFile(t, filepath.Join(fixtureDir, "fixture.json")))
}

func TestIncompatibleCurrentFailsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	producer := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "incompatible-format"))
	recordConsumer(t, env, producer, agent, requestOne)
	material := fixtureMaterialRoot(t, producer, latestCapture(t, producer))
	rawManifestPath := filepath.Join(material, "raw", "manifest.json")
	rawManifest := readJSON(t, rawManifestPath)
	obj(rawManifest["recordingFormat"])["version"] = 999
	writeJSON(t, rawManifestPath, rawManifest)
	manifestPath := filepath.Join(material, "manifest.json")
	manifest := readJSON(t, manifestPath)
	obj(manifest["files"])["manifest.json"] = digestFile(t, rawManifestPath)
	writeJSON(t, manifestPath, manifest)
	// This fixture deliberately represents a legacy package with an unsupported protocol format.
	require.NoError(t, os.Remove(filepath.Join(material, "material.json")))
	fixtureDir := prepareFixture(t, env, "--recording", material)
	index := readFile(t, filepath.Join(fixtureDir, "executions", "index.json"))
	result := runTool(t, env, t.TempDir(), requestOne, "agent", "mock", "--fixture-dir", fixtureDir)
	require.NotZero(t, result.code)
	require.Contains(t, result.stderr, "unsupported recording format")
	require.Equal(t, index, readFile(t, filepath.Join(fixtureDir, "executions", "index.json")))
	entries, err := os.ReadDir(filepath.Join(fixtureDir, "executions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
