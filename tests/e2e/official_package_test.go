package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Synthetic installed peers exercise the production execution/publication seam.
func TestOfficialPackageSettlement(t *testing.T) {
	agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	setControlledOfficialDriver(t, drivers, "local")
	fixture := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	recorded := runTool(t, env, t.TempDir(), "", "official", "record", "--fixture-dir", fixture, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	require.Zero(t, recorded.code, recorded.stderr)
	require.NoFileExists(t, filepath.Join(fixture, "official.json"))
	material := latestCapture(t, fixture)
	packageRoot := filepath.Join(fixture, "packages", str(material["id"]))
	core := filepath.Join(packageRoot, "material")
	origin := readJSON(t, filepath.Join(core, "official-origin.json"))
	runID := str(origin["runId"])
	recordRoot := filepath.Join(packageRoot, "record", runID)
	record := readJSON(t, filepath.Join(recordRoot, "result.json"))
	require.EqualValues(t, 3, record["schemaVersion"])
	require.Equal(t, "passed", record["status"])
	require.NotContains(t, record, "report")
	require.NotContains(t, record, "business")
	require.FileExists(t, filepath.Join(recordRoot, "driver-result.json"))
	require.NoDirExists(t, filepath.Join(fixture, "runtime", "official", runID, "staging"))
	before := officialInventory(t, core)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixture)
	verifyRoots, err := filepath.Glob(filepath.Join(packageRoot, "verify", "*"))
	require.NoError(t, err)
	require.Len(t, verifyRoots, 1)
	verification := readJSON(t, filepath.Join(verifyRoots[0], "verification.json"))
	require.EqualValues(t, 3, verification["schemaVersion"])
	require.Equal(t, "passed", verification["status"])
	for _, key := range []string{"report", "business", "connections", "sourceFiles", "driverFiles"} {
		require.NotContains(t, verification, key)
	}
	require.Len(t, array(verification["executions"]), 1)
	require.Equal(t, before, officialInventory(t, core))
	require.NotEqual(t, record["executionId"], obj(array(verification["executions"])[0])["executionId"])
	corpus := newSyntheticCorpus(t)
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", fixture, "--index", corpus.index, "--scenarios", corpus.scenarios, "--archive", corpus.archive)
	var admitted object
	index := readJSON(t, corpus.index)
	for _, value := range array(index["cases"]) {
		entry := obj(value)
		if entry["caseId"] == "SDK-CASE-001" && entry["provider"] == "codex" {
			admitted = entry
			break
		}
	}
	require.Equal(t, "admitted", admitted["adoption"])
	attempt := filepath.Join(filepath.Dir(corpus.index), str(admitted["materials"]))
	archived := filepath.Join(corpus.archive, filepath.Base(attempt))
	review := readJSON(t, filepath.Join(archived, "review.json"))
	require.EqualValues(t, 2, review["schemaVersion"])
	require.EqualValues(t, 2, readJSON(t, filepath.Join(archived, "adoption.json"))["schemaVersion"])
	require.Equal(t, digestFile(t, filepath.Join(archived, "adoption.json")), admitted["adoptionSHA256"])
	require.Equal(t, digestFile(t, filepath.Join(attempt, "material.json")), admitted["materialSHA256"])
	for _, key := range []string{"files", "captureFiles", "replayFiles"} {
		require.NotContains(t, review, key)
	}
	require.Equal(t, before, officialInventory(t, attempt))
	selectedFiles := map[string]string{}
	domains := []string{"material", filepath.Dir(str(obj(review["record"])["path"])), filepath.Dir(str(obj(review["verification"])["path"]))}
	for _, value := range array(review["executions"]) {
		domains = append(domains, filepath.Dir(str(obj(value)["path"])))
	}
	for _, domain := range domains {
		for name, hash := range officialInventory(t, filepath.Join(packageRoot, domain)) {
			path := filepath.ToSlash(filepath.Join(domain, name))
			selectedFiles[path] = hash
			require.Equal(t, readFile(t, filepath.Join(packageRoot, path)), readFile(t, filepath.Join(archived, path)), path)
		}
	}
	adoptedFiles := officialInventory(t, archived)
	delete(adoptedFiles, "review.json")
	delete(adoptedFiles, "adoption.json")
	require.Equal(t, selectedFiles, adoptedFiles)
	// Each output domain remains independently hashed even when another domain
	// has a file of the same name. A changed owned file must reject re-import.
	for _, kind := range []string{"record", "verification"} {
		path := filepath.Join(archived, filepath.Dir(str(obj(review[kind])["path"])), "driver-result.json")
		original := readFile(t, path)
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
		rejected := runTool(t, env, t.TempDir(), "", "prepare", "--recording", archived, "--fixture-dir", filepath.Join(t.TempDir(), "rejected"))
		require.NotZero(t, rejected.code)
		require.Contains(t, strings.ToLower(rejected.stderr), "hash")
		require.NoError(t, os.WriteFile(path, original, 0o600))
	}
	require.NoDirExists(t, filepath.Join(attempt, "runtime"))
	require.NoFileExists(t, filepath.Join(attempt, "fixture.json"))
	for _, name := range []string{"executions", "record", "verify", "review.json", "adoption.json"} {
		require.NoFileExists(t, filepath.Join(attempt, name))
		require.NoDirExists(t, filepath.Join(attempt, name))
	}
	rawPath := filepath.Join(attempt, "raw", "raw.jsonl")
	raw := readFile(t, rawPath)
	require.NoError(t, os.WriteFile(rawPath, []byte("{}\n"), 0o600))
	rejected := runTool(t, env, t.TempDir(), "", "prepare", "--recording", attempt, "--fixture-dir", filepath.Join(t.TempDir(), "changed-core"))
	require.NotZero(t, rejected.code)
	require.NoError(t, os.WriteFile(rawPath, raw, 0o600))
	imported := prepareFixture(t, env, "--recording", attempt)
	importedPackage := filepath.Join(imported, "packages", str(material["id"]))
	require.Equal(t, officialInventory(t, attempt), officialInventory(t, filepath.Join(importedPackage, "material")))
	require.Empty(t, array(fixtureIndex(t, imported)["executions"]), "archived executions do not join the new fixture schedule")
	// Corpus publication and export transfer the same original replay core.
	exported := filepath.Join(t.TempDir(), "core")
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", fixture, "--output", exported)
	require.Equal(t, before, officialInventory(t, exported))
	if target := os.Getenv("TESTIFY_OFFICIAL_INVENTORY"); target != "" {
		require.NoError(t, writeJSONFile(target, officialInventory(t, fixture)))
	}
	require.NoError(t, os.RemoveAll(fixture))
	require.NoError(t, os.RemoveAll(attempt))
	require.NoError(t, os.RemoveAll(corpus.archive))
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", imported)
	standalone := prepareFixture(t, env, "--recording", exported)
	require.NoError(t, os.RemoveAll(exported))
	plan := readJSON(t, filepath.Join(standalone, "packages", str(material["id"]), "material", "plan.json"))
	input, err := json.Marshal(object{"id": 1, "method": "conformance", "params": object{"cwd": plan["project"]}})
	require.NoError(t, err)
	runPreparedMock(t, env, standalone, "", string(input)+"\n")
	mustRun(t, env, t.TempDir(), "", "official", "verify", "--fixture-dir", standalone)
	secondExport := filepath.Join(t.TempDir(), "reexported-core")
	mustRun(t, env, t.TempDir(), "", "export", "--fixture-dir", standalone, "--output", secondExport)
	require.Equal(t, before, officialInventory(t, secondExport))
}

