package official

import (
	"errors"
	"os"
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

// writeMockIsolation records the current verification's actual mock execution.
// Recording-time isolation remains in the sealed material record and is never
// substituted by these verification-time facts.
func writeMockIsolation(fixtureDir string, fixture *lifecycle.Fixture, root string, material corpus.Material, refs []tool.Object) error {
	if len(refs) == 0 {
		return errors.New("verification has no mock execution")
	}
	executionID := tool.Str(refs[0]["executionId"])
	if executionID == "" {
		return errors.New("verification mock execution has no identity")
	}
	execution, err := lifecycle.ReadExecution(fixtureDir, fixture, filepath.Join(lifecycle.PackageExecutionRoot(fixtureDir, material.ID, executionID), "execution.json"))
	if err != nil {
		return err
	}
	if execution.Command != "mock" || !reflect.DeepEqual(execution.Artifact, material) {
		return errors.New("verification isolation belongs to another execution or material")
	}
	executionPath := filepath.Join(lifecycle.PackageExecutionRoot(fixtureDir, material.ID, executionID), "execution.json")
	executionReference, err := lifecycle.PackageReference(fixtureDir, material.ID, executionPath)
	if err != nil {
		return err
	}
	executionDigest, err := tool.FileDigest(executionPath)
	if err != nil {
		return err
	}
	env := tool.Object{}
	for _, name := range []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		if value, ok := execution.RuntimeEnv[name]; ok {
			env[name] = value
		}
	}
	configuration := tool.Object{}
	for _, name := range []string{"config.toml", "settings.json"} {
		hash, err := tool.FileDigest(filepath.Join(execution.Config, name))
		if err == nil {
			configuration[name] = hash
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	connections := []any{}
	for _, ref := range refs {
		connection := tool.Object{}
		for _, name := range []string{"connectionId", "executionId", "executionSHA256"} {
			if value, ok := ref[name]; ok {
				connection[name] = value
			}
		}
		connections = append(connections, connection)
	}
	isolation := tool.Object{
		"schemaVersion": 3,
		"fixtureId":     fixture.FixtureID,
		"executionId":   executionID,
		"execution":     tool.Object{"path": executionReference, "sha256": executionDigest},
		"materialId":    material.ID,
		"command":       execution.Command,
		"nativeArgs":    execution.NativeArgs,
		"workspace":     execution.Workspace,
		"protocolCwd":   execution.ProtocolCwd,
		"home":          execution.Home,
		"config":        execution.Config,
		"tmp":           execution.Tmp,
		"environment":   env,
		"configuration": configuration,
		"connections":   connections,
	}
	return tool.WriteJSON(filepath.Join(root, "mock-isolation.json"), isolation)
}
