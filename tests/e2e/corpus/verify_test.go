package corpus

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/tests/e2e/fixturetest"
)

func TestVerifyOfficialOnlySelection(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--driver", "/never-execute"}, {"--driver-arg", "x"}, {"--peer", "/never-execute"}, {"--sdk-driver", "/never-execute"}} {
		cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), append([]string{"official", "verify"}, args...)...)
		cmd.Dir = t.TempDir()
		data, err := cmd.CombinedOutput()
		if args[0] == "--help" {
			require.NoError(t, err, "%s", data)
			require.Contains(t, string(data), "fixture-dir")
		} else {
			require.Error(t, err)
			require.Contains(t, string(data), "unknown flag")
		}
	}
}

func verifyDriverSubstitute(args []string) int {
	if len(args) != 3 || args[1] != "--request" {
		return 2
	}
	mode := args[0]
	var req driverRequest
	if tool.ReadJSON(args[2], &req) != nil {
		return 2
	}
	var runtime driverRuntime
	if tool.ReadJSON(req.Runtime, &runtime) != nil || runtime.SchemaVersion != 1 {
		return 2
	}
	var plan tool.Object
	if tool.ReadJSON(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	if mode == "crash" {
		return 9
	}
	entries := []tool.Command{runtime.Peer.Command}
	if runtime.Peer.Probe != nil && mode != "omit-probe" {
		entries = append(entries, *runtime.Peer.Probe)
	}
	for _, entry := range entries {
		cmd := exec.CommandContext(context.Background(), entry.Command, entry.Args...)
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if cmd.Start() != nil {
			return 3
		}
		fmt.Fprintln(in, `{"id":1,"method":"ping"}`)
		in.Close()
		_, _ = bufio.NewReader(out).ReadBytes('\n')
		_ = cmd.Wait()
	}
	status := "passed"
	if mode == "failed" {
		status = "failed"
	}
	if mode == "wrong-hash" {
		var receipt tool.Object
		var receiptPath string
		var events []tool.Object
		notifications, err := os.ReadFile(runtime.AgentResults["main"])
		if err != nil {
			return 5
		}
		for row := range strings.SplitSeq(strings.TrimSpace(string(notifications)), "\n") {
			var event tool.Object
			if json.Unmarshal([]byte(row), &event) != nil {
				return 5
			}
			events = append(events, event)
		}
		if len(events) != 2 {
			return 5
		}
		fixtureRoot := runtime.FixtureDir
		receiptPath = filepath.Join(fixtureRoot, "packages", tool.Str(events[1]["materialId"]), filepath.FromSlash(tool.Str(events[1]["receipt"])))
		path := receiptPath
		if tool.ReadJSON(path, &receipt) != nil {
			return 5
		}
		receipt["recordingSHA256"] = "wrong"
		if tool.WriteJSON(path, receipt) != nil {
			return 5
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return 5
	}
	sdk := tool.Object{"package": "openai-codex", "version": "synthetic-test-only"}
	observed := tool.Object{
		"cwd":       cwd,
		"workspace": runtime.Workspace,
		"home":      os.Getenv("HOME"),
		"config":    os.Getenv("CODEX_HOME"),
		"tmp":       os.Getenv("TMPDIR"),
	}
	report := tool.Object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": status, "branchStatus": status, "captureStatus": "complete", "officialSDK": sdk, "observed": observed}
	if tool.WriteJSON(filepath.Join(runtime.Output, "replay-report.json"), report) != nil {
		return 4
	}
	if tool.WriteJSON(filepath.Join(runtime.Output, "result.json"), tool.Result{Status: status}) != nil {
		return 4
	}
	return 0
}

func verifyFixture(t *testing.T, mode string) (*exec.Cmd, string, string) {
	return verifyFixtureWithMutation(t, mode, nil)
}

func verifyFixtureWithMutation(t *testing.T, mode string, mutate func(*testing.T, string)) (*exec.Cmd, string, string) {
	t.Helper()
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	s.evidence()
	canonicalVerifyFixture(t, s.source, "SDK-CASE-001")
	raw := filepath.Join(s.source, "raw", "raw.jsonl")
	file, e := os.OpenFile(raw, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, e)
	_, e = file.WriteString(`{"ordinal":2,"direction":"s2c","channel":"protocol","epoch":1,"elapsedMs":1,"wire":{"id":1,"result":{}}}` + "\n")
	require.NoError(t, e)
	require.NoError(t, file.Close())
	transport := s.read(s.source, "raw/transport.json")
	transport["frames"] = 2
	s.write(s.source, "raw/transport.json", transport)
	manifest := s.read(s.source, "raw/manifest.json")
	delete(manifest, "files")
	s.write(s.source, "raw/manifest.json", manifest)
	files := s.inventory(filepath.Join(s.source, "raw"))
	delete(files, "manifest.json")
	manifest["files"] = files
	s.write(s.source, "raw/manifest.json", manifest)
	manifest = s.read(s.source, "manifest.json")
	manifest["files"] = s.inventory(filepath.Join(s.source, "raw"))
	s.write(s.source, "manifest.json", manifest)
	summary := s.read(s.source, "summary.json")
	summary["recordingSHA256"] = s.hash(s.source, "raw/raw.jsonl")
	s.write(s.source, "summary.json", summary)
	bin := t.TempDir()
	self, err := os.Executable()
	require.NoError(t, err)
	script := fmt.Sprintf("#!/bin/sh\n%sexec %q verify-driver-substitute %q \"$@\"\n", testutil.RaceShellExport(), self, mode)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "agent-sdk-testify-codex"), []byte(script), 0o700))
	if mutate != nil {
		mutate(t, s.source)
	}
	env := []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "GORACE=" + os.Getenv("GORACE")}
	fixtureDir := prepareVerifyFixture(t, s.source, env)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		paths, err := fixturetest.OfficialStderr(fixtureDir)
		if err != nil {
			t.Logf("official diagnostics: %v", err)
			return
		}
		for _, path := range paths {
			if data, err := os.ReadFile(path); err == nil {
				t.Logf("driver diagnostics (%s): %s", path, data)
			}
		}
	})
	cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "official", "verify", "--fixture-dir", fixtureDir)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	return cmd, s.source, fixtureDir
}

