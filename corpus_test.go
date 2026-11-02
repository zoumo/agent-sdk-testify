package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// All evidence fabricated by this suite stays in temporary directories.
type corpusSuite struct {
	suite.Suite
	root, source, replay string
	c                    corpus
}

func TestCorpus(t *testing.T) { suite.Run(t, new(corpusSuite)) }

func (s *corpusSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.source, s.replay = filepath.Join(s.root, "capture"), filepath.Join(s.root, "replay")
	s.c = corpus{Index: filepath.Join(s.root, "corpus", "index.json"), Scenarios: "tests/simulation/scenarios", Archive: filepath.Join(s.root, "archive")}
	index := s.read("tests/simulation/recordings", "index.json")
	for _, value := range array(index["cases"]) {
		e := obj(value)
		for _, key := range []string{"materials", "adoptionSHA256"} {
			delete(e, key)
		}
		e["adoption"] = "missing"
		e["verification"] = "unverified"
		e["verificationReason"] = "synthetic suite has no admitted evidence"
	}
	s.write(filepath.Dir(s.c.Index), "index.json", index)
}

func (s *corpusSuite) write(root, name string, value any) {
	s.T().Helper()
	s.Require().NoError(os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700))
	s.Require().NoError(writeJSON(filepath.Join(root, name), value))
}
func (s *corpusSuite) read(root, name string) object {
	s.T().Helper()
	v, err := corpusRead(filepath.Join(root, name))
	s.Require().NoError(err)
	return v
}
func (s *corpusSuite) inventory(root string) object {
	s.T().Helper()
	v, err := corpusInventory(root, false)
	s.Require().NoError(err)
	return v
}
func (s *corpusSuite) hash(root, name string) string {
	s.T().Helper()
	v, err := corpusDigest(filepath.Join(root, name))
	s.Require().NoError(err)
	return v
}
func (s *corpusSuite) bindReplay() {
	summary := s.read(s.replay, "summary.json")
	files := object{}
	for _, name := range sourceEvidence {
		files[name] = s.hash(s.source, name)
	}
	summary["source"] = files
	s.write(s.replay, "summary.json", summary)
}
func (s *corpusSuite) evidence() object {
	plan := object{"provider": "codex", "caseId": "SDK-CASE-001", "storyId": "SDK-US-001",
		"driverProfile": "continuation", "project": "/original/project", "recordingIsolation": "personal-skills-v1",
		"scenario": object{"files": object{"numbers.txt": "7\n11\n"}}}
	s.write(s.source, "plan.json", plan)
	s.write(s.source, "raw/raw.jsonl", object{"ordinal": 1, "direction": "c2s", "channel": "protocol",
		"epoch": 1, "elapsedMs": 0, "wire": object{"id": 1, "method": "ping"}})
	s.Require().NoError(os.WriteFile(filepath.Join(s.source, "raw/raw.jsonl"), []byte("{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"protocol\",\"epoch\":1,\"elapsedMs\":0,\"wire\":{\"id\":1,\"method\":\"ping\"}}\n"), 0600))
	s.Require().NoError(os.WriteFile(filepath.Join(s.source, "raw/stderr.raw"), nil, 0600))
	s.write(s.source, "raw/transport.json", object{"captureStatus": "complete", "cleanupComplete": true,
		"nativeLaunched": true, "reaped": true, "clientEOF": true, "frames": 1})
	s.write(s.source, "raw/manifest.json", object{"captureStatus": "complete", "files": s.inventory(filepath.Join(s.source, "raw")),
		"native":          object{"executable": "/observed/native", "sha256": strings.Repeat("a", 64), "cwd": "/original/project"},
		"recordingFormat": object{"name": "agent-mock-jsonl", "version": 1, "transport": "stdio-jsonl"}})
	sdk := object{"package": "openai-codex", "version": "synthetic-test-only"}
	business := object{"status": "passed", "branchStatus": "passed", "captureStatus": "complete",
		"provider": "codex", "caseId": "SDK-CASE-001", "storyId": "SDK-US-001", "officialSDK": sdk}
	s.write(s.source, "report.json", business)
	s.write(s.replay, "replay-report.json", business)
	s.write(s.source, "result.json", object{"status": "passed"})
	s.write(s.replay, "result.json", object{"status": "passed"})
	s.write(s.source, "manifest.json", object{"provenance": "capture", "status": "passed", "captureStatus": "complete",
		"credentialScan": "pass", "planSHA256": s.hash(s.source, "plan.json"), "files": s.inventory(filepath.Join(s.source, "raw"))})
	hash := s.hash(s.source, "raw/raw.jsonl")
	s.write(s.source, "summary.json", object{"provider": "codex", "case": "SDK-CASE-001", "mode": "record",
		"result": object{"status": "passed"}, "recordingSHA256": hash})
	s.write(s.replay, "summary.json", object{"provider": "codex", "case": "SDK-CASE-001", "mode": "replay",
		"result": object{"status": "passed"}, "recordingSHA256": hash})
	s.bindReplay()
	s.write(s.replay, "replay-peer.json", object{"provider": "codex", "recordingSHA256": hash,
		"fullConsumption": true, "clientEOF": true, "nativeLaunched": false, "frames": 1})
	return object{"version": float64(1), "origin": "real-agent", "reviewer": "test reviewer",
		"reviewedAt": "2026-10-31T00:00:00Z", "sourceReviewed": true, "contentReviewed": true, "credentialsReviewed": true,
		"officialSDK": sdk, "captureFiles": s.inventory(s.source), "replayFiles": s.inventory(s.replay),
		"publishFiles": corpusKeys(s.inventory(s.source))}
}
func (s *corpusSuite) qualify() object {
	r, err := s.c.qualification(s.source, s.replay, false)
	s.Require().NoError(err)
	return r
}
func (s *corpusSuite) adopt(review object) string {
	s.write(s.root, "review.json", review)
	target, err := s.c.adopt(s.source, s.replay, filepath.Join(s.root, "review.json"))
	s.Require().NoError(err)
	return target
}
func (s *corpusSuite) entry() object {
	index, err := s.c.loadIndex()
	s.Require().NoError(err)
	for _, value := range array(index["cases"]) {
		if e := obj(value); e["adoption"] == "admitted" {
			return e
		}
	}
	s.FailNow("no admitted entry")
	return nil
}
func (s *corpusSuite) TestFixedCoverageCannotShrink() {
	index, err := s.c.loadIndex()
	s.Require().NoError(err)
	counts := map[string]int{}
	for _, value := range array(index["cases"]) {
		counts[str(obj(value)["requirement"])]++
	}
	s.Equal(map[string]int{"required": 64, "optional": 8}, counts)
	for _, change := range []string{"empty", "omit", "downgrade", "duplicate", "verification", "reason", "unbacked-pass"} {
		s.Run(change, func() {
			bad := s.read(filepath.Dir(s.c.Index), "index.json")
			cases := array(bad["cases"])
			switch change {
			case "empty":
				cases = nil
			case "omit":
				cases = cases[:len(cases)-1]
			case "downgrade":
				obj(cases[0])["requirement"] = "planned"
			case "duplicate":
				cases = append(cases, cases[0])
			case "verification":
				obj(cases[0])["verification"] = "planned"
			case "reason":
				delete(obj(cases[0]), "verificationReason")
			case "unbacked-pass":
				obj(cases[0])["verification"] = "passed"
			}
			bad["cases"] = cases
			p := filepath.Join(s.root, change+".json")
			s.write(s.root, change+".json", bad)
			c := s.c
			c.Index = p
			_, err := c.loadIndex()
			s.Error(err)
		})
	}
	s.Len(array(index["cases"]), 72)
}
func (s *corpusSuite) TestInventoryRetainsPrivateOriginalsAndRejectsSymlinks() {
	s.write(s.source, "private.json", object{"retain": true})
	s.write(s.source, "plan.json", object{})
	s.Len(s.inventory(s.source), 2)
	s.Require().NoError(os.Symlink(filepath.Join(s.source, "plan.json"), filepath.Join(s.source, "linked")))
	_, err := corpusInventory(s.source, false)
	s.Error(err)
}

