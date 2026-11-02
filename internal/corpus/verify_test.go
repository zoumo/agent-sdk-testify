package corpus

import (
	"bufio"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVerifyOfficialOnlySelection(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--driver", "/never-execute"}, {"--driver-arg", "x"}, {"--peer", "/never-execute"}, {"--sdk-driver", "/never-execute"}} {
		cmd := exec.Command(os.Getenv("AGENT_SDK_TESTIFY_BIN"), append([]string{"verify"}, args...)...)
		cmd.Dir = t.TempDir()
		data, err := cmd.CombinedOutput()
		if args[0] == "--help" {
			require.NoError(t, err, "%s", data)
			require.Contains(t, string(data), "recording")
		} else {
			require.Error(t, err)
			require.Contains(t, string(data), "flag provided but not defined")
		}
	}
}

func verifyDriverSubstitute(args []string) int {
	if len(args) != 3 || args[1] != "--request" {
		return 2
	}
	mode := args[0]
	var req request
	if readJSON(args[2], &req) != nil {
		return 2
	}
	var plan object
	if readJSON(filepath.Join(req.Materials, "case.json"), &plan) != nil {
		return 2
	}
	if mode == "crash" {
		return 9
	}
	entries := []command{req.Peer.Command}
	if req.Peer.Probe != nil && mode != "omit-probe" {
		entries = append(entries, *req.Peer.Probe)
	}
	for _, entry := range entries {
		cmd := exec.Command(entry.Command, entry.Args...)
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
	if mode == "wrong-frames" || mode == "receipt-error" || mode == "not-consumed" || mode == "no-eof" || mode == "wrong-hash" || mode == "bad-lifecycle" {
		var receipt object
		name := "replay-peer.json"
		if mode == "bad-lifecycle" {
			name = "peer-main-lifecycle.json"
		}
		path := filepath.Join(req.Output, name)
		if readJSON(path, &receipt) != nil {
			return 5
		}
		switch mode {
		case "wrong-frames":
			receipt["frames"] = 99
		case "receipt-error":
			receipt["error"] = "synthetic protocol failure"
		case "not-consumed":
			receipt["fullConsumption"] = false
		case "no-eof":
			receipt["clientEOF"] = false
		case "wrong-hash":
			receipt["recordingSHA256"] = "wrong"
		case "bad-lifecycle":
			receipt["exitCode"] = 9
		}
		if writeJSON(path, receipt) != nil {
			return 5
		}
	}
	if mode == "no-receipt" {
		os.Remove(filepath.Join(req.Output, "replay-peer.json"))
	}
	sdk := object{"package": "openai-codex", "version": "synthetic-test-only"}
	report := object{"provider": plan["provider"], "caseId": plan["caseId"], "storyId": plan["storyId"], "status": status, "branchStatus": status, "captureStatus": "complete", "officialSDK": sdk}
	if mode == "partial-business" {
		report["captureStatus"] = "partial"
	}
	if mode == "wrong-sdk" {
		report["officialSDK"] = object{"package": "arbitrary-sdk", "version": "synthetic-only"}
	}
	if mode == "no-business" {
		report = object{}
	}
	if writeJSON(filepath.Join(req.Output, "replay-report.json"), report) != nil {
		return 4
	}
	if writeJSON(filepath.Join(req.Output, "result.json"), result{Status: status}) != nil {
		return 4
	}
	return 0
}

func verifyFixture(t *testing.T, mode string) (*exec.Cmd, string, string) {
	t.Helper()
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	s.evidence()
	raw := filepath.Join(s.source, "raw/raw.jsonl")
	file, e := os.OpenFile(raw, os.O_APPEND|os.O_WRONLY, 0600)
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
	script := fmt.Sprintf("#!/bin/sh\nexec %q verify-driver-substitute %q \"$@\"\n", self, mode)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "agent-sdk-testify-codex"), []byte(script), 0700))
	output := filepath.Join(t.TempDir(), "verify")
	cmd := exec.Command(os.Getenv("AGENT_SDK_TESTIFY_BIN"), "verify", "--recording", s.source, "--output", output)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "GORACE=atexit_sleep_ms=0"}
	return cmd, s.source, output
}

