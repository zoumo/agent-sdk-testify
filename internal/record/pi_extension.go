package record

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func preparePiExtension(run lifecycle.RecordContext, plan tool.Object) error {
	origin := run.Execution().Artifact.OfficialOrigin
	dir := filepath.Join(run.Fixture().ResourceRoot, "official", origin.RunID, corpus.PiExtensionDirectory)
	path := filepath.Join(dir, corpus.PiExtensionFilename)
	digest, err := tool.FileDigest(path)
	if err != nil {
		return err
	}
	if digest != corpus.PiExtensionSHA256 {
		return errors.New("staged Pi extension differs from its pinned Go SDK source")
	}
	var config tool.Object
	if err := tool.ReadJSON(filepath.Join(dir, "session.json"), &config); err != nil {
		return err
	}
	expected := corpus.PiExtensionConfig(tool.Str(plan["caseId"]))
	if !reflect.DeepEqual(config, expected) {
		return errors.New("staged Pi extension configuration differs from the selected case")
	}
	plan["args"] = append(tool.Strings(plan["args"]), "--no-extensions", "--no-skills",
		"--no-context-files", "--no-prompt-templates", "-e", path)
	plan["piExtension"] = corpus.PiExtensionIdentity()
	return corpus.ValidatePiExtensionPlan(plan)
}