func (s *corpusSuite) TestRequirementsAreIndependentOfRoutes() {
	catalog, err := s.c.catalog()
	s.Require().NoError(err)
	for _, key := range []string{"codex/SDK-CASE-012"} {
		s.Equal("required", obj(catalog[key])["requirement"], key)
		s.NotEqual("live-selected-path", obj(catalog[key])["route"], key)
	}
	for _, id := range []string{"SDK-CASE-017", "SDK-CASE-032"} {
		s.Equal("optional", obj(catalog["claude/"+id])["requirement"])
		s.Equal("required", obj(catalog["codex/"+id])["requirement"])
	}
	for _, provider := range []string{"codex", "claude"} {
		for _, id := range []string{"SDK-CASE-003", "SDK-CASE-025", "SDK-CASE-026"} {
			s.Equal("optional", obj(catalog[provider+"/"+id])["requirement"])
		}
		s.Equal("required", obj(catalog[provider+"/SDK-CASE-027"])["requirement"], "optional thinking mode must not waive effort")
	}
	s.Equal("unsupported", obj(catalog["codex/SDK-CASE-025"])["route"], "effort cannot substitute for an independent thinking mode")
}

func (s *corpusSuite) TestProviderRequirementOverrideMustBeBoolean() {
	scenarios := filepath.Join(s.root, "scenarios")
	s.Require().NoError(copyTree(s.c.Scenarios, scenarios))
	bindings := s.read(scenarios, "bindings/claude.json")
	obj(obj(bindings["cases"])["SDK-CASE-017"])["required"] = "optional"
	s.write(scenarios, "bindings/claude.json", bindings)
	c := s.c
	c.Scenarios = scenarios
	_, err := c.catalog()
	s.ErrorContains(err, "provider requirement must be boolean")
}

