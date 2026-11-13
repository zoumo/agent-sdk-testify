package corpus

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// All evidence fabricated by this suite stays in temporary directories.
type corpusSuite struct {
	suite.Suite
	root, source, replay string
	c                    Corpus
}

func TestCorpus(t *testing.T) { suite.Run(t, new(corpusSuite)) }

func (s *corpusSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.source, s.replay = filepath.Join(s.root, "capture"), filepath.Join(s.root, "replay")
	repository, err := testutil.RepositoryRoot()
	s.Require().NoError(err)
	s.c = Corpus{
		Index:     filepath.Join(s.root, "corpus", "index.json"),
		Scenarios: filepath.Join(repository, "data", "scenarios"),
		Archive:   filepath.Join(s.root, "archive"),
	}
	index := s.read(repository, "data/index.json")
	for _, value := range tool.Array(index["cases"]) {
		e := tool.Obj(value)
		for _, key := range []string{"materials", "adoptionSHA256", "materialSHA256", "officialSDK", "qualification"} {
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
	s.Require().NoError(os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700))
	s.Require().NoError(tool.WriteJSON(filepath.Join(root, name), value))
}

func (s *corpusSuite) read(root, name string) tool.Object {
	s.T().Helper()
	v, err := Read(filepath.Join(root, name))
	s.Require().NoError(err)
	return v
}

func (s *corpusSuite) inventory(root string) tool.Object {
	s.T().Helper()
	v, err := Inventory(root, false)
	s.Require().NoError(err)
	return v
}

func (s *corpusSuite) hash(root, name string) string {
	s.T().Helper()
	v, err := Digest(filepath.Join(root, name))
	s.Require().NoError(err)
	return v
}

func (s *corpusSuite) bindReplay() {
	summary := s.read(s.replay, "summary.json")
	files := tool.Object{}
	for _, name := range SourceEvidence {
		files[name] = s.hash(s.source, name)
	}
	summary["source"] = files
	s.write(s.replay, "summary.json", summary)
}

func (s *corpusSuite) evidence() tool.Object {
	plan := tool.Object{
		"provider": "codex", "caseId": "SDK-CASE-001", "storyId": "SDK-US-001",
		"driverProfile": "continuation", "project": "/original/project", "recordingIsolation": "personal-skills-v1",
		"scenario": tool.Object{"files": tool.Object{"numbers.txt": "7\n11\n"}},
	}
	s.write(s.source, "plan.json", plan)
	s.write(s.source, "raw/raw.jsonl", tool.Object{
		"ordinal": 1, "direction": "c2s", "channel": "protocol",
		"epoch": 1, "elapsedMs": 0, "wire": tool.Object{"id": 1, "method": "ping"},
	})
	s.Require().NoError(os.WriteFile(filepath.Join(s.source, "raw", "raw.jsonl"), []byte("{\"ordinal\":1,\"direction\":\"c2s\",\"channel\":\"protocol\",\"epoch\":1,\"elapsedMs\":0,\"wire\":{\"id\":1,\"method\":\"ping\"}}\n"), 0o600))
	s.Require().NoError(os.WriteFile(filepath.Join(s.source, "raw", "stderr.raw"), nil, 0o600))
	s.write(s.source, "raw/transport.json", tool.Object{
		"captureStatus": "complete", "cleanupComplete": true,
		"nativeLaunched": true, "reaped": true, "clientEOF": true, "frames": 1,
	})
	s.write(s.source, "raw/manifest.json", tool.Object{
		"captureStatus": "complete", "files": s.inventory(filepath.Join(s.source, "raw")),
		"native":          tool.Object{"executable": "/observed/native", "sha256": strings.Repeat("a", 64), "cwd": "/original/project"},
		"recordingFormat": tool.Object{"name": "agent-mock-jsonl", "version": 1, "transport": "stdio-jsonl"},
	})
	sdk := tool.Object{"package": "openai-codex", "version": "synthetic-test-only"}
	business := tool.Object{
		"status": "passed", "branchStatus": "passed", "captureStatus": "complete",
		"provider": "codex", "caseId": "SDK-CASE-001", "storyId": "SDK-US-001", "officialSDK": sdk,
	}
	s.write(s.source, "report.json", business)
	s.write(s.replay, "replay-report.json", business)
	s.write(s.source, "result.json", tool.Object{"status": "passed"})
	s.write(s.replay, "result.json", tool.Object{"status": "passed"})
	s.write(s.source, "manifest.json", tool.Object{
		"provenance": "capture", "status": "passed", "captureStatus": "complete",
		"credentialScan": "pass", "planSHA256": s.hash(s.source, "plan.json"), "files": s.inventory(filepath.Join(s.source, "raw")),
	})
	hash := s.hash(s.source, "raw/raw.jsonl")
	s.write(s.source, "summary.json", tool.Object{
		"provider": "codex", "case": "SDK-CASE-001", "mode": "record",
		"result": tool.Object{"status": "passed"}, "recordingSHA256": hash,
	})
	s.write(s.replay, "summary.json", tool.Object{
		"provider": "codex", "case": "SDK-CASE-001", "mode": "replay",
		"result": tool.Object{"status": "passed"}, "recordingSHA256": hash,
	})
	s.bindReplay()
	s.write(s.replay, "replay-peer.json", tool.Object{
		"provider": "codex", "recordingSHA256": hash,
		"fullConsumption": true, "clientEOF": true, "nativeLaunched": false, "frames": 1,
	})
	return tool.Object{
		"version": float64(1), "origin": "real-agent", "reviewer": "test reviewer",
		"reviewedAt": "2026-10-31T00:00:00Z", "sourceReviewed": true, "contentReviewed": true, "credentialsReviewed": true,
		"officialSDK": sdk, "captureFiles": s.inventory(s.source), "replayFiles": s.inventory(s.replay),
		"publishFiles": Keys(s.inventory(s.source)),
	}
}

func (s *corpusSuite) qualify() tool.Object {
	r, err := s.c.Qualification(s.source, s.replay, false)
	s.Require().NoError(err)
	return r
}

func (s *corpusSuite) adopt(review tool.Object) string {
	s.write(s.root, "review.json", review)
	target, err := s.c.Adopt(s.source, s.replay, filepath.Join(s.root, "review.json"))
	s.Require().NoError(err)
	return target
}

func (s *corpusSuite) entry() tool.Object {
	index, err := s.c.LoadIndex()
	s.Require().NoError(err)
	for _, value := range tool.Array(index["cases"]) {
		if e := tool.Obj(value); e["adoption"] == "admitted" {
			return e
		}
	}
	s.FailNow("no admitted entry")
	return nil
}

func (s *corpusSuite) TestFixedCoverageCannotShrink() {
	index, err := s.c.LoadIndex()
	s.Require().NoError(err)
	counts := map[string]int{}
	for _, value := range tool.Array(index["cases"]) {
		counts[tool.Str(tool.Obj(value)["requirement"])]++
	}
	s.Equal(map[string]int{"required": 65, "optional": 11}, counts)
	for _, change := range []string{"empty", "omit", "downgrade", "duplicate", "verification", "reason", "unbacked-pass"} {
		s.Run(change, func() {
			bad := s.read(filepath.Dir(s.c.Index), "index.json")
			cases := tool.Array(bad["cases"])
			switch change {
			case "empty":
				cases = nil
			case "omit":
				cases = cases[:len(cases)-1]
			case "downgrade":
				tool.Obj(cases[0])["requirement"] = "planned"
			case "duplicate":
				cases = append(cases, cases[0])
			case "verification":
				tool.Obj(cases[0])["verification"] = "planned"
			case "reason":
				delete(tool.Obj(cases[0]), "verificationReason")
			case "unbacked-pass":
				tool.Obj(cases[0])["verification"] = "passed"
			}
			bad["cases"] = cases
			p := filepath.Join(s.root, change+".json")
			s.write(s.root, change+".json", bad)
			c := s.c
			c.Index = p
			_, err := c.LoadIndex()
			s.Require().Error(err)
		})
	}
	s.Len(tool.Array(index["cases"]), 76)
}

func (s *corpusSuite) TestInventoryRetainsPrivateOriginalsAndRejectsSymlinks() {
	s.write(s.source, "private.json", tool.Object{"retain": true})
	s.write(s.source, "plan.json", tool.Object{})
	s.Len(s.inventory(s.source), 2)
	s.Require().NoError(os.Symlink(filepath.Join(s.source, "plan.json"), filepath.Join(s.source, "linked")))
	_, err := Inventory(s.source, false)
	s.Require().Error(err)
}

func (s *corpusSuite) TestRequirementsAreIndependentOfRoutes() {
	catalog, err := s.c.Catalog()
	s.Require().NoError(err)
	for _, key := range []string{"codex/SDK-CASE-012"} {
		s.Equal("optional", tool.Obj(catalog[key])["requirement"], key)
		s.NotEqual("live-selected-path", tool.Obj(catalog[key])["route"], key)
	}
	for _, id := range []string{"SDK-CASE-017", "SDK-CASE-032"} {
		s.Equal("optional", tool.Obj(catalog["claude/"+id])["requirement"])
		s.Equal("required", tool.Obj(catalog["codex/"+id])["requirement"])
	}
	for _, provider := range []string{"codex", "claude"} {
		for _, id := range []string{"SDK-CASE-003", "SDK-CASE-025", "SDK-CASE-026"} {
			s.Equal("optional", tool.Obj(catalog[provider+"/"+id])["requirement"])
		}
		s.Equal("required", tool.Obj(catalog[provider+"/SDK-CASE-027"])["requirement"], "optional thinking mode must not waive effort")
	}
	s.Equal("unsupported", tool.Obj(catalog["codex/SDK-CASE-025"])["route"], "effort cannot substitute for an independent thinking mode")
}

func (s *corpusSuite) TestProviderRequirementOverrideMustBeBoolean() {
	scenarios := filepath.Join(s.root, "scenarios")
	s.Require().NoError(tool.CopyTree(s.c.Scenarios, scenarios))
	bindings := s.read(scenarios, "bindings/claude.json")
	tool.Obj(tool.Obj(bindings["cases"])["SDK-CASE-017"])["required"] = "optional"
	s.write(scenarios, "bindings/claude.json", bindings)
	c := s.c
	c.Scenarios = scenarios
	_, err := c.Catalog()
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
	for _, value := range tool.Array(index["cases"]) {
		e := tool.Obj(value)
		if e["adoption"] == "admitted" {
			e["verification"], e["verificationReason"] = "failed", "synthetic known business gap"
		}
	}
	s.write(filepath.Dir(s.c.Index), "index.json", index)
	s.adopt(s.qualify())
	entry = s.entry()
	s.Equal("failed", entry["verification"], "re-adoption must not clear a diagnosis for identical frames")
	s.Equal("synthetic known business gap", entry["verificationReason"])
	target, err := s.c.VerifyAdopted(entry)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(filepath.Join(target, "raw", "raw.jsonl"), []byte("tampered\n"), 0o600))
	_, err = s.c.VerifyAdopted(entry)
	s.Require().Error(err, "optional does not waive source integrity")
}

