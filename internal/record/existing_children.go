package record

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func snapshotChildSetup(run lifecycle.RecordContext) error {
	plan := run.Plan()
	stage := filepath.Join(run.ArtifactRoot(), "setup-snapshot")
	bindings, err := history.CaptureCanonical("", run.OverlayRoot(), stage, plan, nil)
	if err != nil {
		return err
	}
	var parent string
	children := []string{}
	for _, binding := range bindings {
		if !strings.HasSuffix(binding.NativeTarget, ".jsonl") {
			continue
		}
		if binding.ParentSessionID == "" {
			if parent != "" {
				return errors.New("existing-child setup has multiple native parents")
			}
			parent = binding.SessionID
		} else {
			children = append(children, binding.SessionID)
		}
	}
	if parent == "" || len(children) != 1 {
		return errors.New("existing-child setup must save exactly one parent and one child")
	}
	for index := range bindings {
		binding := &bindings[index]
		if binding.SessionID != parent && binding.ParentSessionID != parent {
			return errors.New("existing-child setup native parent-child relationship differs")
		}
		binding.Role = "seed"
		binding.Source = strings.Replace(binding.Source, "history/runtime/", "history/seed/", 1)
	}
	seed := filepath.Join(run.ArtifactRoot(), "history", "seed")
	if err := os.MkdirAll(filepath.Dir(seed), 0o700); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(stage, "history", "runtime"), seed); err != nil {
		return err
	}
	if err := os.RemoveAll(stage); err != nil {
		return err
	}
	rawHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "raw", "raw.jsonl"))
	if err != nil {
		return err
	}
	transportHash, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "raw", "transport.json"))
	if err != nil {
		return err
	}
	setup := tool.Object{
		"schemaVersion": 1, "provider": plan["provider"], "sessionId": parent,
		"agentIds": children, "historyBindings": bindings,
		"rawSHA256": rawHash, "transportSHA256": transportHash,
		"completedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := os.MkdirAll(filepath.Join(run.ArtifactRoot(), "existing-children"), 0o700); err != nil {
		return err
	}
	if err := tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "existing-children", "setup.json"), setup); err != nil {
		return err
	}
	plan["existingChildrenSetup"] = setup
	if err := tool.WriteJSON(filepath.Join(run.OverlayRoot(), "record-plan.json"), plan); err != nil {
		return err
	}
	return tool.WriteJSON(filepath.Join(run.OverlayRoot(), "material-plan.json"), corpus.MaterialPlanFromRuntime(plan))
}

func prepareChildObservation(run lifecycle.RecordContext, plan tool.Object) error {
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(run.ExecutionDir(), "receipts", "main.result.json"), &result); err != nil {
		return err
	}
	if result["schemaVersion"] != float64(3) || result["status"] != "passed" ||
		result["connectionId"] != "main" || result["executionId"] != run.Execution().ExecutionID ||
		result["materialId"] != run.Execution().Artifact.ID {
		return errors.New("existing-child observation requires its own settled successful setup connection")
	}
	if err := corpus.ValidateChildSetup(run.ArtifactRoot(), plan); err != nil {
		return err
	}
	digest, err := tool.FileDigest(filepath.Join(run.ArtifactRoot(), "existing-children", "setup.json"))
	if err != nil {
		return err
	}
	return tool.WriteJSON(filepath.Join(run.ArtifactRoot(), "existing-children", "observation.json"), tool.Object{
		"schemaVersion": 1, "sessionId": tool.Obj(plan["existingChildrenSetup"])["sessionId"],
		"setupSHA256": digest, "startedAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
}
