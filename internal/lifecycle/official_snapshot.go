package lifecycle

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// SnapshotOfficialRecord carries settled original facts into qualification and
// its archive. It never changes the sealed material or resolves another fixture.
func SnapshotOfficialRecord(fixtureDir string, fixture *Fixture, material corpus.Material, target string) error {
	if err := corpus.ValidateOfficialCapture(material); err != nil {
		return err
	}
	if err := validateOfficialOrigin(fixtureDir, fixture, material.OfficialOrigin); err != nil {
		return err
	}
	root := filepath.Join(fixtureDir, "official-records", material.OfficialOrigin.RunID)
	if err := corpus.ValidateOfficialRecord(root, material); err != nil {
		return err
	}
	if err := tool.CopyTree(root, filepath.Join(target, "run")); err != nil {
		return err
	}
	if err := tool.CopyFile(FixturePath(fixtureDir), filepath.Join(target, "fixture.json")); err != nil {
		return err
	}
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "result.json"), &result); err != nil {
		return err
	}
	executionID := ""
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return err
	}
	for _, item := range tool.Array(result["connections"]) {
		ref := tool.Obj(item)
		id := tool.Str(ref["executionId"])
		if err := validateExecutionID(id); err != nil {
			return err
		}
		if executionID != "" && executionID != id {
			return errors.New("official capture connections disagree on execution")
		}
		executionID = id
		dir := ExecutionDir(fixtureDir, id)
		execution, err := ReadExecution(fixtureDir, fixture, filepath.Join(dir, "execution.json"))
		if err != nil {
			return err
		}
		if execution.Command != "record" || !reflect.DeepEqual(execution.Artifact, material) {
			return errors.New("official capture execution differs from material")
		}
		if err := validateConnectionInventory(fixtureDir, *execution, &material, fixture.Routes["record"]); err != nil {
			return err
		}
		found := false
		digest, err := tool.FileDigest(filepath.Join(dir, "execution.json"))
		if err != nil {
			return err
		}
		for _, indexed := range index.Executions {
			if indexed.ID != id {
				continue
			}
			found = true
			if indexed.Status != "passed" || indexed.Command != "record" || indexed.DescriptorSHA256 != digest {
				return errors.New("official capture execution is not settled")
			}
			if err := validateIndexedConnections(indexed, *execution); err != nil {
				return err
			}
		}
		if !found {
			return errors.New("official capture execution is absent from index")
		}
		connection := tool.Str(ref["connectionId"])
		if err := validateExecutionID(connection); err != nil {
			return err
		}
		if err := tool.CopyFile(filepath.Join(dir, "receipts", connection+".result.json"), filepath.Join(target, "results", connection+".json")); err != nil {
			return err
		}
	}
	if executionID == "" {
		return errors.New("official capture omits actual execution")
	}
	dir := ExecutionDir(fixtureDir, executionID)
	preparation := tool.Obj(result["recordPreparation"])
	if preparation["executionId"] != executionID {
		return errors.New("original native preparation binding is missing")
	}
	switch preparation["schemaVersion"] {
	case float64(1):
		for name, path := range map[string]string{
			"isolationSHA256":    filepath.Join(dir, "receipts", "isolation.json"),
			"preparedPlanSHA256": filepath.Join(fixture.ResourceRoot, "executions", executionID, "record-plan.json"),
		} {
			digest, err := tool.FileDigest(path)
			if err != nil {
				return err
			}
			if digest != preparation[name] {
				return errors.New("original native preparation evidence changed")
			}
		}
		for name, source := range map[string]string{
			"execution.json":     filepath.Join(dir, "execution.json"),
			"isolation.json":     filepath.Join(dir, "receipts", "isolation.json"),
			"prepared-plan.json": filepath.Join(fixture.ResourceRoot, "executions", executionID, "record-plan.json"),
		} {
			if err := tool.CopyFile(source, filepath.Join(target, name)); err != nil {
				return err
			}
		}
	case float64(2):
		for name, file := range map[string]string{
			"execution": "execution.json", "isolation": "isolation.json",
			"preparedPlan": "prepared-plan.json", "materialPlan": "material-plan.json",
		} {
			reference := tool.Obj(preparation[name])
			path, err := ResolveFixtureReference(fixtureDir, tool.Str(reference["path"]))
			if err != nil {
				return err
			}
			digest, err := tool.FileDigest(path)
			if err != nil {
				return err
			}
			if digest != reference["sha256"] {
				return errors.New("original native preparation evidence changed")
			}
			if err := tool.CopyFile(path, filepath.Join(target, file)); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported original native preparation schemaVersion")
	}
	return nil
}