func (s *corpusSuite) TestOptionalAdmissionAndIntegrity() {
	s.evidence()
	plan := s.read(s.source, "plan.json")
	plan["caseId"], plan["driverProfile"] = "SDK-CASE-003", "structured-input"
	s.write(s.source, "plan.json", plan)
	manifest := s.read(s.source, "manifest.json")
	manifest["planSHA256"] = s.hash(s.source, "plan.json")
	s.write(s.source, "manifest.json", manifest)
	for _, pair := range [][2]string{{s.source, "report.json"}, {s.replay, "replay-report.json"}} {
		report := s.read(pair[0], pair[1])
		report["caseId"] = "SDK-CASE-003"
		s.write(pair[0], pair[1], report)
		summary := s.read(pair[0], "summary.json")
		summary["case"] = "SDK-CASE-003"
		s.write(pair[0], "summary.json", summary)
	}
	s.bindReplay()
	s.adopt(s.qualify())
	entry := s.entry()
	s.Equal("optional", entry["requirement"])
	s.Equal("passed", entry["verification"])
	index := s.read(filepath.Dir(s.c.Index), "index.json")
	for _, value := range array(index["cases"]) {
		e := obj(value)
		if e["adoption"] == "admitted" {
			e["verification"], e["verificationReason"] = "failed", "synthetic known business gap"
		}
	}
	s.write(filepath.Dir(s.c.Index), "index.json", index)
	s.adopt(s.qualify())
	entry = s.entry()
	s.Equal("failed", entry["verification"], "re-adoption must not clear a diagnosis for identical frames")
	s.Equal("synthetic known business gap", entry["verificationReason"])
	target, err := s.c.verifyAdopted(entry)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(target, "raw/raw.jsonl"), []byte("tampered\n"), 0600))
	_, err = s.c.verifyAdopted(entry)
	s.Error(err, "optional does not waive source integrity")
}

