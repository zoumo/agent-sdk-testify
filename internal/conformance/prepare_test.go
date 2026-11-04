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
	producer := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "self-contained"))
	recordConsumer(t, env, producer, agent, requestOne)
	original := str(latestCapture(t, producer)["path"])
	raw := readFile(t, filepath.Join(original, "raw", "raw.jsonl"))
	history := readFile(t, filepath.Join(original, "config", "sessions", "conformance-history.jsonl"))
	session := prepareSession(t, env, "--recording", original)
	descriptorPath := filepath.Join(session, "session.json")
	descriptor := readFile(t, descriptorPath)
	source := str(obj(readJSON(t, descriptorPath)["source"])["path"])
	require.Contains(t, source, filepath.Join(session, "materials")+string(filepath.Separator))
	require.Equal(t, raw, readFile(t, filepath.Join(source, "raw", "raw.jsonl")))
	require.Equal(t, history, readFile(t, filepath.Join(source, "config", "sessions", "conformance-history.jsonl")))
	current := readFile(t, filepath.Join(session, "current.json"))
	materialDescriptor := readFile(t, filepath.Join(source, "material.json"))
	finished, _ := finishSession(t, env, producer)
	require.Zero(t, finished.code, finished.stderr)
	require.NoError(t, os.RemoveAll(producer))

	result := runPreparedMock(t, env, session, "", requestOne)
	require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, result.stdout)
	require.Equal(t, source, obj(latestExecution(t, session, "mock")["artifact"])["path"])
	require.Equal(t, descriptor, readFile(t, descriptorPath))
	require.Equal(t, current, readFile(t, filepath.Join(session, "current.json")))
	require.Equal(t, materialDescriptor, readFile(t, filepath.Join(source, "material.json")))
	info, err := os.Stat(descriptorPath)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o222)
	finished, _ = finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
	require.FileExists(t, filepath.Join(source, "raw", "raw.jsonl"))
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
			session := prepareSession(t, env, args...)
			require.Empty(t, readJSON(t, filepath.Join(session, "current.json"))["materialId"])
			descriptor := readFile(t, filepath.Join(session, "session.json"))
			index := readFile(t, filepath.Join(session, "executions", "index.json"))
			for _, action := range []string{"mock", "verify"} {
				result := runTool(t, env, t.TempDir(), "", action, "--session-dir", session)
				require.NotZero(t, result.code)
				require.Contains(t, result.stderr, "no current recording")
				require.Equal(t, index, readFile(t, filepath.Join(session, "executions", "index.json")))
				require.Equal(t, descriptor, readFile(t, filepath.Join(session, "session.json")))
				entries, err := os.ReadDir(filepath.Join(session, "executions"))
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
			session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
			material := obj(array(materialIndex(t, session)["materials"])[0])
			path := filepath.Join(str(material["path"]), filepath.FromSlash(name))
			require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
			index := readFile(t, filepath.Join(session, "executions", "index.json"))
			for _, action := range []string{"mock", "verify"} {
				result := runTool(t, env, t.TempDir(), "", action, "--session-dir", session)
				require.NotZero(t, result.code)
				require.Equal(t, index, readFile(t, filepath.Join(session, "executions", "index.json")))
				entries, err := os.ReadDir(filepath.Join(session, "executions"))
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
	session := prepareSession(t, env, "--plan", plan, "--history-seed", seed)
	descriptor := readFile(t, filepath.Join(session, "session.json"))
	require.NoError(t, os.Remove(plan))
	require.NoError(t, os.RemoveAll(seed))
	recordConsumer(t, env, session, agent, requestOne)
	runPreparedMock(t, env, session, "", requestOne)
	require.Equal(t, descriptor, readFile(t, filepath.Join(session, "session.json")))
}

func TestIncompatibleCurrentFailsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	producer := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "incompatible-format"))
	recordConsumer(t, env, producer, agent, requestOne)
	material := str(latestCapture(t, producer)["path"])
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
	session := prepareSession(t, env, "--recording", material)
	index := readFile(t, filepath.Join(session, "executions", "index.json"))
	result := runTool(t, env, t.TempDir(), requestOne, "mock", "--session-dir", session)
	require.NotZero(t, result.code)
	require.Contains(t, result.stderr, "unsupported recording format")
	require.Equal(t, index, readFile(t, filepath.Join(session, "executions", "index.json")))
	entries, err := os.ReadDir(filepath.Join(session, "executions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
