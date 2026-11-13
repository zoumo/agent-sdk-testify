package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestMockEvidenceReadersRejectTampering(t *testing.T) {
	execution, resultPath := mockEvidenceFiles(t)
	connection := execution.Connections[0]
	require.NoError(t, ValidateMockConnection("codex", execution, connection))
	for _, tc := range []struct {
		name, file string
		mutate     func(tool.Object)
	}{
		{"replay missing schema", "replay", func(v tool.Object) { delete(v, "schemaVersion") }},
		{"replay wrong schema", "replay", func(v tool.Object) { v["schemaVersion"] = 1 }},
		{"replay wrong version", "replay", func(v tool.Object) { v["version"] = 2 }},
		{"replay failed status", "replay", func(v tool.Object) { v["status"] = "failed" }},
		{"replay schema type", "replay", func(v tool.Object) { v["schemaVersion"] = "1" }},
		{"replay boolean type", "replay", func(v tool.Object) { v["fullConsumption"] = "true" }},
		{"replay error", "replay", func(v tool.Object) { v["error"] = "synthetic failure" }},
		{"replay error type", "replay", func(v tool.Object) { v["error"] = false }},
		{"replay failure class", "replay", func(v tool.Object) { v["failureClass"] = "cleanup" }},
		{"replay failure class type", "replay", func(v tool.Object) { v["failureClass"] = 3 }},
		{"replay transport", "replay", func(v tool.Object) { v["transport"] = "in-process" }},
		{"replay transport type", "replay", func(v tool.Object) { v["transport"] = 42 }},
		{"replay provider", "replay", func(v tool.Object) { v["provider"] = "claude" }},
		{"replay recording path", "replay", func(v tool.Object) { v["recording"] = "/wrong" }},
		{"replay recording hash", "replay", func(v tool.Object) { v["recordingSHA256"] = strings.Repeat("0", 64) }},
		{"replay plan hash", "replay", func(v tool.Object) { v["planSHA256"] = strings.Repeat("0", 64) }},
		{"replay frame count", "replay", func(v tool.Object) { v["frames"] = 999 }},
		{"replay native launched", "replay", func(v tool.Object) { v["nativeLaunched"] = true }},
		{"replay incomplete", "replay", func(v tool.Object) { v["fullConsumption"] = false }},
		{"replay no EOF", "replay", func(v tool.Object) { v["clientEOF"] = false }},
		{"replay no cleanup", "replay", func(v tool.Object) { v["cleanupComplete"] = false }},
		{"replay exit", "replay", func(v tool.Object) { v["exitCode"] = 1 }},
		{"replay exit type", "replay", func(v tool.Object) { v["exitCode"] = "0" }},
		{"process missing schema", "process", func(v tool.Object) { delete(v, "schemaVersion") }},
		{"process schema", "process", func(v tool.Object) { v["schemaVersion"] = 2 }},
		{"process failed status", "process", func(v tool.Object) { v["status"] = "failed" }},
		{"process schema type", "process", func(v tool.Object) { v["schemaVersion"] = "1" }},
		{"process transport", "process", func(v tool.Object) { v["transport"] = "in-process" }},
		{"process transport type", "process", func(v tool.Object) { v["transport"] = false }},
		{"process error", "process", func(v tool.Object) { v["error"] = "synthetic failure" }},
		{"process error type", "process", func(v tool.Object) { v["error"] = false }},
		{"process fixture", "process", func(v tool.Object) { v["fixtureId"] = "wrong" }},
		{"process execution", "process", func(v tool.Object) { v["executionId"] = "wrong" }},
		{"process connection", "process", func(v tool.Object) { v["connectionId"] = "probe" }},
		{"process exit", "process", func(v tool.Object) { v["exitCode"] = 1 }},
		{"process exit type", "process", func(v tool.Object) { v["exitCode"] = "0" }},
		{"process fractional exit", "process", func(v tool.Object) { v["exitCode"] = 0.5 }},
		{"result missing schema", "result", func(v tool.Object) { delete(v, "schemaVersion") }},
		{"result wrong schema", "result", func(v tool.Object) { v["schemaVersion"] = 2 }},
		{"result error", "result", func(v tool.Object) { v["error"] = "synthetic failure" }},
		{"result error type", "result", func(v tool.Object) { v["error"] = false }},
		{"result transport", "result", func(v tool.Object) { v["transport"] = "in-process" }},
		{"result transport type", "result", func(v tool.Object) { v["transport"] = 42 }},
		{"result fixture", "result", func(v tool.Object) { v["fixtureId"] = "wrong" }},
		{"result execution", "result", func(v tool.Object) { v["executionId"] = "wrong" }},
		{"result connection", "result", func(v tool.Object) { v["connectionId"] = "probe" }},
		{"result execution hash", "result", func(v tool.Object) { v["executionSHA256"] = strings.Repeat("0", 64) }},
		{"result material hash", "result", func(v tool.Object) { tool.Obj(v["material"])["planSHA256"] = strings.Repeat("0", 64) }},
		{"result material identity", "result", func(v tool.Object) { tool.Obj(v["material"])["id"] = "wrong" }},
		{"result process ID type", "result", func(v tool.Object) { v["processId"] = "42" }},
		{"result process ID fraction", "result", func(v tool.Object) { v["processId"] = 0.5 }},
		{"result process receipt", "result", func(v tool.Object) { v["processReceipt"] = "/wrong" }},
		{"result command", "result", func(v tool.Object) { v["command"] = "record" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := connection.Receipt
			switch tc.file {
			case "process":
				path = connection.ProcessReceipt
			case "result":
				path = resultPath
			}
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.WriteFile(path, original, 0o600)) })
			var value tool.Object
			require.NoError(t, tool.ReadJSON(path, &value))
			tc.mutate(value)
			require.NoError(t, tool.WriteJSON(path, value))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			officialErr := ValidateMockConnection("codex", execution, connection)
			require.Error(t, officialErr, "official reader")
			facts, err := readMockConnectionEvidence("codex", execution, connection)
			if err == nil {
				err = facts.requirePassedReceipts()
			}
			require.EqualError(t, err, officialErr.Error(), "shared reader must reject the same corrupt fact")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestMockEvidenceReadersPreserveProcessCompatibility(t *testing.T) {
	execution, _ := mockEvidenceFiles(t)
	connection := execution.Connections[0]
	var replay, process tool.Object
	require.NoError(t, tool.ReadJSON(connection.Receipt, &replay))
	// Process receipts predate transport declarations. Legacy location fields are
	// informational and have never bound the process to the execution workspace.
	delete(replay, "transport")
	replay["exitCode"] = 0
	require.NoError(t, tool.WriteJSON(connection.Receipt, replay))
	require.NoError(t, tool.ReadJSON(connection.ProcessReceipt, &process))
	process["cwd"], process["workspace"] = "relative", 42
	require.NoError(t, tool.WriteJSON(connection.ProcessReceipt, process))
	require.NoError(t, ValidateMockConnection("codex", execution, connection))
	facts, err := readMockConnectionEvidence("codex", execution, connection)
	require.NoError(t, err)
	require.NoError(t, facts.requirePassedReceipts())
	// Reports retain compatibility with an absent optional replay exit code;
	// official verification still needs explicit successful process evidence.
	delete(replay, "exitCode")
	require.NoError(t, tool.WriteJSON(connection.Receipt, replay))
	facts, err = readMockConnectionEvidence("codex", execution, connection)
	require.NoError(t, err)
	require.NoError(t, facts.requirePassedReceipts())
	require.Nil(t, facts.replayExitCode)
	require.ErrorContains(t, ValidateMockConnection("codex", execution, connection), "exit")
}

func TestMockEvidenceExecutionFailureRequiresDiagnostic(t *testing.T) {
	execution, resultPath := mockEvidenceFiles(t)
	for _, tc := range []struct {
		name, status string
		diagnostic   any
		wantFailure  bool
	}{
		{"passed", "passed", nil, false},
		{"failed without error", "failed", nil, false},
		{"failed empty error", "failed", "", false},
		{"failed whitespace error", "failed", " \n\t", false},
		{"failed finalize", "failed", "route restore failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value tool.Object
			require.NoError(t, tool.ReadJSON(resultPath, &value))
			value["status"], value["error"] = tc.status, tc.diagnostic
			if tc.diagnostic == nil {
				delete(value, "error")
			}
			require.NoError(t, tool.WriteJSON(resultPath, value))
			facts, err := readMockConnectionEvidence("codex", execution, execution.Connections[0])
			require.NoError(t, err)
			require.NoError(t, facts.requirePassedReceipts())
			require.Equal(t, tc.wantFailure, facts.hasExecutionFailure())
			passedErr := validateExecutionStatus(ExecutionStatusPassed, map[string]string{"main": "passed"}, facts.resultStatus == "failed", facts.hasExecutionFailure())
			if tc.status == "failed" {
				require.ErrorContains(t, passedErr, "passed with a failed result")
			} else {
				require.NoError(t, passedErr)
			}
			err = validateExecutionStatus(ExecutionStatusFailed, map[string]string{"main": "passed"}, facts.resultStatus == "failed", facts.hasExecutionFailure())
			if tc.wantFailure {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "without a failed connection")
			}
		})
	}
}

