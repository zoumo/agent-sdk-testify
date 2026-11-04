package lifecycle

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// SnapshotOfficialRecord carries settled original facts into qualification and
// its archive. It never changes the sealed material or resolves another session.
func SnapshotOfficialRecord(sessionDir string, session *Session, material Artifact, target string) error {
	if err := ValidateOfficialCapture(material); err != nil {
		return err
	}
	if err := ValidateOfficialRecord(sessionDir, session, material); err != nil {
		return err
	}
	root := filepath.Join(sessionDir, "official-records", material.OfficialOrigin.RunID)
	if err := tool.CopyTree(root, filepath.Join(target, "run")); err != nil {
		return err
	}
	if err := tool.CopyFile(filepath.Join(sessionDir, "session.json"), filepath.Join(target, "session.json")); err != nil {
		return err
	}
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "result.json"), &result); err != nil {
		return err
	}
	executionID := ""
	index, err := readIndex(IndexPath(sessionDir))
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
		dir := ExecutionDir(sessionDir, id)
		var execution Execution
		if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
			return err
		}
		if err := execution.Validate(*session); err != nil {
			return err
		}
		if execution.Command != "record" || !reflect.DeepEqual(execution.Artifact, material) {
			return errors.New("official capture execution differs from material")
		}
		if err := validateConnectionInventory(execution, &material, session.Routes["record"]); err != nil {
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
			if err := validateIndexedConnections(indexed, execution); err != nil {
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
	dir := ExecutionDir(sessionDir, executionID)
	preparation := tool.Obj(result["recordPreparation"])
	if preparation["executionId"] != executionID {
		return errors.New("original native preparation binding is missing")
	}
	for name, path := range map[string]string{
		"isolationSHA256":    filepath.Join(dir, "receipts", "isolation.json"),
		"preparedPlanSHA256": filepath.Join(session.ResourceRoot, "executions", executionID, "record-plan.json"),
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
		"prepared-plan.json": filepath.Join(session.ResourceRoot, "executions", executionID, "record-plan.json"),
	} {
		if err := tool.CopyFile(source, filepath.Join(target, name)); err != nil {
			return err
		}
	}
	return nil
}
