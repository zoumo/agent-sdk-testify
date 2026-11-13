package corpus

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ValidateCore verifies the closed format 3 inventory. The two envelopes are
// excluded from that inventory: the descriptor hashes the manifest, and the
// caller hashes the descriptor. No object hashes itself.
func ValidateCore(root string, material Material) error {
	var manifest, plan tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
		return err
	}
	if manifest["materialFormatVersion"] != float64(3) {
		return errors.New("format 3 material requires format 3 manifest")
	}
	if err := tool.ReadJSON(filepath.Join(root, "plan.json"), &plan); err != nil {
		return err
	}
	actual, err := history.HashTree(root)
	if err != nil {
		return err
	}
	delete(actual, "manifest.json")
	delete(actual, "material.json")
	files := tool.Obj(manifest["files"])
	for _, key := range []string{"savedFiles", "caseFiles", "remotePhaseFiles", "driverFiles"} {
		if _, ok := manifest[key]; ok {
			return errors.New("format 3 manifest contains a duplicate inventory")
		}
	}
	if len(files) != len(actual) {
		return errors.New("material core inventory is incomplete or contains extra files")
	}
	for name, hash := range actual {
		if files[name] != hash {
			return fmt.Errorf("material core inventory hash mismatch: %s", name)
		}
	}
	for _, name := range []string{"source-plan.json", "plan.json", "raw/raw.jsonl", "raw/manifest.json", "raw/transport.json", "raw/stderr.raw", "report.json"} {
		if actual[name] == "" {
			return fmt.Errorf("material core omits %s", name)
		}
	}
	for name := range actual {
		if name == "material-plan.json" || strings.HasPrefix(name, "config/") || strings.HasPrefix(name, "history-seed/") || strings.HasPrefix(name, "history/derived/") {
			return fmt.Errorf("format 3 contains duplicate runtime history or plan: %s", name)
		}
	}
	seedMetadata := tool.Obj(plan["historySeed"])
	for _, key := range []string{"files", "sourceFiles", "projectFiles", "sourceRootRef", "projectSnapshot"} {
		if _, ok := seedMetadata[key]; ok {
			return errors.New("format 3 plan contains a duplicate history inventory or runtime reference")
		}
	}
	if material.SourcePlanSHA256 == "" || manifest["sourcePlanSHA256"] != material.SourcePlanSHA256 || plan["sourcePlanSHA256"] != material.SourcePlanSHA256 || actual["source-plan.json"] != material.SourcePlanSHA256 {
		return errors.New("material source plan hash mismatch")
	}
	if material.PlanSHA256 != actual["plan.json"] || manifest["planSHA256"] != material.PlanSHA256 {
		return errors.New("material derived plan hash mismatch")
	}
	if hash, err := tool.FileDigest(filepath.Join(root, "manifest.json")); err != nil || hash != material.ManifestSHA256 {
		return errors.New("material manifest hash mismatch")
	}
	if material.RecordingSHA256 != actual["raw/raw.jsonl"] {
		return errors.New("material recording hash mismatch")
	}
	for _, name := range []string{"root", "home", "config", "raw", "_seedRoot", "_recordedWorkspace", "_executionWorkspace", "_agentResults", "_replay"} {
		if _, ok := plan[name]; ok {
			return fmt.Errorf("material plan contains runtime field %s", name)
		}
	}
	if material.OriginSHA256 != "" {
		if actual["official-origin.json"] != material.OriginSHA256 {
			return errors.New("material official origin hash mismatch")
		}
		if err := ValidateOfficialCapture(material); err != nil {
			return err
		}
	} else if _, ok := actual["official-origin.json"]; ok {
		return errors.New("unbound official origin")
	}
	for name := range actual {
		if strings.HasPrefix(name, "origin/") {
			bound := false
			if material.OriginSHA256 != "" {
				for _, id := range material.Connections {
					if name == "origin/"+id+".json" {
						bound = true
					}
				}
			}
			if !bound {
				return errors.New("unbound official connection proof")
			}
		}
	}
	seed := filepath.Join(root, "history", "seed")
	if _, seedErr := os.Lstat(seed); seedErr == nil {
		source := history.SeedSource(seed)
		if _, planErr := os.Lstat(filepath.Join(source, "plan.json")); planErr == nil {
			if e := VerifyMaterials(source, true); e != nil {
				return e
			}
			if _, e := history.Bindings(source); e != nil {
				return e
			}
		}
	} else if !os.IsNotExist(seedErr) {
		return seedErr
	}
	_, err = history.Bindings(root)
	return err
}

// SealCoreInventory is called only after all core writers have closed.
func SealCoreInventory(root string) error {
	var manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
		return err
	}
	files, err := history.HashTree(root)
	if err != nil {
		return err
	}
	delete(files, "manifest.json")
	delete(files, "material.json")
	for _, name := range []string{"savedFiles", "caseFiles", "remotePhaseFiles", "driverFiles"} {
		delete(manifest, name)
	}
	manifest["materialFormatVersion"] = 3
	manifest["files"] = files
	return tool.WriteJSON(filepath.Join(root, "manifest.json"), manifest)
}

// RecordingRoot recognizes layouts, never a recursively discovered plan.
func RecordingRoot(root string) (core string, completePackage bool, err error) {
	_, nested := os.Lstat(filepath.Join(root, "material", "material.json"))
	_, direct := os.Lstat(filepath.Join(root, "material.json"))
	_, plan := os.Lstat(filepath.Join(root, "plan.json"))
	if nested == nil {
		if direct == nil || plan == nil {
			return "", false, errors.New("conflicting package and standalone material layouts")
		}
		core = filepath.Join(root, "material")
		m, e := ReadMaterialDescriptor(core)
		if e != nil {
			return "", false, e
		}
		if m == nil {
			return "", false, errors.New("complete package requires a material descriptor")
		}
		if e := CheckMaterialInventory(core, *m); e != nil {
			return "", false, e
		}
		if e := ValidatePackage(root, *m); e != nil {
			return "", false, e
		}
		return core, true, nil
	}
	if !os.IsNotExist(nested) {
		return "", false, nested
	}
	if plan != nil {
		return "", false, errors.New("recording has no supported material plan")
	}
	return root, false, nil
}