func (s *corpusSuite) TestReviewNeverApprovesCandidate() {
	s.evidence()
	review, err := s.c.Qualification(s.source, s.replay, true)
	s.Require().NoError(err)
	s.Equal("unreviewed", review["origin"])
	s.Equal(false, review["sourceReviewed"])
	_, err = s.c.ValidateAdmission(s.source, s.replay, review)
	s.Require().Error(err)
}

func (s *corpusSuite) TestOptionalHistoryPreservesFilesAndRejectsLinks() {
	s.evidence()
	name := "config/projects/project/session.jsonl"
	s.write(s.source, name, tool.Object{"cwd": "/original/project"})
	s.Require().NoError(os.MkdirAll(filepath.Join(s.source, "config", "sessions", "empty"), 0o700))
	v := &Validation{}
	s.Equal(true, v.Required(s.source, s.read(s.source, "plan.json"))[name])
	s.Require().NoError(v.Err)
	s.Require().NoError(os.Symlink(filepath.Join(s.source, name), filepath.Join(s.source, "config", "sessions", "linked")))
	v.Required(s.source, s.read(s.source, "plan.json"))
	s.Require().Error(v.Err)
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
	_, err := s.c.ValidateAdmission(s.source, s.replay, review)
	s.Require().NoError(err)
	for field, value := range (tool.Object{
		"origin": "synthetic", "sourceReviewed": false, "credentialsReviewed": false,
		"captureFiles": tool.Object{}, "officialSDK": "", "reviewer": "",
	}) {
		s.Run(field, func() {
			old := review[field]
			review[field] = value
			_, err := s.c.ValidateAdmission(s.source, s.replay, review)
			s.Require().Error(err)
			review[field] = old
		})
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"summary.json", "mode", "simulate"},
		{"summary.json", "source", tool.Object{}},
		{"result.json", "status", "untriggered"},
		{"replay-peer.json", "fullConsumption", false},
		{"replay-peer.json", "clientEOF", false},
		{"replay-peer.json", "nativeLaunched", true},
		{"replay-peer.json", "recordingSHA256", strings.Repeat("f", 64)},
	} {
		s.Run(tc.name+"/"+tc.field, func() {
			path := filepath.Join(s.replay, tc.name)
			before, err := os.ReadFile(path)
			s.Require().NoError(err)
			m := s.read(s.replay, tc.name)
			m[tc.field] = tc.value
			s.write(s.replay, tc.name, m)
			review["replayFiles"] = s.inventory(s.replay)
			_, err = s.c.ValidateAdmission(s.source, s.replay, review)
			s.Require().Error(err)
			s.Require().NoError(os.WriteFile(path, before, 0o600))
		})
	}
	s.Require().NoError(os.WriteFile(filepath.Join(s.source, "raw", "raw.jsonl"), []byte("changed"), 0o600))
	_, err = s.c.ValidateAdmission(s.source, s.replay, review)
	s.Require().Error(err)
}