func TestCorpusGateRequirementPolicy(t *testing.T) {
	passed := func() object {
		return object{"provider": "codex", "caseId": "SDK-CASE-001", "requirement": "required", "adoption": "admitted", "verification": "passed", "simulationStatus": "passed", "status": "passed"}
	}
	for _, state := range []string{"failed", "unverified", "blocked", "unsupported"} {
		t.Run(state, func(t *testing.T) {
			optional := object{"requirement": "optional", "status": state, "verification": state, "simulationStatus": "failed"}
			require.NoError(t, corpusGateResult([]object{passed(), optional}))
			required := passed()
			required["caseId"], required["verification"], required["status"] = "SDK-CASE-012", state, state
			require.ErrorContains(t, corpusGateResult([]object{passed(), required, optional}), "codex/SDK-CASE-012")
			// Even a passing replay cannot override failed or unknown business evidence.
			required["status"] = "passed"
			require.Error(t, corpusGateResult([]object{required}))
		})
	}
	for _, field := range []string{"adoption", "simulationStatus"} {
		row := passed()
		row[field] = "missing"
		require.Error(t, corpusGateResult([]object{row}))
	}
	require.ErrorContains(t, corpusGateResult(nil), "empty required corpus")
}
func (s *corpusSuite) TestReviewNeverApprovesCandidate() {
	s.evidence()
	path := filepath.Join(s.root, "candidate.json")
	err := executeCorpus(context.Background(), []string{"review", "--index", s.c.Index, "--scenarios", s.c.Scenarios,
		"--source", s.source, "--replay", s.replay, "--review", path, "--archive", s.c.Archive})
	s.Require().NoError(err)
	review := s.read(s.root, "candidate.json")
	s.Equal("unreviewed", review["origin"])
	s.Equal(false, review["sourceReviewed"])
	_, err = s.c.validateAdmission(s.source, s.replay, review)
	s.Error(err)
}
func (s *corpusSuite) TestOptionalHistoryPreservesFilesAndRejectsLinks() {
	s.evidence()
	name := "config/projects/project/session.jsonl"
	s.write(s.source, name, object{"cwd": "/original/project"})
	s.Require().NoError(os.MkdirAll(filepath.Join(s.source, "config/sessions/empty"), 0700))
	v := &corpusValidation{}
	s.Equal(true, v.required(s.source, s.read(s.source, "plan.json"))[name])
	s.Require().NoError(v.err)
	s.Require().NoError(os.Symlink(filepath.Join(s.source, name), filepath.Join(s.source, "config/sessions/linked")))
	v.required(s.source, s.read(s.source, "plan.json"))
	s.Error(v.err)
}
func (s *corpusSuite) TestPublishPathsCannotEscapeOrSelectCredentials() {
	for _, name := range []string{"../secret", "/absolute", "config/auth.json", "native.env.private.json", "config/settings.json", "stdout.log", "raw/../plan.json", "raw\\auth.json"} {
		s.False(corpusPublishable(name), name)
	}
	for _, name := range []string{"plan.json", "raw/raw.jsonl", "config/projects/key/session.jsonl", "history-seed/config/sessions/old.jsonl"} {
		s.True(corpusPublishable(name), name)
	}
}
func (s *corpusSuite) TestAdmissionRequiresBusinessAndFrozenReview() {
	review := s.evidence()
	_, err := s.c.validateAdmission(s.source, s.replay, review)
	s.Require().NoError(err)
	for field, value := range (object{"origin": "synthetic", "sourceReviewed": false, "credentialsReviewed": false,
		"captureFiles": object{}, "officialSDK": "", "reviewer": ""}) {
		s.Run(field, func() {
			old := review[field]
			review[field] = value
			_, err := s.c.validateAdmission(s.source, s.replay, review)
			s.Error(err)
			review[field] = old
		})
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"summary.json", "mode", "simulate"}, {"summary.json", "source", object{}}, {"result.json", "status", "untriggered"},
		{"replay-peer.json", "fullConsumption", false}, {"replay-peer.json", "clientEOF", false},
		{"replay-peer.json", "nativeLaunched", true}, {"replay-peer.json", "recordingSHA256", strings.Repeat("f", 64)},
	} {
		s.Run(tc.name+"/"+tc.field, func() {
			path := filepath.Join(s.replay, tc.name)
			before, err := os.ReadFile(path)
			s.Require().NoError(err)
			m := s.read(s.replay, tc.name)
			m[tc.field] = tc.value
			s.write(s.replay, tc.name, m)
			review["replayFiles"] = s.inventory(s.replay)
			_, err = s.c.validateAdmission(s.source, s.replay, review)
			s.Error(err)
			s.Require().NoError(os.WriteFile(path, before, 0600))
		})
	}
	s.Require().NoError(os.WriteFile(filepath.Join(s.source, "raw/raw.jsonl"), []byte("changed"), 0600))
	_, err = s.c.validateAdmission(s.source, s.replay, review)
	s.Error(err)
}
func (s *corpusSuite) TestAdoptArchivesCompleteOriginalAndSurvivesSourceRemoval() {
	review := s.evidence()
	s.write(s.source, "diagnostics.private.json", object{"retain": true})
	review["captureFiles"] = s.inventory(s.source)
	target := s.adopt(review)
	s.NoFileExists(filepath.Join(target, "diagnostics.private.json"))
	saved, err := filepath.Glob(filepath.Join(s.c.Archive, "*", "capture", "diagnostics.private.json"))
	s.Require().NoError(err)
	s.Len(saved, 1)
	s.Require().NoError(os.RemoveAll(s.source))
	s.Require().NoError(os.RemoveAll(s.replay))
	actual, err := s.c.verifyAdopted(s.entry())
	s.Require().NoError(err)
	s.Equal(target, actual)
	s.Equal("/original/project", s.read(target, "plan.json")["project"])
	s.Require().NoError(os.WriteFile(filepath.Join(target, "raw/raw.jsonl"), []byte("changed"), 0600))
	_, err = s.c.verifyAdopted(s.entry())
	s.Error(err)
}
func (s *corpusSuite) TestRejectedAdoptionStillArchivesOriginals() {
	review := s.evidence()
	review["origin"] = "synthetic"
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	s.write(s.root, "review.json", review)
	_, err := s.c.adopt(s.source, s.replay, filepath.Join(s.root, "review.json"))
	s.Require().Error(err)
	s.Equal(before, s.hash(filepath.Dir(s.c.Index), "index.json"))
	saved, err := filepath.Glob(filepath.Join(s.c.Archive, "*", "adoption-result.json"))
	s.Require().NoError(err)
	s.Require().Len(saved, 1)
	s.Equal("rejected", s.read(filepath.Dir(saved[0]), "adoption-result.json")["status"])
	s.Equal(s.inventory(s.source), s.inventory(filepath.Join(filepath.Dir(saved[0]), "capture")))
}
func (s *corpusSuite) TestQualificationDoesNotFabricateContentReview() {
	s.evidence()
	s.write(s.source, "config/logs_2.sqlite-wal", object{"private": true})
	review := s.qualify()
	s.Equal("official-sdk", obj(review["qualification"])["method"])
	s.NotContains(review, "reviewer")
	s.NotContains(review, "contentReviewed")
	s.Contains(obj(review["captureFiles"]), "config/logs_2.sqlite-wal")
	s.NotContains(stringsOf(review["publishFiles"]), "config/logs_2.sqlite-wal")
	s.write(s.replay, "result.json", object{"status": "failed"})
	_, err := s.c.qualification(s.source, s.replay, false)
	s.Error(err)
}
func (s *corpusSuite) TestAutomaticQualificationCannotExpandPublication() {
	s.evidence()
	s.write(s.source, "raw/extra-diagnostic.json", object{"private": true})
	review := s.qualify()
	review["publishFiles"] = append(stringsOf(review["publishFiles"]), "raw/extra-diagnostic.json")
	_, err := s.c.validateAdmission(s.source, s.replay, review)
	s.ErrorContains(err, "only required regression materials")
}
func (s *corpusSuite) TestAutomaticQualificationRejectsUnisolatedCapture() {
	s.evidence()
	plan := s.read(s.source, "plan.json")
	delete(plan, "recordingIsolation")
	s.write(s.source, "plan.json", plan)
	_, err := s.c.qualification(s.source, s.replay, false)
	s.ErrorContains(err, "recording-time isolated capture")
}
func (s *corpusSuite) TestQualificationCLIAndInvalidDestinationPreflight() {
	s.evidence()
	err := executeCorpus(context.Background(), []string{"qualify", "--index", s.c.Index, "--scenarios", s.c.Scenarios,
		"--source", s.source, "--replay", s.replay, "--review", filepath.Join(s.root, "proof.json"), "--archive", s.c.Archive})
	s.Require().NoError(err)
	s.Equal("official-sdk", obj(s.read(s.root, "proof.json")["qualification"])["method"])
	c := s.c
	c.Archive = filepath.Join(filepath.Dir(c.Index), "private")
	output := filepath.Join(s.root, "must-not-launch")
	_, err = c.recordAndAdopt(context.Background(), options{Output: output})
	s.ErrorContains(err, "outside regression corpus")
	s.NoDirExists(output)
}

