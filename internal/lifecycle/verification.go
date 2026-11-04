package lifecycle

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func validateOfficialVerification(sessionDir string, session *Session, id string) (tool.Object, error) {
	if err := validateExecutionID(id); err != nil {
		return nil, err
	}
	root := filepath.Join(sessionDir, "verifications", id)
	evidence, err := readEvidenceObject(filepath.Join(root, "verification.json"), "official verification")
	if err != nil {
		return nil, err
	}
	if evidence["sessionId"] != session.SessionID || evidence["id"] != id || evidence["pipeline"] != "public-agent-mock" {
		return evidence, errors.New("official verification identity differs")
	}
	var value struct {
		Material corpus.Material `json:"material"`
	}
	if err := tool.ReadJSON(filepath.Join(root, "verification.json"), &value); err != nil {
		return evidence, err
	}
	material := value.Material
	if material.Path != filepath.Join(sessionDir, "materials", material.ID) {
		return evidence, errors.New("verified material escapes session")
	}
	inspection, err := inspectMaterial(session, material)
	if err != nil {
		return evidence, err
	}
	// Finish checks the persisted verification contract. Runtime verifier
	// selection and driver launch belong to official.
	if material.Provenance != "official" && material.Provenance != "builtin" {
		return evidence, errors.New("verified material provenance must be official or builtin")
	}
	bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: session.AgentType, Case: tool.Str(inspection.Plan["caseId"]), Model: tool.Str(inspection.Plan["model"])})
	if err != nil {
		return evidence, err
	}
	if err := catalog.CheckVerifierPlan(inspection.Plan, bound); err != nil {
		return evidence, err
	}
	v := &corpus.Validation{}
	v.Replay(material.Path, root, inspection.Plan, "verify")
	result, resultErr := readEvidenceObject(filepath.Join(root, "result.json"), "official verify orchestration")
	if resultErr != nil {
		return evidence, errors.Join(v.Err, resultErr)
	}
	if result["id"] != id || result["sessionId"] != session.SessionID || result["status"] != "passed" || result["cleanupComplete"] != true || result["error"] != nil {
		v.Check(false, "official verify orchestration or admission failed")
	}

	if !reflect.DeepEqual(result["admission"], evidence["admission"]) {
		v.Check(false, "persisted admission differs from verification evidence")
	}
	for _, item := range tool.Array(evidence["connections"]) {
		ref := tool.Obj(item)
		executionID := tool.Str(ref["executionId"])
		if err := validateExecutionID(executionID); err != nil {
			return evidence, errors.Join(v.Err, err)
		}
		path := filepath.Join(sessionDir, "executions", executionID, "execution.json")
		hash, err := tool.FileDigest(path)
		if err != nil || hash != ref["executionSHA256"] {
			return evidence, errors.Join(v.Err, errors.New("verified mock descriptor changed"), err)
		}
		var execution Execution
		if err := tool.ReadJSON(path, &execution); err != nil {
			return evidence, err
		}
		if err := ValidateMockExecution(sessionDir, session, execution); err != nil {
			return evidence, err
		}
		if execution.Command != "mock" || !reflect.DeepEqual(execution.Artifact, material) {
			return evidence, errors.New("verified execution material differs")
		}
		for _, connection := range execution.Connections {
			if connection.ID != ref["connectionId"] {
				continue
			}
			if err := ValidateMockConnection(session.AgentType, execution, connection); err != nil {
				return evidence, err
			}
			for _, receipt := range []string{connection.Receipt, connection.ProcessReceipt, filepath.Join(filepath.Dir(path), "receipts", connection.ID+".result.json")} {
				hash, err := tool.FileDigest(receipt)
				if err != nil || hash != tool.Obj(ref["receiptHashes"])[receipt] {
					return evidence, errors.Join(errors.New("verified mock receipt changed"), err)
				}
			}
		}
	}
	return evidence, v.Err
}