func (s *corpusSuite) TestAdoptArchivesCompleteOriginalAndSurvivesSourceRemoval() {
	review := s.evidence()
	s.write(s.source, "diagnostics.private.json", tool.Object{"retain": true})
	review["captureFiles"] = s.inventory(s.source)
	target := s.adopt(review)
	s.NoFileExists(filepath.Join(target, "diagnostics.private.json"))
	saved, err := filepath.Glob(filepath.Join(s.c.Archive, "*", "capture", "diagnostics.private.json"))
	s.Require().NoError(err)
	s.Len(saved, 1)
	s.Require().NoError(os.RemoveAll(s.source))
	s.Require().NoError(os.RemoveAll(s.replay))
	actual, err := s.c.VerifyAdopted(s.entry())
	s.Require().NoError(err)
	s.Equal(target, actual)
	s.Equal("/original/project", s.read(target, "plan.json")["project"])
	s.Require().NoError(os.WriteFile(filepath.Join(target, "raw", "raw.jsonl"), []byte("changed"), 0o600))
	_, err = s.c.VerifyAdopted(s.entry())
	s.Require().Error(err)
}

func (s *corpusSuite) TestRejectedAdoptionStillArchivesOriginals() {
	review := s.evidence()
	review["origin"] = "synthetic"
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	s.write(s.root, "review.json", review)
	_, err := s.c.Adopt(s.source, s.replay, filepath.Join(s.root, "review.json"))
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
	s.write(s.source, "config/logs_2.sqlite-wal", tool.Object{"private": true})
	review := s.qualify()
	s.Equal("official-sdk", tool.Obj(review["qualification"])["method"])
	s.NotContains(review, "reviewer")
	s.NotContains(review, "contentReviewed")
	s.Contains(tool.Obj(review["captureFiles"]), "config/logs_2.sqlite-wal")
	s.NotContains(tool.Strings(review["publishFiles"]), "config/logs_2.sqlite-wal")
	s.write(s.replay, "result.json", tool.Object{"status": "failed"})
	_, err := s.c.Qualification(s.source, s.replay, false)
	s.Require().Error(err)
}