func TestMockEvidenceReadersRejectUnreadableFiles(t *testing.T) {
	execution, resultPath := mockEvidenceFiles(t)
	connection := execution.Connections[0]
	for _, path := range []string{connection.Receipt, connection.ProcessReceipt, resultPath} {
		for _, mode := range []string{"missing", "malformed", "symlink"} {
			t.Run(filepath.Base(path)+"/"+mode, func(t *testing.T) {
				original, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.Remove(path))
				t.Cleanup(func() {
					if _, err := os.Lstat(path); err == nil {
						require.NoError(t, os.Remove(path))
					}
					require.NoError(t, os.WriteFile(path, original, 0o600))
				})
				switch mode {
				case "malformed":
					require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
				case "symlink":
					target := filepath.Join(t.TempDir(), "evidence.json")
					require.NoError(t, os.WriteFile(target, original, 0o600))
					require.NoError(t, os.Symlink(target, path))
				}
				officialErr := ValidateMockConnection("codex", execution, connection)
				require.Error(t, officialErr)
				_, err = readMockConnectionEvidence("codex", execution, connection)
				require.EqualError(t, err, officialErr.Error())
				switch mode {
				case "missing":
					require.NoFileExists(t, path)
				case "malformed":
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, "{", string(data))
				case "symlink":
					info, err := os.Lstat(path)
					require.NoError(t, err)
					require.NotZero(t, info.Mode()&os.ModeSymlink)
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, original, data)
				}
			})
		}
	}
}