func officialInventory(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = digestFile(t, path)
		return nil
	}))
	return files
}

func assertMaterialHashes(t *testing.T, root string, material object) {
	t.Helper()
	manifest := readJSON(t, filepath.Join(root, "manifest.json"))
	require.Equal(t, material["manifestSHA256"], digestFile(t, filepath.Join(root, "manifest.json")))
	require.Equal(t, material["planSHA256"], digestFile(t, filepath.Join(root, "plan.json")))
	require.Equal(t, material["sourcePlanSHA256"], digestFile(t, filepath.Join(root, "source-plan.json")))
	assertFileHashes(t, root, obj(manifest["files"]))
	files := officialInventory(t, root)
	delete(files, "manifest.json")
	delete(files, "material.json")
	require.Len(t, obj(manifest["files"]), len(files))
}

func TestOfficialPackageFailureOwnership(t *testing.T) {
	for _, mode := range []string{"no-trigger", "core-failed", "business-failed", "lost-notice", "partial-notice"} {
		t.Run(mode, func(t *testing.T) {
			agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
			driverMode := mode
			if mode == "core-failed" {
				driverMode = "local"
			}
			setControlledOfficialDriver(t, drivers, driverMode)
			fixture := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
			prior := readFile(t, filepath.Join(fixture, "current.json"))
			args := []string{"official", "record", "--fixture-dir", fixture, "--cmd", agent, "--", "app-server", "--listen", "stdio://"}
			if mode == "core-failed" {
				writeExecutable(t, agent, os.Executable, "conformance-agent", "--conformance-fault", "publication", fixture)
			}
			failed := runTool(t, env, t.TempDir(), "", args...)
			t.Logf("fault=%s exit=%d stderr=%s", mode, failed.code, failed.stderr)
			if target := os.Getenv("TESTIFY_OFFICIAL_FAULTS"); target != "" {
				defer func() {
					require.NoError(t, os.MkdirAll(target, 0o700))
					require.NoError(t, writeJSONFile(filepath.Join(target, mode+".json"), object{"exitCode": failed.code, "stderr": failed.stderr, "files": officialInventory(t, fixture)}))
				}()
			}
			require.Equal(t, 1, failed.code, failed.stderr)
			switch mode {
			case "no-trigger":
				require.Equal(t, prior, readFile(t, filepath.Join(fixture, "current.json")))
				require.NoFileExists(t, filepath.Join(fixture, "official.json"))
				roots, err := filepath.Glob(filepath.Join(fixture, "failures", "official-record", "*"))
				require.NoError(t, err)
				require.Len(t, roots, 1)
				result := readJSON(t, filepath.Join(roots[0], "result.json"))
				require.Equal(t, "failed", result["status"])
				require.NotContains(t, result, "materialId")
			case "lost-notice", "partial-notice":
				journal := readJSON(t, filepath.Join(fixture, "official.json"))
				require.NotEmpty(t, journal["executionId"])
				require.NotEmpty(t, journal["materialId"])
				require.DirExists(t, filepath.Join(fixture, filepath.FromSlash(str(journal["staging"]))))
				require.NoDirExists(t, filepath.Join(fixture, filepath.FromSlash(str(journal["target"]))))
				require.Equal(t, journal["materialId"], readJSON(t, filepath.Join(fixture, "current.json"))["materialId"])
			default:
				require.NoFileExists(t, filepath.Join(fixture, "official.json"))
				paths, err := filepath.Glob(filepath.Join(fixture, "packages", "*", "record", "*", "result.json"))
				require.NoError(t, err)
				paths = ownOfficialEvidence(t, fixture, paths)
				require.Len(t, paths, 1)
				result := readJSON(t, paths[0])
				require.Equal(t, "failed", result["status"])
				require.NotEmpty(t, result["materialId"])
				switch mode {
				case "core-failed":
					require.Equal(t, prior, readFile(t, filepath.Join(fixture, "current.json")))
				default:
					require.Equal(t, true, result["publicationCommitted"])
					require.Equal(t, result["materialId"], readJSON(t, filepath.Join(fixture, "current.json"))["materialId"])
				}
			}
		})
	}
}