func (s *corpusSuite) TestCompiledCorpusOutsideCheckoutWithoutCompilerOrPython() {
	s.evidence()
	scenarios, err := filepath.Abs(s.c.Scenarios)
	s.Require().NoError(err)
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	s.Require().NotEmpty(binary)
	binary, err = filepath.Abs(binary)
	s.Require().NoError(err)
	cmd := exec.Command(binary, "corpus", "qualify", "--index", s.c.Index, "--scenarios", scenarios,
		"--source", s.source, "--replay", s.replay, "--review", filepath.Join(s.root, "standalone.json"), "--archive", s.c.Archive)
	cmd.Dir = s.root
	cmd.Env = []string{"PATH=" + s.T().TempDir(), "HOME=" + s.root}
	output, err := cmd.CombinedOutput()
	s.Require().NoError(err, "%s", output)
	s.Equal("official-sdk", obj(s.read(s.root, "standalone.json")["qualification"])["method"])
}
func (s *corpusSuite) TestReplacementArchivesPreviousAndRetainsNewOnFailure() {
	s.evidence()
	first := s.adopt(s.qualify())
	old := s.inventory(first)
	s.write(s.source, "diagnostic.private.json", object{"second": true})
	review := s.qualify()
	second := s.adopt(review)
	s.NotEqual(first, second)
	s.NoDirExists(first)
	s.Equal(s.hash(s.source, "raw/raw.jsonl"), s.hash(second, "raw/raw.jsonl"))
	saved, err := filepath.Glob(filepath.Join(s.c.Archive, "*", "previous-regression"))
	s.Require().NoError(err)
	s.Require().Len(saved, 1)
	s.Equal(old, s.inventory(saved[0]))
	actual, err := s.c.verifyAdopted(s.entry())
	s.Require().NoError(err)
	s.Equal(second, actual)
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	review["origin"] = "synthetic"
	s.write(s.root, "bad.json", review)
	_, err = s.c.adopt(s.source, s.replay, filepath.Join(s.root, "bad.json"))
	s.Error(err)
	s.Equal(before, s.hash(filepath.Dir(s.c.Index), "index.json"))
	s.DirExists(second)
}
func (s *corpusSuite) TestIndexLockFailureRetainsExistingSample() {
	s.evidence()
	target := s.adopt(s.qualify())
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	lock := strings.TrimSuffix(s.c.Index, ".json") + ".lock"
	s.Require().NoError(os.WriteFile(lock, nil, 0600))
	s.write(s.root, "review.json", s.qualify())
	_, err := s.c.adopt(s.source, s.replay, filepath.Join(s.root, "review.json"))
	s.Error(err)
	s.Equal(before, s.hash(filepath.Dir(s.c.Index), "index.json"))
	children, err := os.ReadDir(filepath.Dir(target))
	s.Require().NoError(err)
	s.Len(children, 1)
	s.FileExists(lock)
}