func prepareVerifyFixture(t *testing.T, source string, env []string) string {
	t.Helper()
	fixtureDir := filepath.Join(t.TempDir(), "fixture")
	var plan tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(source, "plan.json"), &plan))
	prepare := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"),
		"prepare", "--agent", "codex", "--case", tool.Str(plan["caseId"]), "--fixture-dir", fixtureDir)
	prepare.Dir = t.TempDir()
	prepare.Env = env
	data, err := prepare.CombinedOutput()
	require.NoError(t, err, "%s", data)
	// Install a controlled official capture at the process boundary. Production
	// prepare --recording must not elevate an external capture to official.
	var registry tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "packages", "index.json"), &registry))
	material := tool.Obj(tool.Array(registry["materials"])[0])
	target := filepath.Join(fixtureDir, "packages", tool.Str(material["id"]), "material")
	require.NoError(t, os.RemoveAll(target))
	require.NoError(t, tool.CopyTree(source, target))
	if plan["driverProfile"] == "remote-close" {
		material["connections"] = []string{"main", "probe"}
	}
	material["kind"], material["action"], material["provenance"] = "capture", "record", "official"
	delete(material, "officialOrigin")
	delete(material, "legacyInventorySHA256")
	delete(registry, "descriptorless")
	material["formatVersion"] = 2
	files, err := corpus.Inventory(target, false)
	require.NoError(t, err)
	material["files"] = files
	material["planSHA256"], material["manifestSHA256"], material["recordingSHA256"] = files["plan.json"], files["manifest.json"], files["raw/raw.jsonl"]
	require.NoError(t, tool.WriteJSON(filepath.Join(target, "material.json"), material))
	require.NoError(t, tool.WriteJSON(filepath.Join(fixtureDir, "packages", "index.json"), registry))
	var current tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "current.json"), &current))
	current["materialSHA256"], err = corpus.Digest(filepath.Join(target, "material.json"))
	require.NoError(t, err)
	current["manifestSHA256"] = files["manifest.json"]
	delete(current, "legacyFormat")
	delete(current, "legacyInventorySHA256")
	require.NoError(t, tool.WriteJSON(filepath.Join(fixtureDir, "current.json"), current))
	t.Cleanup(func() {
		var descriptor tool.Object
		if tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor) == nil {
			_ = os.RemoveAll(filepath.Join(fixtureDir, "runtime"))
		}
		_ = os.RemoveAll(fixtureDir)
	})
	return fixtureDir
}