func TestVerifyOfficialExecutionKeepsSource(t *testing.T) {
	cmd, source, output := verifyFixture(t, "passed")
	before, err := Inventory(source, false)
	require.NoError(t, err)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	after, err := Inventory(source, false)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var evidence, summary object
	require.NoError(t, readJSON(filepath.Join(output, "verification.json"), &evidence))
	require.Equal(t, "passed", evidence["status"])
	require.Equal(t, "synthetic-test-only", obj(evidence["officialSDK"])["version"])
	require.NotEmpty(t, obj(evidence["tool"])["sha256"])
	require.NotEmpty(t, evidence["contracts"])
	require.NoError(t, readJSON(filepath.Join(output, "summary.json"), &summary))
	require.Equal(t, "verify", summary["mode"])
}

func TestVerifyRejectsWrongFrameCount(t *testing.T) {
	cmd, _, output := verifyFixture(t, "wrong-frames")
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var evidence object
	require.NoError(t, readJSON(filepath.Join(output, "verification.json"), &evidence))
	require.Equal(t, "failed", evidence["status"])
}

func TestVerifyRequiresPeerExitAndCleanup(t *testing.T) {
	cmd, _, output := verifyFixture(t, "passed")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var lifecycle object
	require.NoError(t, readJSON(filepath.Join(output, "peer-main-lifecycle.json"), &lifecycle))
	require.Equal(t, float64(0), lifecycle["exitCode"])
	require.Equal(t, true, lifecycle["reaped"])
	require.Equal(t, true, lifecycle["cleanupComplete"])
}

func TestVerifyFailureEvidence(t *testing.T) {
	for _, mode := range []string{"no-receipt", "wrong-frames", "receipt-error", "not-consumed", "no-eof", "wrong-hash", "bad-lifecycle", "partial-business", "wrong-sdk", "no-business", "failed", "crash"} {
		t.Run(mode, func(t *testing.T) {
			cmd, source, output := verifyFixture(t, mode)
			before, err := Inventory(source, false)
			require.NoError(t, err)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			var evidence object
			require.NoError(t, readJSON(filepath.Join(output, "verification.json"), &evidence))
			require.Equal(t, "failed", evidence["status"])
			require.NotEmpty(t, evidence["error"])
			after, err := Inventory(source, false)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.FileExists(t, filepath.Join(output, "stderr.log"))
		})
	}
}

func TestVerifyCorruptSourceRetainsDiagnostic(t *testing.T) {
	cmd, source, output := verifyFixture(t, "passed")
	require.NoError(t, os.WriteFile(filepath.Join(source, "raw/raw.jsonl"), []byte("corrupt\n"), 0600))
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var evidence object
	require.NoError(t, readJSON(filepath.Join(output, "verification.json"), &evidence))
	require.Contains(t, evidence["error"], "hash mismatch")
	require.NoFileExists(t, filepath.Join(output, "request.json"))
}

func TestVerifyAllSelectedPeers(t *testing.T) {
	for _, mode := range []string{"passed", "omit-probe"} {
		t.Run(mode, func(t *testing.T) {
			cmd, source, output := verifyFixture(t, mode)
			require.NoError(t, copyTree(filepath.Join(source, "raw"), filepath.Join(source, "remote-probe/raw")))
			var plan, manifest object
			require.NoError(t, readJSON(filepath.Join(source, "plan.json"), &plan))
			plan["driverProfile"], plan["caseId"], plan["storyId"] = "remote-close", "SDK-CASE-032", "SDK-US-007"
			require.NoError(t, writeJSON(filepath.Join(source, "plan.json"), plan))
			require.NoError(t, writeJSON(filepath.Join(source, "remote-service.json"), object{"cleanupComplete": true, "serverExitCode": 0, "forcedCleanup": false}))
			require.NoError(t, readJSON(filepath.Join(source, "manifest.json"), &manifest))
			digest, err := Digest(filepath.Join(source, "plan.json"))
			require.NoError(t, err)
			manifest["planSHA256"] = digest
			require.NoError(t, writeJSON(filepath.Join(source, "manifest.json"), manifest))
			data, err := cmd.CombinedOutput()
			if mode == "passed" {
				require.NoError(t, err, "%s", data)
				require.FileExists(t, filepath.Join(output, "remote-probe/replay-peer.json"))
				require.FileExists(t, filepath.Join(output, "peer-probe-lifecycle.json"))
			} else {
				require.Error(t, err, "%s", data)
				require.Contains(t, string(data), "probe peer")
			}
		})
	}
}

