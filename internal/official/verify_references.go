package official

import (
	"errors"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// References come exclusively from each launcher's actual notification pipe.
// No directory inventory, latest execution or caller-supplied success is used.
func mockReferences(fixtureDir string, fixture *lifecycle.Fixture, root string, ids []string, material corpus.Material, previous map[string]bool) ([]tool.Object, error) {
	return collectPeerReferences(ids, func(id string) (tool.Object, error) {
		return mockReference(fixtureDir, fixture, root, id, material, previous)
	})
}

func mockReference(fixtureDir string, fixture *lifecycle.Fixture, root, id string, pinned corpus.Material, previous map[string]bool) (tool.Object, error) {
	ref, execution, connection, err := readPeerReference(fixtureDir, fixture, root, id, "mock", func(execution lifecycle.Execution) error {
		return lifecycle.ValidateMockExecution(fixtureDir, fixture, execution)
	})
	if err != nil {
		return ref, err
	}
	material := execution.Artifact
	if previous[execution.ExecutionID] || !reflect.DeepEqual(material, pinned) {
		return ref, errors.New("mock does not belong to this verification and exact material")
	}
	ref["material"] = material
	started, finished := tool.Obj(ref["started"]), tool.Obj(ref["finished"])
	for _, value := range []tool.Object{started, finished} {
		m := tool.Obj(value["material"])
		if m["id"] != material.ID || m["planSHA256"] != material.PlanSHA256 || m["manifestSHA256"] != material.ManifestSHA256 || m["recordingSHA256"] != material.RecordingSHA256 {
			return ref, errors.New("mock notification material differs")
		}
	}
	processReceiptReference, err := lifecycle.PackageReference(fixtureDir, pinned.ID, connection.ProcessReceipt)
	if err != nil {
		return ref, err
	}
	if finished["processReceipt"] != processReceiptReference || started["processReceipt"] != processReceiptReference {
		return ref, errors.New("mock process receipt reference differs")
	}
	if err := lifecycle.ValidateMockConnection(fixture.AgentType, *execution, *connection); err != nil {
		return ref, err
	}
	dir := lifecycle.PackageExecutionRoot(fixtureDir, pinned.ID, execution.ExecutionID)
	hashes := tool.Object{}
	for _, path := range []string{connection.Receipt, connection.ProcessReceipt, filepath.Join(dir, "receipts", id+".result.json")} {
		reference, err := lifecycle.PackageReference(fixtureDir, pinned.ID, path)
		if err != nil {
			return ref, err
		}
		hash, err := tool.FileDigest(path)
		if err != nil {
			return ref, err
		}
		hashes[reference] = hash
	}
	ref["receiptHashes"] = hashes
	return ref, nil
}
