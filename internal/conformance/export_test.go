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
	session := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "export-current"))
	recordConsumer(t, env, session, agent, requestOne)
	old := latestCapture(t, session)
	recordConsumer(t, env, session, agent, requestTwo)
	current := latestCapture(t, session)
	require.NotEqual(t, old["id"], current["id"])
	material := str(current["path"])
	hashes := packageHashes(t, material)
	require.Contains(t, hashes, "config/sessions/conformance-history.jsonl")
	require.Contains(t, hashes, "raw/raw.jsonl")
	before := readAllFiles(t, session)
	output := filepath.Join(root, "export-open")
	mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	require.Equal(t, hashes, packageHashes(t, output))
	require.Equal(t, before, readAllFiles(t, session))
	metadata := readJSON(t, filepath.Join(output, "material.json"))
	require.Equal(t, current, metadata)
	assertFileHashes(t, output, obj(metadata["files"]))
	for _, name := range []string{"session.json", "current.json", "executions", "receipts", "verification.json", "qualification.json", "finish.json", "index.json"} {
		require.NoFileExists(t, filepath.Join(output, name))
		require.NoDirExists(t, filepath.Join(output, name))
	}
	finished, _ := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
	before = readAllFiles(t, session)
	closedOutput := filepath.Join(root, "export-closed")
	mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", closedOutput)
	require.Equal(t, hashes, packageHashes(t, closedOutput))
	require.Equal(t, before, readAllFiles(t, session))
	require.NoError(t, os.RemoveAll(session))
	imported := prepareSession(t, env, "--recording", output)
	importedMetadata := obj(array(materialIndex(t, imported)["materials"])[0])
	importedPath := str(importedMetadata["path"])
	importedMetadata["path"] = metadata["path"]
	require.Equal(t, metadata, importedMetadata)
	importedHashes := packageHashes(t, importedPath)
	delete(importedHashes, "material.json")
	delete(hashes, "material.json")
	require.Equal(t, hashes, importedHashes)
	require.NoError(t, os.RemoveAll(output))
	require.NoError(t, os.RemoveAll(closedOutput))
	runPreparedMock(t, env, imported, "", requestTwo)
	before = readAllFiles(t, imported)
	failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", imported)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "no applicable official verifier")
	require.Equal(t, before, readAllFiles(t, imported))
	finished, _ = finishSession(t, env, imported)
	require.Zero(t, finished.code, finished.stderr)
}

func TestExportOfficialCaptureRoundTrip(t *testing.T) {
	root := t.TempDir()
	agent, drivers, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	installDriver(t, drivers, "codex", "record")
	mustRun(t, env, t.TempDir(), "", "official", "record", "--session-dir", session, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	material := latestCapture(t, session)
	installDriver(t, drivers, "codex", "verify")
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", session)
	output := filepath.Join(root, "official-export")
	before := readAllFiles(t, session)
	mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	require.Equal(t, before, readAllFiles(t, session))
	require.Equal(t, packageHashes(t, str(material["path"])), packageHashes(t, output))
	finished, _ := finishSession(t, env, session)
	require.Zero(t, finished.code, finished.stderr)
	require.NoError(t, os.RemoveAll(session))
	imported := prepareSession(t, env, "--recording", output)
	metadata := obj(array(materialIndex(t, imported)["materials"])[0])
	require.Equal(t, "official", metadata["provenance"])
	require.Equal(t, material["id"], metadata["id"])
	require.Equal(t, "capture", metadata["kind"])
	require.Equal(t, "record", metadata["action"])
	require.Empty(t, array(sessionIndex(t, imported)["executions"]))
	// The exported original capture has no transferable verification evidence.
	require.NoFileExists(t, filepath.Join(str(metadata["path"]), "verification.json"))
	// Removing metadata makes the same native capture an external input again.
	require.NoError(t, os.Remove(filepath.Join(output, "material.json")))
	external := prepareSession(t, env, "--recording", output)
	require.Equal(t, "external", obj(array(materialIndex(t, external)["materials"])[0])["provenance"])
	before = readAllFiles(t, external)
	failed := runTool(t, env, t.TempDir(), "", "official", "verify", "--session-dir", external)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "no applicable official verifier")
	require.Equal(t, before, readExistingFiles(t, external, before))
	require.NoError(t, os.RemoveAll(output))
	runPreparedMock(t, env, imported, "", conformanceRequest)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--session-dir", imported)
	evidence := readJSON(t, filepath.Join(latestVerificationRoot(t, imported), "verification.json"))
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, material["id"], obj(evidence["material"])["id"])
	finished, _ = finishSession(t, env, imported)
	require.Zero(t, finished.code, finished.stderr)
}