func TestOfficialCoreSealsBeforeParentReport(t *testing.T) {
	agent, drivers, env := isolatedCommands(t, t.TempDir(), "codex")
	setControlledOfficialDriver(t, drivers, "hold-after")
	fixture := prepareFixture(t, env, "--agent", "codex", "--case", "SDK-CASE-001", "--model", "synthetic-model")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), "official", "record", "--fixture-dir", fixture, "--cmd", agent, "--", "app-server", "--listen", "stdio://")
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var journal object
	require.Eventually(t, func() bool {
		if readJSONFile(filepath.Join(fixture, "official.json"), &journal) != nil {
			return false
		}
		_, err := os.Stat(filepath.Join(fixture, filepath.FromSlash(str(journal["staging"])), "checkpoint.json"))
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
	current := readFile(t, filepath.Join(fixture, "current.json"))
	core := filepath.Join(fixture, "packages", str(journal["materialId"]), "material")
	before := officialInventory(t, core)
	require.Contains(t, before, "material.json")
	require.NoFileExists(t, filepath.Join(fixture, filepath.FromSlash(str(journal["staging"])), "report.json"))
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	require.Error(t, <-done, stderr.String())
	require.Equal(t, current, readFile(t, filepath.Join(fixture, "current.json")))
	require.Equal(t, before, officialInventory(t, core))
	result := readJSON(t, filepath.Join(fixture, filepath.FromSlash(str(journal["target"])), "result.json"))
	require.Equal(t, "failed", result["status"])
	require.Equal(t, true, result["publicationCommitted"])
	require.NoFileExists(t, filepath.Join(fixture, "official.json"))
	if target := os.Getenv("TESTIFY_OFFICIAL_FAULTS"); target != "" {
		require.NoError(t, os.MkdirAll(target, 0o700))
		require.NoError(t, writeJSONFile(filepath.Join(target, "post-commit-parent-failure.json"), object{"exitCode": cmd.ProcessState.ExitCode(), "files": officialInventory(t, fixture), "coreBefore": before, "coreAfter": officialInventory(t, core)}))
	}
}
