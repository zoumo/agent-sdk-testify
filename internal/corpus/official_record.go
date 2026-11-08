package corpus

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// ValidateOfficialRecord is the downstream qualification seam. Capture ownership
// alone is insufficient: verify/admission must require the exact successful run,
// business report, supervised connections and unchanged material hashes.
func ValidateOfficialRecord(root string, artifact Material) error {
	if artifact.OfficialOrigin == nil {
		return errors.New("official capture origin is incomplete")
	}
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "result.json"), &result); err != nil {
		return err
	}
	process := tool.Obj(result["driverProcess"])
	if result["status"] != "passed" || evidenceFixtureID(result) != artifact.OfficialOrigin.FixtureID || result["cleanupComplete"] != true || process["reaped"] != true || process["exitCode"] != float64(0) || tool.Obj(result["business"])["status"] != "passed" {
		return errors.New("official capture has no successful settled orchestration")
	}
	files, err := history.HashTree(filepath.Join(root, "driver"))
	if err != nil {
		return err
	}
	var expected map[string]string
	if err := tool.ReadJSON(filepath.Join(root, "driver-files.json"), &expected); err != nil {
		return err
	}
	if !reflect.DeepEqual(files, expected) {
		return errors.New("official driver evidence changed")
	}
	if tool.Obj(result["driver"])["sha256"] != artifact.OfficialOrigin.DriverSHA256 {
		return errors.New("official driver identity differs from capture")
	}
	connections := tool.Array(result["connections"])
	if len(connections) != len(artifact.Connections) {
		return errors.New("official result omits connections")
	}
	for _, id := range artifact.Connections {
		found := false
		for _, item := range connections {
			ref := tool.Obj(item)
			material := tool.Obj(ref["material"])
			if ref["connectionId"] == id && material["id"] == artifact.ID && material["manifestSHA256"] == artifact.ManifestSHA256 && material["recordingSHA256"] == artifact.RecordingSHA256 && material["planSHA256"] == artifact.PlanSHA256 && tool.Obj(ref["finished"])["status"] == "passed" {
				found = true
			}
		}
		if !found {
			return errors.New("official result does not bind exact capture")
		}
	}
	return nil
}