func verifyOutput(t *testing.T, fixtureDir string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(fixtureDir, "packages", "*", "verify", "*", "verification.json"))
	require.NoError(t, err)
	paths, err = fixturetest.OwnOfficialEvidence(fixtureDir, paths)
	require.NoError(t, err)
	require.Len(t, paths, 1)
	return filepath.Dir(paths[0])
}

func TestVerifyOfficialExecutionKeepsSource(t *testing.T) {
	cmd, source, fixtureDir := verifyFixture(t, "passed")
	before, err := corpus.Inventory(source, false)
	require.NoError(t, err)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	output := verifyOutput(t, fixtureDir)
	after, err := corpus.Inventory(source, false)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var evidence, summary tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "verification.json"), &evidence))
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, "synthetic-test-only", tool.Obj(readObject(t, filepath.Join(output, "replay-report.json"))["officialSDK"])["version"])
	require.NotEmpty(t, tool.Obj(evidence["tool"])["sha256"])
	require.NotEmpty(t, evidence["contracts"])
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "result.json"), &summary))
	require.Equal(t, evidence["id"], summary["id"])
	require.Equal(t, evidence["materialId"], summary["materialId"])
	require.EqualValues(t, 3, summary["schemaVersion"])
}

func TestVerifyExecutionDescriptorMatchesOfficialExecutorPaths(t *testing.T) {
	cmd, _, fixtureDir := verifyFixture(t, "passed")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	root := verifyOutput(t, fixtureDir)
	var evidence, report, descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "verification.json"), &evidence))
	require.NoError(t, tool.ReadJSON(filepath.Join(root, "replay-report.json"), &report))
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
	resolvedFixture, err := filepath.EvalSymlinks(fixtureDir)
	require.NoError(t, err)
	routeRoot := filepath.Join(resolvedFixture, "runtime", "routes", "mock")
	routeWorkspace := filepath.Join(routeRoot, "workspace")
	observed := tool.Obj(report["observed"])
	hostCwd, err := filepath.EvalSymlinks(routeWorkspace)
	require.NoError(t, err)
	require.Equal(t, hostCwd, observed["cwd"])
	require.Equal(t, routeWorkspace, observed["workspace"])
	for _, pair := range [][2]string{{"home", "home"}, {"config", "config"}, {"tmp", "tmp"}} {
		require.Equal(t, filepath.Join(routeRoot, pair[1]), observed[pair[0]])
	}
	refs := tool.Array(evidence["executions"])
	require.Len(t, refs, 1)
	var execution tool.Object
	executionRoot, err := fixturetest.ExecutionRoot(fixtureDir, tool.Str(tool.Obj(refs[0])["executionId"]))
	require.NoError(t, err)
	require.NoError(t, tool.ReadJSON(filepath.Join(executionRoot, "execution.json"), &execution))
	require.Equal(t, "mock", execution["command"])
	workspace := filepath.Join(fixtureDir, "runtime", "executions", tool.Str(execution["executionId"]), "workspace")
	require.True(t, strings.HasPrefix(workspace, filepath.Join(fixtureDir, "runtime", "executions")+string(filepath.Separator)))
	require.NotEqual(t, workspace, routeWorkspace)
}

func TestVerifyRequiresPeerExitAndCleanup(t *testing.T) {
	cmd, _, fixtureDir := verifyFixture(t, "passed")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	output := verifyOutput(t, fixtureDir)
	var lifecycle tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "peers", "main.json"), &lifecycle))
	require.InDelta(t, float64(0), lifecycle["exitCode"], 0)
	require.Equal(t, true, lifecycle["reaped"])
	require.Equal(t, true, lifecycle["cleanupComplete"])
}

func TestVerifyFailureEvidence(t *testing.T) {
	for _, mode := range []string{"wrong-hash"} {
		t.Run(mode, func(t *testing.T) {
			cmd, source, fixtureDir := verifyFixture(t, mode)
			before, err := corpus.Inventory(source, false)
			require.NoError(t, err)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			output := verifyOutput(t, fixtureDir)
			var evidence tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(output, "verification.json"), &evidence))
			require.Equal(t, "failed", evidence["status"])
			require.NotEmpty(t, evidence["error"])
			after, err := corpus.Inventory(source, false)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.FileExists(t, filepath.Join(output, "stderr.log"))
		})
	}
}