func (s *corpusSuite) TestAutomaticQualificationCannotExpandPublication() {
	s.evidence()
	s.write(s.source, "raw/extra-diagnostic.json", tool.Object{"private": true})
	review := s.qualify()
	review["publishFiles"] = append(tool.Strings(review["publishFiles"]), "raw/extra-diagnostic.json")
	_, err := s.c.ValidateAdmission(s.source, s.replay, review)
	s.ErrorContains(err, "only required regression materials")
}

func (s *corpusSuite) TestAutomaticQualificationRejectsUnisolatedCapture() {
	s.evidence()
	plan := s.read(s.source, "plan.json")
	delete(plan, "recordingIsolation")
	s.write(s.source, "plan.json", plan)
	_, err := s.c.Qualification(s.source, s.replay, false)
	s.ErrorContains(err, "recording-time isolated capture")
}

func (s *corpusSuite) TestQualificationCLIAndInvalidDestinationPreflight() {
	s.evidence()
	review, err := s.c.Qualification(s.source, s.replay, false)
	s.Require().NoError(err)
	s.Equal("official-sdk", tool.Obj(review["qualification"])["method"])
	c := s.c
	c.Archive = filepath.Join(filepath.Dir(c.Index), "private")
	output := filepath.Join(s.root, "must-not-launch")
	_, err = c.archiveRoot()
	s.Require().ErrorContains(err, "outside regression corpus")
	s.NoDirExists(output)
}

