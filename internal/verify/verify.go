package verify

import (
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type Options struct {
	SessionDir string
	Timeout    time.Duration
	Index      string
	Scenarios  string
	Archive    string
}

func admitVerified(materials string, summary tool.Object, err error, c corpus.Corpus) error {
	if summary == nil || c.Index == "" {
		return err
	}
	root := tool.Str(summary["output"])
	admission := tool.Object{"status": "rejected"}
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