func TestVerifyCorruptSourceRetainsDiagnostic(t *testing.T) {
	for _, target := range []string{"original", "fixture"} {
		t.Run(target, func(t *testing.T) {
			cmd, source, fixtureDir := verifyFixture(t, "passed")
			if target == "fixture" {
				var descriptor tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
				var current, registry tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "current.json"), &current))
				require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "packages", "index.json"), &registry))
				for _, value := range tool.Array(registry["materials"]) {
					material := tool.Obj(value)
					if material["id"] == current["materialId"] {
						source = filepath.Join(fixtureDir, "packages", tool.Str(material["id"]), "material")
					}
				}
			}
			require.NoError(t, os.WriteFile(filepath.Join(source, "raw", "raw.jsonl"), []byte("corrupt\n"), 0o600))
			data, err := cmd.CombinedOutput()
			if target == "original" {
				require.NoError(t, err, "%s", data)
				return
			}
			require.Error(t, err, "%s", data)
			require.Contains(t, string(data), "hash mismatch")
			var index tool.Object
			require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "executions", "index.json"), &index))
			require.Empty(t, tool.Array(index["executions"]))
		})
	}
}

func TestVerifyAllSelectedPeers(t *testing.T) {
	for _, mode := range []string{"passed", "omit-probe"} {
		t.Run(mode, func(t *testing.T) {
			cmd, _, fixtureDir := verifyFixtureWithMutation(t, mode, func(t *testing.T, source string) {
				require.NoError(t, tool.CopyTree(filepath.Join(source, "raw"), filepath.Join(source, "remote-probe", "raw")))
				probeRoot := filepath.Join(source, "remote-probe", "raw")
				raw, err := os.ReadFile(filepath.Join(probeRoot, "raw.jsonl"))
				require.NoError(t, err)
				raw = []byte(strings.Replace(string(raw), `"result":{}`, `"result":{"phase":"probe"}`, 1))
				require.NoError(t, os.WriteFile(filepath.Join(probeRoot, "raw.jsonl"), raw, 0o600))
				var probeManifest tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(probeRoot, "manifest.json"), &probeManifest))
				tool.Obj(probeManifest["files"])["raw.jsonl"] = tool.DigestBytes(raw)
				require.NoError(t, tool.WriteJSON(filepath.Join(probeRoot, "manifest.json"), probeManifest))
				var plan, manifest tool.Object
				require.NoError(t, tool.ReadJSON(filepath.Join(source, "plan.json"), &plan))
				canonicalVerifyFixture(t, source, "SDK-CASE-032")
				require.NoError(t, tool.ReadJSON(filepath.Join(source, "plan.json"), &plan))
				require.NoError(t, tool.WriteJSON(filepath.Join(source, "plan.json"), plan))
				require.NoError(t, tool.WriteJSON(filepath.Join(source, "remote-service.json"), tool.Object{"cleanupComplete": true, "serverExitCode": 0, "forcedCleanup": false}))
				require.NoError(t, tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest))
				digest, err := corpus.Digest(filepath.Join(source, "plan.json"))
				require.NoError(t, err)
				manifest["planSHA256"] = digest
				probeFiles, err := corpus.Inventory(filepath.Join(source, "remote-probe", "raw"), false)
				require.NoError(t, err)
				remoteFiles := tool.Object{}
				for name, hash := range probeFiles {
					remoteFiles["remote-probe/raw/"+name] = hash
				}
				manifest["remotePhaseFiles"] = remoteFiles
				require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), manifest))
			})
			data, err := cmd.CombinedOutput()
			t.Logf("official verify stderr/result: %s", data)
			if mode == "passed" {
				require.NoError(t, err, "%s", data)
				output := verifyOutput(t, fixtureDir)
				evidence := readObject(t, filepath.Join(output, "verification.json"))
				refs := tool.Array(evidence["executions"])
				require.Len(t, refs, 1)
				executionRoot, err := fixturetest.ExecutionRoot(fixtureDir, tool.Str(tool.Obj(refs[0])["executionId"]))
				require.NoError(t, err)
				require.FileExists(t, filepath.Join(executionRoot, "receipts", "probe.json"))
				require.FileExists(t, filepath.Join(output, "peers", "probe.json"))
			} else {
				require.Error(t, err, "%s", data)
				require.Contains(t, string(data), "probe: protocol process not started or settled")
				require.Contains(t, string(data), "official execution is not settled; journal retained")
				journal := readObject(t, filepath.Join(fixtureDir, "official.json"))
				require.Equal(t, "running", journal["status"])
				require.Equal(t, "verify", journal["mode"])
				staging := filepath.Join(fixtureDir, filepath.FromSlash(tool.Str(journal["staging"])))
				require.FileExists(t, filepath.Join(staging, "replay-report.json"))
				require.FileExists(t, filepath.Join(staging, "stderr.log"))
				require.NoFileExists(t, filepath.Join(fixtureDir, filepath.FromSlash(tool.Str(journal["target"])), "verification.json"))
				index := readObject(t, filepath.Join(fixtureDir, "executions", "index.json"))
				rows := tool.Array(index["executions"])
				require.Len(t, rows, 1)
				require.Equal(t, "open", tool.Obj(rows[0])["status"])
				for _, value := range tool.Array(tool.Obj(rows[0])["connections"]) {
					connection := tool.Obj(value)
					if connection["id"] == "probe" {
						require.Equal(t, "pending", connection["status"])
					}
				}
			}
		})
	}
}