// Only files consumed by the evidence reader are needed: no prepared fixture,
// route templates, environment overlays or transaction run belongs in this unit.
func mockEvidenceFiles(t *testing.T) (Execution, string) {
	t.Helper()
	root := t.TempDir()
	materialRoot := MaterialRoot(root, "material")
	require.NoError(t, os.MkdirAll(filepath.Join(materialRoot, "raw"), 0o700))
	planPath := filepath.Join(materialRoot, "plan.json")
	rawPath := filepath.Join(materialRoot, "raw", "raw.jsonl")
	require.NoError(t, os.WriteFile(planPath, []byte(`{"provider":"codex","provenance":"synthetic"}`), 0o600))
	require.NoError(t, os.WriteFile(rawPath, []byte(`{"ordinal":1,"direction":"c2s","channel":"protocol","epoch":1,"wire":{"id":1,"method":"ping","params":{"unknown":9007199254740993}}}
{"ordinal":2,"direction":"s2c","channel":"protocol","epoch":1,"wire":{"id":1,"result":{"unknown":9007199254740993}}}
`), 0o600))
	planHash, err := tool.FileDigest(planPath)
	require.NoError(t, err)
	rawHash, err := tool.FileDigest(rawPath)
	require.NoError(t, err)
	manifestPath := filepath.Join(materialRoot, "manifest.json")
	require.NoError(t, tool.WriteJSON(manifestPath, tool.Object{
		"provenance": "synthetic", "captureStatus": "complete", "planSHA256": planHash,
		"files": tool.Object{"raw.jsonl": rawHash},
	}))
	manifestHash, err := tool.FileDigest(manifestPath)
	require.NoError(t, err)
	dir := PackageExecutionRoot(root, "material", "execution")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "receipts"), 0o700))
	connection := ExecutionConnection{
		ID: "main", Transport: ConnectionTransportProcess, RecordingSHA256: rawHash,
		Receipt:        filepath.Join(dir, "receipts", "main.json"),
		ProcessReceipt: filepath.Join(dir, "receipts", "main.process.json"),
	}
	execution := Execution{
		SchemaVersion: SchemaVersion, FixtureID: "fixture", ExecutionID: "execution", Command: "mock", ArtifactRoot: filepath.Join(dir, "mock"),
		Connections: []ExecutionConnection{connection}, dir: root,
		Args: []string{"--fixture-dir", root}, Source: Source{Path: planPath},
		Artifact: corpus.Material{
			ID: "material", Path: materialRoot, Provenance: "synthetic", Connections: []string{"main"},
			PlanSHA256: planHash, ManifestSHA256: manifestHash, RecordingSHA256: rawHash,
		},
	}
	descriptorPath := filepath.Join(dir, "execution.json")
	require.NoError(t, tool.WriteJSON(descriptorPath, execution))
	descriptorHash, err := tool.FileDigest(descriptorPath)
	require.NoError(t, err)
	require.NoError(t, tool.WriteJSON(connection.Receipt, tool.Object{
		"schemaVersion": 2, "materialId": "material", "executionId": "execution", "connectionId": "main", "version": 1, "status": "passed", "provider": "codex", "exitCode": 0,
		"recording": "material", "recordingSHA256": rawHash, "planSHA256": planHash, "frames": 2,
		"nativeLaunched": false, "fullConsumption": true, "clientEOF": true, "cleanupComplete": true,
		"unknown": tool.Object{"retained": true},
	}))
	require.NoError(t, tool.WriteJSON(connection.ProcessReceipt, tool.Object{
		"schemaVersion": 1, "fixtureId": "fixture", "executionId": "execution", "connectionId": "main",
		"status": "passed", "exitCode": 0, "unknown": "retained",
	}))
	resultPath := filepath.Join(dir, "receipts", "main.result.json")
	require.NoError(t, tool.WriteJSON(resultPath, tool.Object{
		"schemaVersion": 3, "materialId": "material", "fixtureId": "fixture", "executionId": "execution", "connectionId": "main", "status": "passed",
		"execution": "executions/execution/execution.json", "executionSHA256": descriptorHash, "result": "executions/execution/receipts/main.result.json",
		"receipt": "executions/execution/receipts/main.json", "processReceipt": "executions/execution/receipts/main.process.json", "processId": 42, "command": "mock",
		"unknown": []string{"retained"},
		"material": tool.Object{
			"id": "material", "provenance": "synthetic",
			"planSHA256": planHash, "manifestSHA256": manifestHash, "recordingSHA256": rawHash,
		},
	}))
	return execution, resultPath
}
