package official

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func admitVerified(materials string, summary tool.Object, err error, c corpus.Corpus) error {
	if summary == nil || c.Index == "" {
		return err
	}
	root := tool.Str(summary["output"])
	admission := tool.Object{"status": "rejected"}
	var verification tool.Object
	if e := tool.ReadJSON(filepath.Join(root, "verification.json"), &verification); e == nil && verification["schemaVersion"] == float64(3) {
		return admitPackage(materials, root, err, c)
	}
	var descriptor corpus.Material
	if e := tool.ReadJSON(filepath.Join(materials, "material.json"), &descriptor); e == nil && descriptor.FormatVersion == 3 {
		return admitPackage(materials, root, err, c)
	}
	if err == nil {
		var verification tool.Object
		err = tool.ReadJSON(filepath.Join(root, "verification.json"), &verification)
		if err == nil {
			var files tool.Object
			files, err = corpus.Inventory(materials, false)
			if err == nil && (verification["status"] != "passed" || !reflect.DeepEqual(files, verification["sourceFiles"])) {
				err = errors.New("admission requires the exact verified material")
			}
		}
	}
	if err == nil {
		var review tool.Object
		review, err = c.Qualification(materials, root, false)
		reviewPath := filepath.Join(root, "qualification.json")
		if err == nil {
			err = corpus.Write(reviewPath, review)
		}
		if err == nil {
			var target string
			target, err = c.Adopt(materials, root, reviewPath)
			admission["materials"] = target
			if err == nil {
				admission["status"] = "admitted"
			}
		}
	}
	if err != nil {
		admission["error"] = err.Error()
	}
	return errors.Join(err, tool.WriteJSON(filepath.Join(root, "admission-summary.json"), admission))
}

// Admission is a separate transaction after verification sealing. Its diagnostics
// are runtime output and cannot mutate the already sealed verification domain.
func admitPackage(materials, root string, err error, c corpus.Corpus) error {
	private := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(materials))), "runtime", "official", filepath.Base(root))
	summary := tool.Object{"status": "rejected"}
	var review tool.Object
	if err == nil {
		review, err = c.Qualification(materials, root, false)
	}
	if err == nil {
		path := filepath.Join(private, "qualification.json")
		err = corpus.Write(path, review)
		if err == nil {
			var target string
			target, err = c.Adopt(materials, root, path)
			if err == nil {
				summary["status"], summary["materials"] = "admitted", target
			}
		}
	}
	if err != nil {
		summary["error"] = err.Error()
	}
	return errors.Join(err, tool.WriteJSON(filepath.Join(private, "admission-summary.json"), summary))
}