func TestVerifyQualifiesAndAdoptsWithoutCorpusCommand(t *testing.T) {
	cmd, source, fixtureDir := verifyFixture(t, "passed")
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	scenarios, err := filepath.Abs(s.c.Scenarios)
	require.NoError(t, err)
	workingDir, err := filepath.EvalSymlinks(cmd.Dir)
	require.NoError(t, err)
	indexArg, err := filepath.Rel(workingDir, s.c.Index)
	require.NoError(t, err)
	scenariosArg, err := filepath.Rel(workingDir, scenarios)
	require.NoError(t, err)
	archiveArg, err := filepath.Rel(workingDir, s.c.Archive)
	require.NoError(t, err)
	cmd.Args = append(cmd.Args, "--index", indexArg, "--scenarios", scenariosArg, "--archive", archiveArg)
	before, err := corpus.Inventory(source, false)
	require.NoError(t, err)
	current := readObject(t, filepath.Join(fixtureDir, "current.json"))
	core := filepath.Join(fixtureDir, "packages", tool.Str(current["materialId"]), "material")
	expectedPublished, err := corpus.Inventory(core, false)
	require.NoError(t, err)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	output := verifyOutput(t, fixtureDir)
	var admission tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "runtime", "official", filepath.Base(output), "admission-summary.json"), &admission))
	require.Equal(t, "admitted", admission["status"])
	published := tool.Str(admission["materials"])
	archived := filepath.Join(s.c.Archive, filepath.Base(published))
	require.FileExists(t, filepath.Join(archived, "verify", filepath.Base(output), "verification.json"))
	require.NoDirExists(t, filepath.Join(published, "verify"))
	publishedFiles, err := corpus.Inventory(published, false)
	require.NoError(t, err)
	require.Equal(t, expectedPublished, publishedFiles)
	var result, evidence tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "result.json"), &result))
	require.NoError(t, tool.ReadJSON(filepath.Join(output, "verification.json"), &evidence))
	require.NotContains(t, result, "admission")
	require.NotContains(t, evidence, "admission")
	normalizedIndex, err := filepath.Abs(s.c.Index)
	require.NoError(t, err)
	normalizedScenarios, err := filepath.Abs(scenarios)
	require.NoError(t, err)
	normalizedArchive, err := corpus.Resolved(s.c.Archive)
	require.NoError(t, err)
	require.Equal(t, normalizedIndex, admission["index"])
	require.Equal(t, normalizedScenarios, admission["scenarios"])
	require.Equal(t, normalizedArchive, admission["archive"])
	_, err = s.c.VerifyAdopted(s.entry())
	require.NoError(t, err)
	after, err := corpus.Inventory(source, false)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestVerifyRejectsAdoptedEvidenceDrift(t *testing.T) {
	cmd, _, _ := verifyFixture(t, "passed")
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	s.evidence()
	canonicalVerifyFixture(t, s.source, "SDK-CASE-001")
	s.bindReplay()
	source := s.adopt(s.qualify())
	var report tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(source, "report.json"), &report))
	report["facts"] = []string{"changed after adoption"}
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "report.json"), report))
	env := cmd.Env
	fixtureDir := prepareVerifyFixture(t, source, env)
	cmd = exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "official", "verify", "--fixture-dir", fixtureDir)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	before := fixtureFiles(t, fixtureDir)
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	require.Contains(t, string(data), "adopted corpus inventory drift")
	require.Equal(t, before, fixtureFiles(t, fixtureDir))
	require.NoFileExists(t, filepath.Join(fixtureDir, "verifications", "index.json"))
}

