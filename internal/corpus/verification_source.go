package corpus

import (
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// Legacy material keeps its original evidence contract. New public recordings
// seal capture provenance before the parent business report exists.
func ValidateVerificationSource(material Material, inventory tool.Object) error {
	if material.OfficialOrigin == nil {
		for _, name := range SourceEvidence {
			if inventory[name] == nil {
				return fmt.Errorf("recording omits %s", name)
			}
		}
	}
	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(material.Path, "plan.json"), &plan); err != nil {
		return err
	}
	v := &Validation{}
	manifest := v.Read(material.Path, "manifest.json")
	v.Check(manifest["planSHA256"] == inventory["plan.json"], "source plan hash differs")
	for _, phase := range Phases(plan) {
		raw := filepath.Join(material.Path, phase[1], "raw")
		m := v.Read(raw, "manifest.json")
		v.Hashes(raw, tool.Obj(m["files"]))
		for _, name := range []string{"raw.jsonl", "transport.json", "stderr.raw"} {
			v.Check(tool.Obj(m["files"])[name] != nil, "raw manifest omits required artifact: "+name)
		}
	}
	if inventory["adoption.json"] != nil {
		adoption := v.Read(material.Path, "adoption.json")
		files := tool.Object{}
		for name, digest := range inventory {
			if name != "adoption.json" && name != "material.json" {
				files[name] = digest
			}
		}
		v.Check(reflect.DeepEqual(files, adoption["files"]), "adopted corpus inventory drift")
		review := v.Read(material.Path, "review.json")
		v.Check(reflect.DeepEqual(review["captureFiles"], adoption["originalFiles"]), "original provenance hash inventory drift")
	}
	return v.Err
}