func (s *corpusSuite) TestReplacementArchivesPreviousAndRetainsNewOnFailure() {
	s.evidence()
	first := s.adopt(s.qualify())
	old := s.inventory(first)
	s.write(s.source, "diagnostic.private.json", tool.Object{"second": true})
	review := s.qualify()
	second := s.adopt(review)
	s.NotEqual(first, second)
	s.NoDirExists(first)
	s.Equal(s.hash(s.source, "raw/raw.jsonl"), s.hash(second, "raw/raw.jsonl"))
	saved, err := filepath.Glob(filepath.Join(s.c.Archive, "*", "previous-regression"))
	s.Require().NoError(err)
	s.Require().Len(saved, 1)
	s.Equal(old, s.inventory(saved[0]))
	actual, err := s.c.VerifyAdopted(s.entry())
	s.Require().NoError(err)
	s.Equal(second, actual)
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	review["origin"] = "synthetic"
	s.write(s.root, "bad.json", review)
	_, err = s.c.Adopt(s.source, s.replay, filepath.Join(s.root, "bad.json"))
	s.Require().Error(err)
	s.Equal(before, s.hash(filepath.Dir(s.c.Index), "index.json"))
	s.DirExists(second)
}

func (s *corpusSuite) TestLegacyReplacementClearsPublishedMaterialReceipt() {
	s.evidence()
	repository, err := testutil.RepositoryRoot()
	s.Require().NoError(err)
	index := s.read(filepath.Dir(s.c.Index), "index.json")
	for _, value := range tool.Array(s.read(repository, "data/index.json")["cases"]) {
		published := tool.Obj(value)
		if published["provider"] != "codex" || published["caseId"] != "SDK-CASE-001" {
			continue
		}
		for _, row := range tool.Array(index["cases"]) {
			entry := tool.Obj(row)
			if entry["provider"] == published["provider"] && entry["caseId"] == published["caseId"] {
				maps.Copy(entry, published)
				source := filepath.Join(repository, "data", tool.Str(published["materials"]))
				target := filepath.Join(filepath.Dir(s.c.Index), tool.Str(published["materials"]))
				s.Require().NoError(tool.CopyTree(source, target))
			}
		}
	}
	s.write(filepath.Dir(s.c.Index), "index.json", index)
	s.Require().NotEmpty(s.entry()["materialSHA256"])
	_, err = s.c.VerifyAdopted(s.entry())
	s.Require().NoError(err)

	target := s.adopt(s.qualify())
	for _, key := range []string{"materialSHA256", "officialSDK", "qualification"} {
		s.NotContains(s.entry(), key)
	}
	actual, err := s.c.VerifyAdopted(s.entry())
	s.Require().NoError(err)
	s.Equal(target, actual)
}

func (s *corpusSuite) TestPublishedLegacyMaterialPreservesStoryAlias() {
	s.evidence()
	plan := s.read(s.source, "plan.json")
	plan["story"] = plan["storyId"]
	delete(plan, "storyId")
	s.write(s.source, "plan.json", plan)
	manifest := s.read(s.source, "manifest.json")
	manifest["planSHA256"] = s.hash(s.source, "plan.json")
	s.write(s.source, "manifest.json", manifest)
	s.write(s.source, "material.json", tool.Object{
		"formatVersion": 2, "id": "synthetic-legacy", "kind": "capture", "action": "record", "provenance": "official",
		"planSHA256": s.hash(s.source, "plan.json"), "manifestSHA256": s.hash(s.source, "manifest.json"),
		"recordingSHA256": s.hash(s.source, "raw/raw.jsonl"), "connections": []string{"main"}, "files": s.inventory(s.source),
	})
	entry := tool.Object{
		"provider": "codex", "caseId": "SDK-CASE-001", "storyId": "SDK-US-001", "requirement": "required",
		"adoption": "admitted", "verification": "passed", "adoptionSHA256": strings.Repeat("a", 64),
		"materialSHA256": s.hash(s.source, "material.json"),
		"officialSDK":    tool.Object{"package": "openai-codex", "version": "synthetic-legacy"},
		"qualification":  tool.Object{"method": "official-sdk", "qualifiedAt": "2026-11-13T00:00:00Z"},
	}
	s.Require().NoError(s.c.verifyPublishedMaterial(s.source, entry))
	entry["storyId"] = "SDK-US-002"
	s.Require().ErrorContains(s.c.verifyPublishedMaterial(s.source, entry), "identity differs")
}