func (s *corpusSuite) TestIndexSwitchFailurePreservesOldSampleAndRemovesStaging() {
	s.evidence()
	target := s.adopt(s.qualify())
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	s.write(s.root, "review.json", s.qualify())
	called := false
	_, err := s.c.adoptWithIndexReplacement(s.source, s.replay, filepath.Join(s.root, "review.json"), func(staged, index string) error {
		called = true
		s.FileExists(staged)
		s.Equal(s.c.Index, index)
		return errors.New("index write failed")
	})
	s.Require().ErrorContains(err, "index write failed")
	s.True(called)
	s.Equal(before, s.hash(filepath.Dir(s.c.Index), "index.json"))
	children, err := os.ReadDir(filepath.Dir(target))
	s.Require().NoError(err)
	s.Len(children, 1)
	s.NoFileExists(strings.TrimSuffix(s.c.Index, ".json") + ".lock")
	remaining, err := filepath.Glob(filepath.Join(filepath.Dir(s.c.Index), ".index-*"))
	s.Require().NoError(err)
	s.Empty(remaining)
}

func (s *corpusSuite) recordOptions(id string) options {
	o := sample(s.T(), id)
	plan := s.read(o.Materials, "plan.json")
	plan["storyId"] = "SDK-US-001"
	plan["corpusTestBusiness"] = true
	if id == "SDK-CASE-002" {
		plan["driverProfile"] = "structured-input"
	}
	s.write(o.Materials, "plan.json", plan)
	o.Mode, o.Config = "record", s.T().TempDir()
	o.CLI, _ = os.Executable()
	return o
}
func (s *corpusSuite) TestRecordAutomaticallyReplaysAndAdopts() {
	o := s.recordOptions("SDK-CASE-001")
	report, err := s.c.recordAndAdopt(context.Background(), o)
	s.Require().NoError(err)
	s.Equal(1, report["passed"])
	_, err = s.c.verifyAdopted(s.entry())
	s.NoError(err)
	s.Equal(float64(1), s.read(o.Output, "admission-summary.json")["passed"])
}
func (s *corpusSuite) TestFailedOfficialReplayRetainsPrevious() {
	s.evidence()
	target := s.adopt(s.qualify())
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	o := s.recordOptions("SDK-CASE-001")
	plan := s.read(o.Materials, "plan.json")
	plan["corpusTestReplayFail"] = true
	s.write(o.Materials, "plan.json", plan)
	report, err := s.c.recordAndAdopt(context.Background(), o)
	s.Error(err)
	s.Equal(0, report["passed"])
	s.Equal(before, s.hash(filepath.Dir(s.c.Index), "index.json"))
	s.DirExists(target)
}
func (s *corpusSuite) TestPartialBatchAdoptsSuccessAndReturnsFailure() {
	first, second := s.recordOptions("SDK-CASE-001"), s.recordOptions("SDK-CASE-002")
	plan := s.read(second.Materials, "plan.json")
	plan["corpusTestFail"] = true
	s.write(second.Materials, "plan.json", plan)
	selection := filepath.Join(s.root, "batch.json")
	s.write(s.root, "batch.json", []object{
		{"case": first.Materials, "driver": command{Command: first.Driver, Args: first.DriverArgs}},
		{"case": second.Materials, "driver": command{Command: second.Driver, Args: second.DriverArgs}},
	})
	first.CasesFile, first.Jobs, first.Output = selection, 2, filepath.Join(s.root, "batch")
	report, err := s.c.recordAndAdopt(context.Background(), first)
	s.Error(err)
	s.Equal(1, report["passed"])
	s.Equal(2, report["selected"])
	s.Equal("SDK-CASE-001", s.entry()["caseId"])
}
func (s *corpusSuite) TestSourceFailuresCannotBeReviewedIntoSuccess() {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"manifest.json", "provenance", "synthetic"}, {"result.json", "status", "untriggered"},
		{"raw/transport.json", "cleanupComplete", false}, {"report.json", "branchStatus", "untriggered"},
		{"report.json", "gaps", []string{"missing behavior"}},
	} {
		s.Run(tc.name+"/"+tc.field, func() {
			review := s.evidence()
			m := s.read(s.source, tc.name)
			m[tc.field] = tc.value
			s.write(s.source, tc.name, m)
			review["captureFiles"] = s.inventory(s.source)
			_, err := s.c.validateAdmission(s.source, s.replay, review)
			s.Error(err)
		})
	}
}
func (s *corpusSuite) TestClaudeMaterializationPinsSourceAndDerivedHashes() {
	s.evidence()
	plan := s.read(s.source, "plan.json")
	plan["provider"] = "claude"
	s.write(s.source, "plan.json", plan)
	name := "config/projects/-original-project/session.jsonl"
	s.write(s.source, name, object{"cwd": "/original/project", "id": "unchanged"})
	execution := "/gone-replay/project"
	target := "config/projects/" + claudeProjectKey(execution) + "/session.jsonl"
	s.write(s.replay, target, object{"cwd": execution, "id": "unchanged"})
	s.write(s.replay, "materialization.json", object{"version": 1, "originalProject": "/original/project",
		"originalHistoryProject": nil, "executionProject": execution, "transformations": []object{{
			"kind": "claude-project-cwd-copy", "source": name, "derived": target,
			"sourceSHA256": s.hash(s.source, name), "derivedSHA256": s.hash(s.replay, target),
			"sourceCWD": "/original/project", "executionCWD": execution}}})
	summary := s.read(s.replay, "summary.json")
	summary["provider"], summary["output"] = "claude", "/gone-replay"
	summary["materializationSHA256"] = s.hash(s.replay, "materialization.json")
	s.write(s.replay, "summary.json", summary)
	s.bindReplay()
	receipt := s.read(s.replay, "replay-peer.json")
	receipt["provider"] = "claude"
	s.write(s.replay, "replay-peer.json", receipt)
	v := &corpusValidation{}
	v.replay(s.source, s.replay, plan, "replay")
	s.Require().NoError(v.err)
	s.write(s.replay, target, object{"cwd": execution, "id": "changed"})
	v.replay(s.source, s.replay, plan, "replay")
	s.Error(v.err)
}

func TestCorpusStrictJSONAndSourceProvenance(t *testing.T) {
	for _, data := range []string{`{"a":1,"a":2}`, `null`, `[]`, `{"a":1} {}`} {
		p := filepath.Join(t.TempDir(), "invalid.json")
		require.NoError(t, os.WriteFile(p, []byte(data), 0600))
		_, err := corpusRead(p)
		require.Error(t, err, data)
	}
	source, err := corpusSource(".")
	require.NoError(t, err)
	require.Len(t, str(source["sourceRevision"]), 40)
	require.Contains(t, obj(source["sourceFiles"]), "corpus.go")
	require.Contains(t, obj(source["sourceFiles"]), "go.mod")
	require.NotContains(t, obj(source["sourceFiles"]), ".artifacts")
}
