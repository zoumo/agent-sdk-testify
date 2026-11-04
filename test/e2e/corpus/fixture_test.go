package corpus

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// All evidence fabricated by this suite stays in temporary directories.
type corpusSuite struct {
	suite.Suite
	root, source, replay string
	c                    corpus.Corpus
}

func (s *corpusSuite) SetupTest() {
	s.root = s.T().TempDir()
	s.source, s.replay = filepath.Join(s.root, "capture"), filepath.Join(s.root, "replay")
	repository, err := testutil.RepositoryRoot()
	s.Require().NoError(err)
	s.c = corpus.Corpus{
		Index:     filepath.Join(s.root, "corpus", "index.json"),
		Scenarios: filepath.Join(repository, "data", "scenarios"),
		Archive:   filepath.Join(s.root, "archive"),
	}
	index := s.read(repository, "data/index.json")
	for _, value := range tool.Array(index["cases"]) {
		e := tool.Obj(value)
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
	s.Require().NoError(os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700))
	s.Require().NoError(tool.WriteJSON(filepath.Join(root, name), value))
}

func (s *corpusSuite) read(root, name string) tool.Object {
	s.T().Helper()
	v, err := corpus.Read(filepath.Join(root, name))
	s.Require().NoError(err)
	return v
}

func (s *corpusSuite) inventory(root string) tool.Object {
	s.T().Helper()
	v, err := corpus.Inventory(root, false)
	s.Require().NoError(err)
	return v
}

func (s *corpusSuite) hash(root, name string) string {
	s.T().Helper()
	v, err := corpus.Digest(filepath.Join(root, name))
	s.Require().NoError(err)
	return v
}

func (s *corpusSuite) bindReplay() {
	summary := s.read(s.replay, "summary.json")
	files := tool.Object{}
	for _, name := range corpus.SourceEvidence {
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
		"publishFiles": corpus.Keys(s.inventory(s.source)),
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
