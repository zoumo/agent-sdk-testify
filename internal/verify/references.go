package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// References come exclusively from each launcher's actual notification pipe.
// No directory inventory, latest execution or caller-supplied success is used.
func mockReferences(sessionDir string, session *lifecycle.Session, root string, ids []string, material lifecycle.Artifact, previous map[string]bool) ([]tool.Object, error) {
	var refs []tool.Object
	var failures []error
	executionID := ""
	for _, id := range ids {
		ref, err := mockReference(sessionDir, session, root, id, material, previous)
		if ref != nil {
			refs = append(refs, ref)
		}
		if err == nil {
			if executionID == "" {
				executionID = tool.Str(ref["executionId"])
			} else if executionID != tool.Str(ref["executionId"]) {
				err = errors.New("official connections used different executions")
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return refs, errors.Join(failures...)
}

func mockReference(sessionDir string, session *lifecycle.Session, root, id string, pinned lifecycle.Artifact, previous map[string]bool) (tool.Object, error) {
	var process tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "peer-"+id+".process.json"), &process); err != nil {
		return nil, fmt.Errorf("protocol process not started or settled: %w", err)
	}
	f, err := os.Open(filepath.Join(root, "peer-"+id+".results.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	var started, finished tool.Object
	if err := decoder.Decode(&started); err != nil {
		return nil, err
	}
	ref := tool.Object{"started": started, "process": process}
	if err := decoder.Decode(&finished); err != nil {
		return ref, err
	}
	ref["finished"] = finished
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ref, errors.New("unexpected execution notifications")
	}
	executionID := tool.Str(started["executionId"])
	if executionID == "" || filepath.Base(executionID) != executionID || !filepath.IsLocal(executionID) {
		return ref, errors.New("invalid execution reference")
	}
	ref["executionId"], ref["connectionId"] = executionID, id
	dir := filepath.Join(sessionDir, "executions", executionID)
	for _, value := range []tool.Object{started, finished} {
		if value["processId"] != process["pid"] || value["sessionId"] != session.SessionID || value["connectionId"] != id || value["executionId"] != executionID || value["command"] != "mock" || value["execution"] != filepath.Join(dir, "execution.json") || value["result"] != filepath.Join(dir, "receipts", id+".result.json") {
			return ref, errors.New("execution notification reference mismatch")
		}
	}
	if err := validatePeerExit(started, finished, process); err != nil {
		return ref, err
	}
	var durable tool.Object
	if err := tool.ReadJSON(filepath.Join(dir, "receipts", id+".result.json"), &durable); err != nil {
		return ref, err
	}
	if !reflect.DeepEqual(finished, durable) {
		return ref, errors.New("notification differs from durable result")
	}
	var execution lifecycle.Execution
	if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
		return ref, err
	}
	if err := lifecycle.ValidateMockExecution(sessionDir, session, execution); err != nil {
		return ref, err
	}
	material := execution.Artifact
	if previous[executionID] || execution.Command != "mock" || !reflect.DeepEqual(material, pinned) {
		return ref, errors.New("mock does not belong to this verification and exact material")
	}
	ref["material"] = material
	ref["executionSHA256"], err = tool.FileDigest(filepath.Join(dir, "execution.json"))
	if err != nil {
		return ref, err
	}
	if started["executionSHA256"] != ref["executionSHA256"] || finished["executionSHA256"] != ref["executionSHA256"] {
		return ref, errors.New("mock descriptor hash differs from notifications")
	}
	for _, value := range []tool.Object{started, finished} {
		m := tool.Obj(value["material"])
		if m["id"] != material.ID || m["path"] != material.Path || m["planSHA256"] != material.PlanSHA256 || m["manifestSHA256"] != material.ManifestSHA256 || m["recordingSHA256"] != material.RecordingSHA256 {
			return ref, errors.New("mock notification material differs")
		}
	}
	for _, connection := range execution.Connections {
		if connection.ID != id {
			continue
		}
		if started["receipt"] != connection.Receipt || finished["receipt"] != connection.Receipt {
			return ref, errors.New("connection receipt reference mismatch")
		}
		if finished["processReceipt"] != connection.ProcessReceipt || started["processReceipt"] != connection.ProcessReceipt {
			return ref, errors.New("mock process receipt reference differs")
		}
		if err := lifecycle.ValidateMockConnection(session.AgentType, execution, connection); err != nil {
			return ref, err
		}
		hashes := tool.Object{}
		for _, path := range []string{connection.Receipt, connection.ProcessReceipt, filepath.Join(dir, "receipts", id+".result.json")} {
			hash, err := tool.FileDigest(path)
			if err != nil {
				return ref, err
			}
			hashes[path] = hash
		}
		ref["receiptHashes"] = hashes
		ref["connection"] = connection
		return ref, nil
	}
	return ref, errors.New("connection absent from execution")
}

// Snapshot exact durable evidence for archive revalidation, without rewriting
// receipts or requiring access to the original session after export/archive.
func snapshotMocks(root string, refs []tool.Object) error {
	for _, ref := range refs {
		connection, ok := ref["connection"].(lifecycle.ExecutionConnection)
		if !ok {
			continue
		}
		finished := tool.Obj(ref["finished"])
		names := map[string]string{
			"execution.json": tool.Str(finished["execution"]),
			"result.json":    tool.Str(finished["result"]),
			"replay.json":    connection.Receipt,
			"process.json":   connection.ProcessReceipt,
		}
		files := tool.Object{}
		for name, source := range names {
			relative := filepath.Join("driver", "connections", connection.ID, name)
			if err := tool.CopyFile(source, filepath.Join(root, relative)); err != nil {
				return err
			}
			hash, err := tool.FileDigest(filepath.Join(root, relative))
			if err != nil {
				return err
			}
			files[filepath.ToSlash(relative)] = hash
		}
		ref["evidenceFiles"] = files
	}
	return nil
}

func validatePeerExit(started, finished, process tool.Object) error {
	if started["status"] != "running" || finished["status"] != "passed" || process["started"] != true || process["error"] != nil || process["exitCode"] != float64(0) || process["reaped"] != true || process["cleanupComplete"] != true {
		return errors.New("protocol process failed or was not reaped")
	}
	return nil
}
