package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestMockEvidenceReadersRejectTampering(t *testing.T) {
	dir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	result, err := RunMock(t.Context(), mockRequest(t, dir, "main", 0), mockPolicyFunc(func(context.Context, MockContext) error { return nil }))
	require.NoError(t, err)
	execution := readRecordExecution(t, dir, result.ExecutionID)
	connection := execution.Connections[0]
	var replay tool.Object
	require.NoError(t, tool.ReadJSON(connection.Receipt, &replay))
	replay["exitCode"] = 0
	require.NoError(t, tool.WriteJSON(connection.Receipt, replay))
	require.NoError(t, ValidateMockConnection("codex", execution, connection))
	for _, tc := range []struct {
		name, file string
		mutate     func(tool.Object)
	}{
		{"replay missing schema", "replay", func(v tool.Object) { delete(v, "schemaVersion") }},
		{"replay wrong schema", "replay", func(v tool.Object) { v["schemaVersion"] = 2 }},
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
				path = result.ResultPath
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
			_, err = ReadMockExecutionReport(dir, result.ExecutionID)
			require.EqualError(t, err, officialErr.Error(), "report reader must reject the same corrupt fact")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestMockEvidenceReadersPreserveProcessCompatibility(t *testing.T) {
	dir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	result, err := RunMock(t.Context(), mockRequest(t, dir, "main", 0), mockPolicyFunc(func(context.Context, MockContext) error { return nil }))
	require.NoError(t, err)
	execution := readRecordExecution(t, dir, result.ExecutionID)
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
	_, err = ReadMockExecutionReport(dir, result.ExecutionID)
	require.NoError(t, err)
	// Reports retain compatibility with an absent optional replay exit code;
	// official verification still needs explicit successful process evidence.
	delete(replay, "exitCode")
	require.NoError(t, tool.WriteJSON(connection.Receipt, replay))
	_, err = ReadMockExecutionReport(dir, result.ExecutionID)
	require.NoError(t, err)
	require.ErrorContains(t, ValidateMockConnection("codex", execution, connection), "exit")
}

func TestMockEvidenceReportPreservesFinalizeFailure(t *testing.T) {
	dir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	result, err := RunMock(t.Context(), mockRequest(t, dir, "main", 0), mockPolicyFunc(func(_ context.Context, run MockContext) error {
		return os.Mkdir(filepath.Join(run.ExecutionDir(), "mock-finalize.lock"), 0o700)
	}))
	require.Error(t, err)
	report, err := ReadMockExecutionReport(dir, result.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusFailed, report.Status)
	require.True(t, report.Complete)
	require.Equal(t, ConnectionStatusFailed, report.Connections[0].Status)
	require.True(t, report.Connections[0].FullConsumption)
	require.True(t, report.Connections[0].ClientEOF)
	require.True(t, report.Connections[0].CleanupComplete)
	require.Equal(t, 0, *report.Connections[0].ExitCode)
	require.Contains(t, report.Connections[0].Error, "is a directory")
	execution := readRecordExecution(t, dir, result.ExecutionID)
	require.Error(t, ValidateMockConnection("codex", execution, execution.Connections[0]))
	fixture, err := OpenFixture(dir)
	require.NoError(t, err)
	require.Error(t, ValidateMockExecution(dir, fixture, execution))
}

func TestMockEvidenceReadersRejectUnreadableFiles(t *testing.T) {
	dir := prepareMockTransactionFixture(t, "SDK-CASE-001")
	result, err := RunMock(t.Context(), mockRequest(t, dir, "main", 0), mockPolicyFunc(func(context.Context, MockContext) error { return nil }))
	require.NoError(t, err)
	execution := readRecordExecution(t, dir, result.ExecutionID)
	connection := execution.Connections[0]
	for _, path := range []string{connection.Receipt, connection.ProcessReceipt, result.ResultPath} {
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
				_, err = ReadMockExecutionReport(dir, result.ExecutionID)
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