func TestExportRejectsTargetsAndTampering(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	alias := filepath.Join(root, "session-alias")
	require.NoError(t, os.Symlink(session, alias))
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(existing, 0o700))
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("preserve\n"), 0o600))
	link := filepath.Join(root, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(root, "absent"), link))
	before := readAllFiles(t, session)
	for _, target := range []string{session, filepath.Join(session, "export"), filepath.Join(alias, "export"), existing, file, link, "relative", filepath.Join(root, "missing-parent", "export")} {
		failed := runTool(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", target)
		require.NotZero(t, failed.code, target)
		require.Equal(t, before, readAllFiles(t, session))
	}
	require.Equal(t, []byte("preserve\n"), readFile(t, file))
	entries, err := os.ReadDir(existing)
	require.NoError(t, err)
	require.Empty(t, entries)
	material := str(obj(array(materialIndex(t, session)["materials"])[0])["path"])
	for _, name := range []string{"raw/raw.jsonl", "material.json", "unbound.txt"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(material, filepath.FromSlash(name))
			original, err := os.ReadFile(path)
			require.True(t, err == nil || os.IsNotExist(err))
			require.NoError(t, os.WriteFile(path, []byte("tampered\n"), 0o600))
			before := readAllFiles(t, session)
			output := filepath.Join(root, "failed-export")
			failed := runTool(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
			require.NotZero(t, failed.code)
			require.NoDirExists(t, output)
			require.Equal(t, before, readAllFiles(t, session))
			if err == nil {
				require.NoError(t, os.WriteFile(path, original, 0o600))
			} else {
				require.NoError(t, os.Remove(path))
			}
		})
	}
	output := filepath.Join(root, "valid-export")
	mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	exported := readAllFiles(t, output)
	failed := runTool(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	require.NotZero(t, failed.code)
	require.Equal(t, exported, readAllFiles(t, output))
	// A changed exported payload cannot be re-imported.
	require.NoError(t, os.WriteFile(filepath.Join(output, "raw", "stderr.raw"), []byte("changed\n"), 0o600))
	failed = runTool(t, env, t.TempDir(), "", "prepare", "--session-dir", filepath.Join(root, "bad-import"), "--recording", output)
	require.NotZero(t, failed.code)
	stages, err := filepath.Glob(filepath.Join(root, ".agent-sdk-testify-export-*"))
	require.NoError(t, err)
	require.Empty(t, stages)
}

func TestExportNoCurrentAndCLISelection(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--plan", writeConsumerPlan(t, root, "no-export"))
	before := readAllFiles(t, session)
	output := filepath.Join(root, "export")
	failed := runTool(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "no current recording")
	for _, extra := range [][]string{{"--recording", root}, {"--case", "SDK-CASE-001"}, {"--plan", root}, {"--agent", "codex"}, {"unexpected"}} {
		args := append([]string{"export", "--session-dir", session, "--output", output}, extra...)
		failed = runTool(t, env, t.TempDir(), "", args...)
		require.Equal(t, 1, failed.code, failed.stderr)
	}
	require.Equal(t, before, readAllFiles(t, session))
	require.NoDirExists(t, output)
}

func TestExportBuiltinRoundTrip(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			_, _, env := isolatedCommands(t, root, provider)
			session := prepareSession(t, env, "--agent", provider, "--case", "SDK-CASE-001")
			material := obj(array(materialIndex(t, session)["materials"])[0])
			output := filepath.Join(root, "builtin-export")
			before := readAllFiles(t, session)
			mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
			require.Equal(t, before, readAllFiles(t, session))
			require.Equal(t, packageHashes(t, str(material["path"])), packageHashes(t, output))
			imported := prepareSession(t, env, "--recording", output)
			metadata := obj(array(materialIndex(t, imported)["materials"])[0])
			require.Equal(t, "builtin", metadata["provenance"])
			require.Equal(t, material["id"], metadata["id"])
			finished, _ := finishSession(t, env, session)
			require.Zero(t, finished.code, finished.stderr)
			require.NoError(t, os.RemoveAll(session))
			require.NoError(t, os.RemoveAll(output))
			mustRun(t, env, t.TempDir(), builtinInputs(t, provider, "SDK-CASE-001")["main"], "agent", "mock", "--session-dir", imported)
			finished, _ = finishSession(t, env, imported)
			require.Zero(t, finished.code, finished.stderr)
		})
	}
}

func TestExportBusyLeavesSessionAndTargetUntouched(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	session := prepareSession(t, env, "--agent", "codex", "--case", "SDK-CASE-001")
	file, err := os.OpenFile(filepath.Join(session, "locks", "action.lock"), os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB))
	before := readAllFiles(t, session)
	output := filepath.Join(root, "busy-export")
	failed := runTool(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	require.NotZero(t, failed.code)
	require.Contains(t, failed.stderr, "session action busy")
	require.Equal(t, before, readAllFiles(t, session))
	require.NoDirExists(t, output)
	require.NoError(t, syscall.Flock(int(file.Fd()), syscall.LOCK_UN))
	mustRun(t, env, t.TempDir(), "", "export", "--session-dir", session, "--output", output)
	require.Equal(t, before, readAllFiles(t, session))
}
