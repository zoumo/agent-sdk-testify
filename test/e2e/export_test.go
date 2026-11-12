package conformance

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func packageHashes(t *testing.T, root string) object {
	t.Helper()
	files := object{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = digestFile(t, path)
		return nil
	}))
	return files
}

func TestExportCurrentRoundTrip(t *testing.T) {
	root := t.TempDir()
	agent, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "export-current"))
	recordConsumer(t, env, fixtureDir, agent, requestOne)
	old := latestCapture(t, fixtureDir)
	recordConsumer(t, env, fixtureDir, agent, requestTwo)
	current := latestCapture(t, fixtureDir)
	require.NotEqual(t, old["id"], current["id"])
	material := fixtureMaterialRoot(t, fixtureDir, current)
	hashes := packageHashes(t, material)
	require.Contains(t, hashes, "config/sessions/conformance-history.jsonl")
	require.Contains(t, hashes, "raw/raw.jsonl")
	before := readAllFiles(t, fixtureDir)
	output := filepath.Join(root, "export-first")
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	require.Equal(t, hashes, packageHashes(t, output))
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	metadata := readJSON(t, filepath.Join(output, "material.json"))
	require.Equal(t, current, metadata)
	assertFileHashes(t, output, obj(metadata["files"]))
	for _, name := range []string{"fixture.json", "current.json", "executions", "receipts", "verification.json", "qualification.json", "runtime", "index.json"} {
		require.NoFileExists(t, filepath.Join(output, name))
		require.NoDirExists(t, filepath.Join(output, name))
	}
	require.NoError(t, os.RemoveAll(filepath.Join(fixtureDir, "runtime")))
	before = readAllFiles(t, fixtureDir)
	secondOutput := filepath.Join(root, "export-second")
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", secondOutput)
	require.Equal(t, hashes, packageHashes(t, secondOutput))
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	require.NoError(t, os.RemoveAll(fixtureDir))
	imported := prepareFixture(t, env, "--recording", output)
	importedMetadata := obj(array(materialIndex(t, imported)["materials"])[0])
	require.Equal(t, metadata, importedMetadata)
	importedPath := fixtureMaterialRoot(t, imported, importedMetadata)
	importedHashes := packageHashes(t, importedPath)
	delete(importedHashes, "material.json")
	delete(hashes, "material.json")
	require.Equal(t, hashes, importedHashes)
	require.NoError(t, os.RemoveAll(output))
	require.NoError(t, os.RemoveAll(secondOutput))
	runPreparedMock(t, env, imported, "", requestTwo)
	before = readAllFiles(t, imported)
	failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", imported)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "no applicable official verifier")
	require.Equal(t, before, readAllFiles(t, imported))
}

func TestExportOfficialCaptureRoundTrip(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixtureDir, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	material := latestCapture(t, fixtureDir)
	installDriver(t, drivers, "codex", "verify")
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixtureDir)
	output := filepath.Join(root, "official-export")
	before := readAllFiles(t, fixtureDir)
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	require.Equal(t, packageHashes(t, fixtureMaterialRoot(t, fixtureDir, material)), packageHashes(t, output))
	require.NoError(t, os.RemoveAll(fixtureDir))
	imported := prepareFixture(t, env, "--recording", output)
	metadata := obj(array(materialIndex(t, imported)["materials"])[0])
	require.Equal(t, "official", metadata["provenance"])
	require.Equal(t, material["id"], metadata["id"])
	require.Equal(t, "capture", metadata["kind"])
	require.Equal(t, "record", metadata["action"])
	require.Empty(t, array(fixtureIndex(t, imported)["executions"]))
	// The exported original capture has no transferable verification evidence.
	require.NoFileExists(t, filepath.Join(fixtureMaterialRoot(t, imported, metadata), "verification.json"))
	// Removing metadata makes the same native capture an external input again.
	require.NoError(t, os.Remove(filepath.Join(output, "material.json")))
	external := prepareFixture(t, env, "--recording", output)
	require.Equal(t, "external", obj(array(materialIndex(t, external)["materials"])[0])["provenance"])
	before = readAllFiles(t, external)
	failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", external)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "no applicable official verifier")
	require.Equal(t, before, readAllFiles(t, external))
	require.NoError(t, os.RemoveAll(output))
	runPreparedMock(t, env, imported, "", conformanceRequest)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", imported)
	evidence := readJSON(t, filepath.Join(latestVerificationRoot(t, imported), "verification.json"))
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, material["id"], obj(evidence["material"])["id"])
}