func TestVerifyQualifiesAndAdoptsWithoutCorpusCommand(t *testing.T) {
	cmd, source, output := verifyFixture(t, "passed")
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	scenarios, err := filepath.Abs(s.c.Scenarios)
	require.NoError(t, err)
	cmd.Args = append(cmd.Args, "--index", s.c.Index, "--scenarios", scenarios, "--archive", s.c.Archive)
	before, err := Inventory(source, false)
	require.NoError(t, err)
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var admission object
	require.NoError(t, readJSON(filepath.Join(output, "admission-summary.json"), &admission))
	require.Equal(t, "admitted", admission["status"])
	require.FileExists(t, filepath.Join(str(admission["materials"]), "official-replay/verification.json"))
	_, err = s.c.VerifyAdopted(s.entry())
	require.NoError(t, err)
	after, err := Inventory(source, false)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestVerifyNeverWritesInsideSource(t *testing.T) {
	for _, mode := range []string{"explicit", "default", "alias"} {
		t.Run(mode, func(t *testing.T) {
			cmd, source, _ := verifyFixture(t, "passed")
			cmd.Args = cmd.Args[:len(cmd.Args)-2]
			switch mode {
			case "explicit":
				cmd.Args = append(cmd.Args, "--output", filepath.Join(source, "verification"))
			case "default":
				cmd.Env = append(cmd.Env, "AGENT_SDK_TESTIFY_DATA_DIR="+source)
			case "alias":
				link := filepath.Join(t.TempDir(), "alias")
				require.NoError(t, os.Symlink(source, link))
				cmd.Args = append(cmd.Args, "--output", filepath.Join(link, "verification"))
			}
			before, err := Inventory(source, false)
			require.NoError(t, err)
			data, err := cmd.CombinedOutput()
			require.Error(t, err, "%s", data)
			after, err := Inventory(source, false)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestVerifyRejectsAdoptedEvidenceDrift(t *testing.T) {
	cmd, _, output := verifyFixture(t, "passed")
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	s.evidence()
	source := s.adopt(s.qualify())
	var report object
	require.NoError(t, readJSON(filepath.Join(source, "report.json"), &report))
	report["facts"] = []string{"changed after adoption"}
	require.NoError(t, writeJSON(filepath.Join(source, "report.json"), report))
	cmd.Args[3] = source
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	var evidence object
	require.NoError(t, readJSON(filepath.Join(output, "verification.json"), &evidence))
	require.Contains(t, evidence["error"], "adopted corpus inventory drift")
	require.NoFileExists(t, filepath.Join(output, "request.json"))
}

func TestVerifyRejectsAdmissionAndPreservesPrevious(t *testing.T) {
	cmd, source, output := verifyFixture(t, "failed")
	s := &corpusSuite{}
	s.SetT(t)
	s.SetupTest()
	s.evidence()
	previous := s.adopt(s.qualify())
	indexBefore, err := os.ReadFile(s.c.Index)
	require.NoError(t, err)
	previousBefore, err := Inventory(previous, false)
	require.NoError(t, err)
	sourceBefore, err := Inventory(source, false)
	require.NoError(t, err)
	scenarios, err := filepath.Abs(s.c.Scenarios)
	require.NoError(t, err)
	cmd.Args = append(cmd.Args, "--index", s.c.Index, "--scenarios", scenarios, "--archive", s.c.Archive)
	data, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", data)
	indexAfter, err := os.ReadFile(s.c.Index)
	require.NoError(t, err)
	require.Equal(t, indexBefore, indexAfter)
	previousAfter, err := Inventory(previous, false)
	require.NoError(t, err)
	require.Equal(t, previousBefore, previousAfter)
	sourceAfter, err := Inventory(source, false)
	require.NoError(t, err)
	require.Equal(t, sourceBefore, sourceAfter)
	var admission object
	require.NoError(t, readJSON(filepath.Join(output, "admission-summary.json"), &admission))
	require.Equal(t, "rejected", admission["status"])
}

func TestVerifyIgnoresRecordedDriverCommands(t *testing.T) {
	cmd, source, _ := verifyFixture(t, "passed")
	var summary object
	require.NoError(t, readJSON(filepath.Join(source, "summary.json"), &summary))
	summary["driver"] = "/never-execute-recorded-sdk"
	summary["driverArgs"] = []string{"arbitrary-sdk"}
	require.NoError(t, writeJSON(filepath.Join(source, "summary.json"), summary))
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
}