func (s *corpusSuite) TestPublishedMaterialRejectsLinkedOwnerDirectory() {
	repository, err := testutil.RepositoryRoot()
	s.Require().NoError(err)
	for _, value := range tool.Array(s.read(repository, "data/index.json")["cases"]) {
		entry := tool.Obj(value)
		if entry["provider"] != "codex" || entry["caseId"] != "SDK-CASE-001" {
			continue
		}
		link := filepath.Join(filepath.Dir(s.c.Index), "codex")
		s.Require().NoError(os.Symlink(filepath.Join(repository, "data", "codex"), link))
		_, err := s.c.VerifyAdopted(entry)
		s.Require().ErrorContains(err, "material symlink is forbidden")
		return
	}
	s.FailNow("missing published test material")
}

func (s *corpusSuite) TestIndexLockFailureRetainsExistingSample() {
	s.evidence()
	target := s.adopt(s.qualify())
	before := s.hash(filepath.Dir(s.c.Index), "index.json")
	lock := strings.TrimSuffix(s.c.Index, ".json") + ".lock"
	s.Require().NoError(os.WriteFile(lock, nil, 0o600))
	s.write(s.root, "review.json", s.qualify())
	_, err := s.c.Adopt(s.source, s.replay, filepath.Join(s.root, "review.json"))
	s.Require().Error(err)
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
	_, err := s.c.AdoptWithIndexReplacement(s.source, s.replay, filepath.Join(s.root, "review.json"), func(staged, index string) error {
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

func (s *corpusSuite) TestSourceFailuresCannotBeReviewedIntoSuccess() {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"manifest.json", "provenance", "synthetic"},
		{"result.json", "status", "untriggered"},
		{"raw/transport.json", "cleanupComplete", false},
		{"report.json", "branchStatus", "untriggered"},
		{"report.json", "gaps", []string{"missing behavior"}},
	} {
		s.Run(tc.name+"/"+tc.field, func() {
			review := s.evidence()
			m := s.read(s.source, tc.name)
			m[tc.field] = tc.value
			s.write(s.source, tc.name, m)
			review["captureFiles"] = s.inventory(s.source)
			_, err := s.c.ValidateAdmission(s.source, s.replay, review)
			s.Require().Error(err)
		})
	}
}

func (s *corpusSuite) TestClaudeMaterializationPinsSourceAndDerivedHashes() {
	s.evidence()
	plan := s.read(s.source, "plan.json")
	plan["provider"] = "claude"
	s.write(s.source, "plan.json", plan)
	name := "config/projects/-original-project/session.jsonl"
	s.write(s.source, name, tool.Object{"cwd": "/original/project", "id": "unchanged"})
	execution := "/gone-replay/project"
	target := "config/projects/" + claudeProjectKey(execution) + "/session.jsonl"
	s.write(s.replay, target, tool.Object{"cwd": execution, "id": "unchanged"})
	s.write(s.replay, "materialization.json", tool.Object{
		"version": 1, "originalProject": "/original/project",
		"originalHistoryProject": nil, "executionProject": execution, "transformations": []tool.Object{{
			"kind": "claude-project-cwd-copy", "source": name, "derived": target,
			"sourceSHA256": s.hash(s.source, name), "derivedSHA256": s.hash(s.replay, target),
			"sourceCWD": "/original/project", "executionCWD": execution,
		}},
	})
	summary := s.read(s.replay, "summary.json")
	summary["provider"], summary["output"] = "claude", "/gone-replay"
	summary["materializationSHA256"] = s.hash(s.replay, "materialization.json")
	s.write(s.replay, "summary.json", summary)
	s.bindReplay()
	receipt := s.read(s.replay, "replay-peer.json")
	receipt["provider"] = "claude"
	s.write(s.replay, "replay-peer.json", receipt)
	v := &Validation{}
	v.Replay(s.source, s.replay, plan, "replay")
	s.Require().NoError(v.Err)
	s.write(s.replay, target, tool.Object{"cwd": execution, "id": "changed"})
	v.Replay(s.source, s.replay, plan, "replay")
	s.Require().Error(v.Err)
}

func TestCorpusStrictJSONAndSourceProvenance(t *testing.T) {
	for _, data := range []string{`{"a":1,"a":2}`, `null`, `[]`, `{"a":1} {}`} {
		p := filepath.Join(t.TempDir(), "invalid.json")
		require.NoError(t, os.WriteFile(p, []byte(data), 0o600))
		_, err := Read(p)
		require.Error(t, err, data)
	}
}
