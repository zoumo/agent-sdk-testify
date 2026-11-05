package native

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const isolatedRecording = "personal-skills-v1"

// A completed capture supplies native saved records. Preparing a later case
// does not execute an SDK, start a session, or require the replay stage first.
func PrepareHistorySeed(source, root string, plan tool.Object) error {
	var err error
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if err := corpus.VerifyMaterials(source, true); err != nil {
		return err
	}
	var previous, report, manifest tool.Object
	for name, target := range map[string]*tool.Object{"plan.json": &previous, "report.json": &report, "manifest.json": &manifest} {
		if err := tool.ReadJSON(filepath.Join(source, name), target); err != nil {
			return err
		}
	}
	// This startup assertion is emitted only by new recording setup and bound
	// by the capture's existing plan hash. It is not a content-safety verdict.
	if previous["recordingIsolation"] != isolatedRecording || manifest["provenance"] != "capture" || tool.Str(manifest["planSHA256"]) == "" || manifest["captureStatus"] != "complete" || (manifest["status"] != "passed" && manifest["status"] != "captured") {
		return errors.New("seed must come from an isolated recording")
	}
	if report["status"] == "captured" {
		if _, err := corpus.VerifyAdoptedFiles(source); err != nil {
			return err
		}
		if err := tool.ReadJSON(filepath.Join(source, "official-replay", "driver", "record-origin", "run", "driver", "report.json"), &report); err != nil {
			return err
		}
	}
	provider := tool.Str(plan["provider"])
	if previous["provider"] != provider || previous["driverProfile"] != "history" || tool.Obj(tool.Obj(previous["scenario"])["parameters"])["historyTarget"] != "child-messages" || report["status"] != "passed" {
		return errors.New("seed must be a completed, passed child-history capture for this Agent")
	}
	var session string
	var agents map[string]bool
	var ids []string
	if provider == "claude" {
		session, agents, ids, err = claudeHistorySeedReceipts(report)
	} else {
		session, agents, ids, err = codexHistorySeedReceipts(report)
	}
	if err != nil {
		return err
	}
	if session == "" || len(agents) == 0 || agents[""] {
		return errors.New("seed has no saved parent and child identities")
	}
	files := tool.Object{}
	found := map[string]bool{}
	for name, digest := range tool.Obj(manifest["savedFiles"]) {
		if !filepath.IsLocal(name) {
			return errors.New("seed file escapes source")
		}
		data, e := os.ReadFile(filepath.Join(source, name))
		if e != nil {
			return e
		}
		var identity string
		var matched bool
		if provider == "codex" {
			identity, matched, e = codexHistorySeedIdentity(name, data)
		} else {
			identity, matched = claudeHistorySeedIdentity(name, session)
		}
		if e != nil {
			return e
		}
		if !matched {
			continue
		}
		if identity != session && !agents[identity] {
			continue
		}
		if found[identity] {
			return errors.New("duplicate saved seed identity")
		}
		sum := sha256.Sum256(data)
		actual := hex.EncodeToString(sum[:])
		if actual != tool.Str(digest) {
			return errors.New("seed content changed")
		}
		if err := tool.CopyFile(filepath.Join(source, name), filepath.Join(root, name)); err != nil {
			return err
		}
		destination := filepath.Join("history-seed", name)
		if err := tool.CopyFile(filepath.Join(source, name), filepath.Join(root, destination)); err != nil {
			return err
		}
		files[destination] = tool.Object{"source": name, "sha256": actual}
		found[identity] = true
	}
	if !found[session] || len(found) != len(agents)+1 {
		return errors.New("seed manifest lacks saved parent or child records")
	}
	project := tool.Str(previous["project"])
	if provider == "claude" && tool.Str(previous["_fixtureProject"]) != "" {
		project = tool.Str(previous["_fixtureProject"])
	}
	if !filepath.IsAbs(project) {
		return errors.New("seed project identity must be absolute")
	}
	if provider == "codex" && project == filepath.Join(source, "project") {
		if err := prepareCodexIsolation(root, tool.Object{"project": project}); err != nil {
			return err
		}
	}
	snapshot := filepath.Join(root, "history-seed", "project")
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		return err
	}
	if err := tool.WriteFiles(snapshot, tool.Obj(tool.Obj(previous["scenario"])["files"])); err != nil {
		return err
	}
	projectFiles := tool.Object{}
	if err := filepath.WalkDir(snapshot, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == snapshot {
			return nil
		}
		name, err := filepath.Rel(snapshot, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			projectFiles[name] = "directory"
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported seed project entry: %s", name)
		}
		digest, err := tool.FileDigest(p)
		projectFiles[name] = digest
		return err
	}); err != nil {
		return err
	}

	sourceFiles := tool.Object{}
	for _, name := range []string{"plan.json", "manifest.json", "report.json", "raw/raw.jsonl"} {
		digest, e := tool.FileDigest(filepath.Join(source, name))
		if e != nil {
			return e
		}
		sourceFiles[name] = digest
	}
	plan["historySeed"] = tool.Object{"provider": provider, "sourceRoot": source, "sourceProject": project, "sessionId": session, "agentIds": ids, "files": files, "sourceFiles": sourceFiles, "projectFiles": projectFiles, "projectSnapshot": "history-seed/project"}
	plan["historyProject"] = project
	if tool.Str(plan["model"]) == "" {
		plan["model"] = previous["model"]
	}
	return nil
}
