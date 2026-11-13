package corpus

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// SeedBusinessReport reads the explicitly selected official record. A sealed
// core alone proves capture, never the business qualification of a history seed.
func SeedBusinessReport(core string) (tool.Object, error) {
	material, err := ReadMaterialDescriptor(core)
	if err != nil {
		return nil, err
	}
	if material == nil || material.FormatVersion != 3 {
		if _, err := VerifyAdoptedFiles(core); err != nil {
			return nil, err
		}
		return Read(filepath.Join(core, "official-replay", "driver", "record-origin", "run", "driver", "report.json"))
	}
	if filepath.Base(core) != "material" {
		return nil, errors.New("child-history seed requires a qualified complete package, not a standalone core")
	}
	root := filepath.Dir(core)
	review, err := VerifyAdoptedFiles(root)
	if err != nil {
		return nil, err
	}
	qualification := tool.Obj(review["qualification"])
	if _, err := time.Parse(time.RFC3339Nano, tool.Str(qualification["qualifiedAt"])); err != nil || review["origin"] != "real-agent" || qualification["method"] != "official-sdk" {
		return nil, errors.New("seed review must attest official qualification")
	}
	data, err := json.Marshal(review)
	if err != nil {
		return nil, err
	}
	var selected PackageSelection
	if err := json.Unmarshal(data, &selected); err != nil {
		return nil, err
	}
	expected, err := packageSelection(root, filepath.Base(filepath.Dir(selected.Verification.Path)), *material)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(selected, expected) {
		return nil, errors.New("seed selection differs from its qualified official closure")
	}
	return Read(filepath.Join(root, filepath.Dir(selected.Record.Path), "report.json"))
}
