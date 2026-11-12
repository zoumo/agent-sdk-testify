package native

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const isolatedRecording = "personal-skills-v1"

// A completed capture supplies native saved records. Preparing a later case
// does not execute an SDK, start a session, or require the replay stage first.
func PrepareHistorySeed(source, root string, plan tool.Object) error {
	return prepareHistorySeed(source, root, plan)
}

// ReadHistorySeed reconstructs preparation facts from the frozen source without
// changing it or touching the writable native environment.
func ReadHistorySeed(source string, plan tool.Object) error {
	return prepareHistorySeed(source, "", plan)
}

func prepareHistorySeed(source, root string, plan tool.Object) error {
	var err error
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	sourceSnapshot := source
	source = history.SeedSource(source)
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
	if report["status"] == "captured" || manifest["materialFormatVersion"] == float64(3) {
		report, err = corpus.SeedBusinessReport(source)
		if err != nil {
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
	sourcePrefix, err := filepath.Rel(sourceSnapshot, source)
	if err != nil {
		return err
	}
	if session == "" || len(agents) == 0 || agents[""] {
		return errors.New("seed has no saved parent and child identities")
	}
	files := tool.Object{}
	found := map[string]bool{}
	type seedFile struct {
		source string
		digest string
	}
	selected := map[string]seedFile{}
	var canonical []history.Binding
	if manifest["materialFormatVersion"] == float64(3) {
		bindings, err := history.CanonicalBindings(source, previous, manifest)
		if err != nil {
			return err
		}
		for _, binding := range bindings {
			selected[binding.NativeTarget] = seedFile{source: binding.Source, digest: binding.SHA256}
			binding.Role = "seed"
			binding.Source = filepath.ToSlash(filepath.Join("history", "seed", sourcePrefix, binding.Source))
			canonical = append(canonical, binding)
		}
	} else {
		for name, digest := range tool.Obj(manifest["savedFiles"]) {
			selected[name] = seedFile{source: name, digest: tool.Str(digest)}
		}
	}
	for name, file := range selected {
		if !filepath.IsLocal(name) {
			return errors.New("seed file escapes source")
		}
		data, e := os.ReadFile(filepath.Join(source, filepath.FromSlash(file.source)))
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
		if actual != file.digest {
			return errors.New("seed content changed")
		}
		if root != "" {
			if err := tool.CopyFile(filepath.Join(source, filepath.FromSlash(file.source)), filepath.Join(root, name)); err != nil {
				return err
			}
		}
		files[name] = tool.Object{"source": filepath.ToSlash(filepath.Join("history", "seed", sourcePrefix, file.source)), "sha256": actual}
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
	if root != "" && provider == "codex" && project == filepath.Join(source, "project") {
		if err := prepareCodexIsolation(root, tool.Object{"project": project}); err != nil {
			return err
		}
	}
	snapshot := filepath.Join(sourceSnapshot, "project")
	if root != "" {
		snapshot = filepath.Join(root, "history-seed", "project")
		if err := os.MkdirAll(snapshot, 0o700); err != nil {
			return err
		}
		if err := tool.WriteFiles(snapshot, tool.Obj(tool.Obj(previous["scenario"])["files"])); err != nil {
			return err
		}
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
	if err := filepath.WalkDir(sourceSnapshot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported seed source entry: %s", current)
		}
		name, err := filepath.Rel(sourceSnapshot, current)
		if err != nil {
			return err
		}
		digest, err := tool.FileDigest(current)
		if err != nil {
			return err
		}
		sourceFiles[filepath.ToSlash(name)] = digest
		return nil
	}); err != nil {
		return err
	}
	for _, file := range []string{"plan.json", "manifest.json", "report.json", "raw/raw.jsonl"} {
		name := filepath.ToSlash(filepath.Join(sourcePrefix, file))
		if sourceFiles[name] == nil {
			return fmt.Errorf("seed source closure omits %s", name)
		}
	}
	plan["historySeed"] = tool.Object{
		"provider": provider, "sourceRootRef": "history/seed", "sourceProject": project,
		"sessionId": session, "agentIds": ids, "files": files, "sourceFiles": sourceFiles,
		"projectFiles": projectFiles, "projectSnapshot": "history-seed/project",
		"materialPlan": tool.Object{"path": filepath.ToSlash(filepath.Join("history", "seed", sourcePrefix, "plan.json")), "sha256": manifest["planSHA256"]},
	}
	if canonical != nil {
		plan["historyBindings"] = canonical
	}
	plan["historyProject"] = project
	if tool.Str(plan["model"]) == "" {
		plan["model"] = previous["model"]
	}
	return nil
}
