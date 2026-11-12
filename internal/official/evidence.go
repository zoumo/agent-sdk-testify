package official

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func executionReferences(root, materialID string, refs []tool.Object) ([]tool.Object, error) {
	seen := map[string]bool{}
	var selected []tool.Object
	for _, ref := range refs {
		id := tool.Str(ref["executionId"])
		if seen[id] {
			continue
		}
		if err := validateExecutionName(id); err != nil {
			return nil, err
		}
		seen[id] = true
		dir := lifecycle.PackageExecutionRoot(root, materialID, id)
		var result tool.Object
		if err := tool.ReadJSON(filepath.Join(dir, "result.json"), &result); err != nil {
			return nil, err
		}
		if result["schemaVersion"] != float64(1) || result["executionId"] != id || result["materialId"] != materialID || result["status"] != "passed" {
			return nil, errors.New("official execution did not seal successfully")
		}
		hash, err := tool.FileDigest(filepath.Join(dir, "result.json"))
		if err != nil {
			return nil, err
		}
		selected = append(selected, tool.Object{"executionId": id, "path": "executions/" + id + "/result.json", "sha256": hash, "executionSHA256": result["executionSHA256"], "connections": result["connections"]})
	}
	return selected, nil
}

// A peer envelope retains the supervisor's facts and the running notification.
// The final result stays solely in its ordinary execution domain.
func sealPeerEvidence(activity *lifecycle.OfficialRun, root string, peers map[string]tool.Command) error {
	if err := os.MkdirAll(filepath.Join(root, "peers"), 0o700); err != nil {
		return err
	}
	for id := range peers {
		processPath := filepath.Join(root, "peer-"+id+".process.json")
		var process tool.Object
		if err := tool.ReadJSON(processPath, &process); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if process["reaped"] != true || process["cleanupComplete"] != true {
			return errors.New("peer writer is not confirmed stopped")
		}
		started, finished, err := readPeerNotifications(filepath.Join(root, "peer-"+id+".results.jsonl"))
		if err != nil {
			return err
		}
		materialID := tool.Str(started["materialId"])
		if validateExecutionName(materialID) != nil {
			return errors.New("peer material identity is unknown")
		}
		path, err := lifecycle.ResolvePackageReference(activity.FixtureDir, materialID, tool.Str(finished["result"]))
		if err != nil {
			return err
		}
		var durable tool.Object
		if err := tool.ReadJSON(path, &durable); err != nil {
			return err
		}
		if !reflect.DeepEqual(durable, finished) {
			return errors.New("peer notification differs from durable result")
		}
		hash, err := tool.FileDigest(path)
		if err != nil {
			return err
		}
		process["running"] = started
		process["result"] = tool.Object{"path": finished["result"], "sha256": hash}
		if err := tool.WriteJSON(filepath.Join(root, "peers", id+".json"), process); err != nil {
			return err
		}
		if err := os.Remove(processPath); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(root, "peer-"+id+".results.jsonl")); err != nil {
			return err
		}
	}
	return nil
}