func TestExportRejectsTargetsAndTampering(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	alias := filepath.Join(root, "fixture-alias")
	require.NoError(t, os.Symlink(fixtureDir, alias))
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(existing, 0o700))
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("preserve\n"), 0o600))
	link := filepath.Join(root, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(root, "absent"), link))
	before := readAllFiles(t, fixtureDir)
	for _, target := range []string{fixtureDir, filepath.Join(fixtureDir, "export"), filepath.Join(alias, "export"), existing, file, link, "relative", filepath.Join(root, "missing-parent", "export")} {
		failed := runTool(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", target)
		require.NotZero(t, failed.code, target)
		require.Equal(t, before, readAllFiles(t, fixtureDir))
	}
	require.Equal(t, []byte("preserve\n"), readFile(t, file))
	entries, err := os.ReadDir(existing)
	require.NoError(t, err)
	require.Empty(t, entries)
	material := fixtureMaterialRoot(t, fixtureDir, obj(array(materialIndex(t, fixtureDir)["materials"])[0]))
	for _, name := range []string{"raw/raw.jsonl", "material.json", "unbound.txt"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(material, filepath.FromSlash(name))
			original, err := os.ReadFile(path)
			require.True(t, err == nil || os.IsNotExist(err))
			require.NoError(t, os.WriteFile(path, []byte("tampered\n"), 0o600))
			before := readAllFiles(t, fixtureDir)
			output := filepath.Join(root, "failed-export")
			failed := runTool(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
			require.NotZero(t, failed.code)
			require.NoDirExists(t, output)
			require.Equal(t, before, readAllFiles(t, fixtureDir))
			if err == nil {
				require.NoError(t, os.WriteFile(path, original, 0o600))
			} else {
				require.NoError(t, os.Remove(path))
			}
		})
	}
	output := filepath.Join(root, "valid-export")
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	exported := readAllFiles(t, output)
	failed := runTool(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	require.NotZero(t, failed.code)
	require.Equal(t, exported, readAllFiles(t, output))
	// A changed exported payload cannot be re-imported.
	require.NoError(t, os.WriteFile(filepath.Join(output, "raw", "stderr.raw"), []byte("changed\n"), 0o600))
	failed = runTool(t, env, t.TempDir(), "", "prepare", "--fixture-dir", filepath.Join(root, "bad-import"), "--recording", output)
	require.NotZero(t, failed.code)
	stages, err := filepath.Glob(filepath.Join(root, ".agent-sdk-testify-export-*"))
	require.NoError(t, err)
	require.Empty(t, stages)
}

func TestExportNoCurrentAndCLISelection(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--plan", writeConsumerPlan(t, root, "no-export"))
	before := readAllFiles(t, fixtureDir)
	output := filepath.Join(root, "export")
	failed := runTool(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "no current recording")
	for _, extra := range [][]string{{"--recording", root}, {"--case", "SDK-CASE-001"}, {"--plan", root}, {"--agent", "codex"}, {"unexpected"}} {
		args := append([]string{"export", "--fixture-dir", fixtureDir, "--output", output}, extra...)
		failed = runTool(t, env, t.TempDir(), "", args...)
		require.Equal(t, 1, failed.code, failed.stderr)
	}
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	require.NoDirExists(t, output)
}

func TestExportBuiltinRoundTrip(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, caseID := range []string{"SDK-CASE-001", "SDK-CASE-037", "SDK-CASE-038"} {
			t.Run(provider+"/"+caseID, func(t *testing.T) {
				root := t.TempDir()
				_, _, env := isolatedCommands(t, root, provider)
				fixtureDir := prepareFixture(t, env, "--agent", provider, "--case", caseID)
				material := obj(array(materialIndex(t, fixtureDir)["materials"])[0])
				require.NotEmpty(t, material["officialOrigin"])
				output := filepath.Join(root, "builtin-export")
				before := readAllFiles(t, fixtureDir)
				mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
				require.Equal(t, before, readAllFiles(t, fixtureDir))
				require.Equal(t, packageHashes(t, fixtureMaterialRoot(t, fixtureDir, material)), packageHashes(t, output))
				imported := prepareFixture(t, env, "--recording", output)
				metadata := obj(array(materialIndex(t, imported)["materials"])[0])
				require.Equal(t, "builtin", metadata["provenance"])
				require.Equal(t, material["id"], metadata["id"])
				require.Equal(t, material["officialOrigin"], metadata["officialOrigin"])
				require.NoError(t, os.RemoveAll(fixtureDir))
				require.NoError(t, os.RemoveAll(output))
				mustRun(t, env, t.TempDir(), builtinInputs(t, provider, caseID)["main"], "agent", "mock", "--fixture-dir", imported)
			})
		}
	}
}

func TestExportBusyLeavesFixtureAndTargetUntouched(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	fixtureDir := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	file, err := os.OpenFile(filepath.Join(fixtureDir, "locks", "action.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
	before := readAllFiles(t, fixtureDir)
	output := filepath.Join(root, "busy-export")
	failed := runTool(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "fixture action busy")
	require.Equal(t, before, readAllFiles(t, fixtureDir))
	require.NoDirExists(t, output)
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_UN))
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixtureDir, "--output", output)
	require.Equal(t, before, readAllFiles(t, fixtureDir))
}
