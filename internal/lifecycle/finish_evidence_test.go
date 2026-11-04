package lifecycle

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// These are durable evidence inputs to the same validator used by finish and
// official verification. No session, Agent or protocol process is needed.
func TestMockReceiptEvidenceRejectsTampering(t *testing.T) {
	for _, tc := range []struct {
		name, message   string
		replay, process tool.Object
		missing         bool
	}{
		{name: "valid"},
		{name: "recording hash", replay: tool.Object{"recordingSHA256": strings.Repeat("0", 64)}, message: "recording differs"},
		{name: "frame count", replay: tool.Object{"frames": 999}, message: "frame count differs"},
		{name: "official wrong frames", replay: tool.Object{"frames": 99}, message: "frame count differs"},
		{name: "wrong provider", replay: tool.Object{"provider": "wrong"}, message: "strict full consumption"},
		{name: "native launched", replay: tool.Object{"nativeLaunched": true}, message: "strict full consumption"},
		{name: "receipt error", replay: tool.Object{"error": "synthetic protocol failure"}, message: "strict full consumption"},
		{name: "not consumed", replay: tool.Object{"fullConsumption": false}, message: "strict full consumption"},
		{name: "no EOF", replay: tool.Object{"clientEOF": false}, message: "strict full consumption"},
		{name: "no cleanup", replay: tool.Object{"cleanupComplete": false}, message: "strict full consumption"},
		{name: "missing receipt", missing: true, message: "no such file"},
		{name: "process cwd", process: tool.Object{"cwd": "/different/workspace"}, message: "process receipt is absent or unsuccessful"},
		{name: "process exit", process: tool.Object{"exitCode": 9}, message: "process exit is absent or nonzero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			receiptPath, processPath := filepath.Join(root, "replay.json"), filepath.Join(root, "process.json")
			execution := Execution{SessionID: "session", ExecutionID: "execution", Workspace: "/owned/workspace"}
			connection := ExecutionConnection{ID: "main"}
			expected := replayReceiptExpectation{Provider: "codex", RecordingSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64), Material: "/selected/material", Frames: 2, RequirePlan: true, RequireExitCode: true}
			receipt := tool.Object{"version": 1, "status": "passed", "provider": "codex", "fullConsumption": true, "clientEOF": true, "cleanupComplete": true, "nativeLaunched": false, "recordingSHA256": expected.RecordingSHA256, "planSHA256": expected.PlanSHA256, "recording": expected.Material, "frames": 2, "exitCode": 0}
			process := tool.Object{"schemaVersion": 1, "status": "passed", "sessionId": execution.SessionID, "executionId": execution.ExecutionID, "connectionId": "main", "cwd": execution.Workspace, "exitCode": 0}
			maps.Copy(receipt, tc.replay)
			maps.Copy(process, tc.process)
			if !tc.missing {
				require.NoError(t, tool.WriteJSON(receiptPath, receipt))
			}
			require.NoError(t, tool.WriteJSON(processPath, process))
			err := validateMockReceipts(receiptPath, processPath, execution, connection, expected)
			if tc.message == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.message)
			}
			// Validation must not rewrite or discard failed evidence.
			if !tc.missing {
				var after tool.Object
				require.NoError(t, tool.ReadJSON(receiptPath, &after))
				for k, v := range tc.replay {
					require.EqualValues(t, v, after[k])
				}
			}
			require.FileExists(t, processPath)
		})
	}
}

func TestExecutionInventoryRejectsUnindexedDirectoryWithoutDeletingEvidence(t *testing.T) {
	root := t.TempDir()
	evidence := filepath.Join(root, "executions", "orphan", "receipts", "main.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(evidence), 0o700))
	require.NoError(t, os.WriteFile(evidence, []byte("failed evidence\n"), 0o600))
	descriptor := filepath.Join(root, "executions", "orphan", "execution.json")
	require.NoError(t, tool.WriteJSON(descriptor, tool.Object{"executionId": "orphan"}))
	orphans, err := validateExecutionInventory(root, &Index{})
	require.ErrorContains(t, err, "absent from execution index")
	require.Equal(t, []string{"orphan"}, orphans)
	require.FileExists(t, descriptor)
	data, err := os.ReadFile(evidence)
	require.NoError(t, err)
	require.Equal(t, "failed evidence\n", string(data))
}