func TestVerifyRejectsAdmissionAndPreservesPrevious(t *testing.T) {
	cmd, source, fixtureDir := verifyFixture(t, "failed")
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	s.evidence()
	previous := s.adopt(s.qualify())
	indexBefore, err := os.ReadFile(s.c.Index)
	require.NoError(t, err)
	previousBefore, err := corpus.Inventory(previous, false)
	require.NoError(t, err)
	sourceBefore, err := corpus.Inventory(source, false)
	require.NoError(t, err)
	scenarios, err := filepath.Abs(s.c.Scenarios)
	require.NoError(t, err)
	cmd.Args = append(cmd.Args, "--index", s.c.Index, "--scenarios", scenarios, "--archive", s.c.Archive)
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	output := verifyOutput(t, fixtureDir)
	indexAfter, err := os.ReadFile(s.c.Index)
	require.NoError(t, err)
	require.Equal(t, indexBefore, indexAfter)
	previousAfter, err := corpus.Inventory(previous, false)
	require.NoError(t, err)
	require.Equal(t, previousBefore, previousAfter)
	sourceAfter, err := corpus.Inventory(source, false)
	require.NoError(t, err)
	require.Equal(t, sourceBefore, sourceAfter)
	var admission tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "runtime", "official", filepath.Base(output), "admission-summary.json"), &admission))
	require.Equal(t, "rejected", admission["status"])
}

func TestVerifyIgnoresRecordedDriverCommands(t *testing.T) {
	cmd, source, _ := verifyFixture(t, "passed")
	var summary tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(source, "summary.json"), &summary))
	summary["driver"] = "/never-execute-recorded-sdk"
	summary["driverArgs"] = []string{"arbitrary-sdk"}
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "summary.json"), summary))
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
}

func canonicalVerifyFixture(t *testing.T, source, caseID string) {
	t.Helper()
	var previous tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(source, "plan.json"), &previous))
	fixtureDir := filepath.Join(t.TempDir(), "canonical")
	cmd := exec.CommandContext(t.Context(), os.Getenv("AGENT_SDK_TESTIFY_BIN"), "prepare", "--agent", "codex", "--case", caseID, "--fixture-dir", fixtureDir)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	var descriptor, plan tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, "fixture.json"), &descriptor))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(fixtureDir, "runtime")) })
	require.NoError(t, tool.ReadJSON(filepath.Join(fixtureDir, filepath.FromSlash(tool.Str(tool.Obj(descriptor["source"])["path"]))), &plan))
	maps.Copy(previous, plan)
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "plan.json"), previous))
	var manifest tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(source, "manifest.json"), &manifest))
	digest, err := corpus.Digest(filepath.Join(source, "plan.json"))
	require.NoError(t, err)
	manifest["planSHA256"] = digest
	require.NoError(t, tool.WriteJSON(filepath.Join(source, "manifest.json"), manifest))
}

func fixtureFiles(t *testing.T, fixture string) tool.Object {
	t.Helper()
	files := tool.Object{}
	require.NoError(t, filepath.WalkDir(fixture, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files[rel] = "symlink:" + target
			return nil
		}
		digest, err := corpus.Digest(path)
		if err != nil {
			return err
		}
		files[rel] = digest
		return nil
	}))
	return files
}

func readObject(t *testing.T, path string) tool.Object {
	t.Helper()
	var value tool.Object
	require.NoError(t, tool.ReadJSON(path, &value))
	return value
}
