package lifecycle_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// The consumption notification holds replay before client EOF, so faults are
// installed before RunMock settles a genuinely successful replay connection.
func TestMockReportPreservesFinalizeFailure(t *testing.T) {
	for _, fault := range []string{"route", "authentication", "finalize lock"} {
		t.Run(fault, func(t *testing.T) { checkMockFinalizeFailure(t, fault) })
	}
}

func checkMockFinalizeFailure(t *testing.T, fault string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, "personal-config"))
	dir := filepath.Join(root, "fixture")
	fixture, err := lifecycle.Prepare(lifecycle.PrepareOptions{FixtureDir: dir, Agent: "codex", Case: "SDK-CASE-001"})
	require.NoError(t, err)
	material, err := lifecycle.LatestArtifact(dir, fixture)
	require.NoError(t, err)
	input, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = input.Close(); _ = writer.Close() })
	require.NoError(t, writer.SetWriteDeadline(time.Now().Add(5*time.Second)))
	progress, notification, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = progress.Close(); _ = notification.Close() })
	require.NoError(t, progress.SetReadDeadline(time.Now().Add(5*time.Second)))
	output, err := os.CreateTemp(root, "output")
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })
	source, err := os.Open(filepath.Join(material.Path, "raw", "raw.jsonl"))
	require.NoError(t, err)
	document, err := recording.Read(source, "codex", recording.Limits{})
	require.NoError(t, err)
	require.NoError(t, source.Close())
	type outcome struct {
		result *lifecycle.MockResult
		err    error
	}
	done := make(chan outcome, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() {
		result, runErr := mock.Run(ctx, mock.Prepared{
			FixtureDir: dir, Connection: "main", Agent: "codex", Artifact: *material,
			NativeArgs: []string{"app-server", "--listen", "stdio://"}, Timeout: 5 * time.Second,
			ProgressFD: int(notification.Fd()),
		}, input, output)
		done <- outcome{result, runErr}
	}()
	for _, frame := range document.Frames {
		if frame.Direction == recording.ClientToServer {
			_, err = writer.WriteString(string(frame.Wire.Raw()) + "\n")
			require.NoError(t, err)
		}
	}
	var marker [1]byte
	_, err = progress.Read(marker[:])
	require.NoError(t, err)
	require.Equal(t, byte('1'), marker[0])
	var index lifecycle.Index
	require.NoError(t, tool.ReadJSON(lifecycle.IndexPath(dir), &index))
	require.Len(t, index.Executions, 1)
	executionDir := lifecycle.ExecutionDir(dir, index.Executions[0].ID)
	execution, err := lifecycle.ReadExecution(dir, fixture, filepath.Join(executionDir, "execution.json"))
	require.NoError(t, err)
	wantStatus := lifecycle.ConnectionStatusPassed
	wantError := ""
	switch fault {
	case "route":
		route := filepath.Join(fixture.ResourceRoot, "routes", "mock", "workspace")
		require.NoError(t, os.Remove(route))
		require.NoError(t, os.Mkdir(route, 0o700))
		wantError = "rename"
	case "authentication":
		// A loop fails removal even when tests run as root. It affects only
		// authentication cleanup; this recording has no seeded history.
		savedConfig := execution.Config + ".saved"
		require.NoError(t, os.Rename(execution.Config, savedConfig))
		require.NoError(t, os.Symlink(filepath.Base(execution.Config), execution.Config))
		t.Cleanup(func() {
			require.NoError(t, os.Remove(execution.Config))
			require.NoError(t, os.Rename(savedConfig, execution.Config))
		})
		wantError = "too many levels of symbolic links"
	case "finalize lock":
		require.NoError(t, os.Mkdir(filepath.Join(lifecycle.ExecutionOverlayRoot(dir, execution.ExecutionID), "mock-finalize.lock"), 0o700))
		wantStatus = lifecycle.ConnectionStatusFailed
		wantError = "is a directory"
	}
	require.NoError(t, writer.Close())
	var completed outcome
	select {
	case completed = <-done:
	case <-ctx.Done():
		t.Fatal("mock did not finish after EOF")
	}
	require.ErrorContains(t, completed.err, wantError)
	require.NotNil(t, completed.result)
	result := completed.result
	var replay, process, durable tool.Object
	require.NoError(t, tool.ReadJSON(result.ReceiptPath, &replay))
	require.NoError(t, tool.ReadJSON(result.ProcessReceiptPath, &process))
	require.NoError(t, tool.ReadJSON(result.ResultPath, &durable))
	require.Equal(t, "passed", replay["status"])
	require.Equal(t, "passed", process["status"])
	require.Equal(t, "failed", durable["status"])
	paths := []string{
		result.ExecutionPath, result.ReceiptPath, result.ProcessReceiptPath, result.ResultPath,
		lifecycle.IndexPath(dir), filepath.Join(dir, "current.json"), filepath.Join(material.Path, "raw", "raw.jsonl"),
		filepath.Join(executionDir, "receipts", "main.failure.json"),
	}
	before := evidenceBytes(t, paths)
	require.NotNil(t, result.Execution)
	require.Equal(t, lifecycle.ExecutionStatusFailed, result.ExecutionStatus)
	require.Equal(t, wantStatus, result.ConnectionStatus)
	report, err := lifecycle.ReadMockExecutionReport(dir, result.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, lifecycle.ExecutionStatusFailed, report.Status)
	require.True(t, report.Complete)
	require.Equal(t, wantStatus, report.Connections[0].Status)
	require.True(t, report.Connections[0].FullConsumption)
	require.True(t, report.Connections[0].ClientEOF)
	require.True(t, report.Connections[0].CleanupComplete)
	require.Equal(t, 0, *report.Connections[0].ExitCode)
	require.Contains(t, report.Connections[0].Error, wantError)
	if wantStatus == lifecycle.ConnectionStatusPassed {
		require.Equal(t, lifecycle.FailureClassCleanup, report.Connections[0].Failure)
		require.Equal(t, "cleanup", durable["failureClass"])
		require.Equal(t, lifecycle.FailureClassCleanup, lifecycle.FailureClassOf(completed.err))
	}
	require.Error(t, lifecycle.ValidateMockConnection("codex", *execution, execution.Connections[0]))
	require.Error(t, lifecycle.ValidateMockExecution(dir, fixture, *execution))
	require.Equal(t, before, evidenceBytes(t, paths), "readers must preserve all evidence bytes")
	if fault == "route" {
		// Representative corrupt facts reach both adapters through their real
		// entrypoints. Field/type/hash combinations stay in reader unit tests.
		for _, file := range []string{"replay schema", "process error"} {
			t.Run(file, func(t *testing.T) {
				path := result.ReceiptPath
				if file == "process error" {
					path = result.ProcessReceiptPath
				}
				var value tool.Object
				require.NoError(t, tool.ReadJSON(path, &value))
				if file == "replay schema" {
					delete(value, "schemaVersion")
				} else {
					value["error"] = "synthetic contradictory success"
				}
				require.NoError(t, tool.WriteJSON(path, value))
				t.Cleanup(func() { require.NoError(t, os.WriteFile(path, before[path], 0o600)) })
				corrupt := evidenceBytes(t, paths)
				officialErr := lifecycle.ValidateMockConnection("codex", *execution, execution.Connections[0])
				require.Error(t, officialErr)
				rejected, readErr := lifecycle.ReadMockExecutionReport(dir, result.ExecutionID)
				require.EqualError(t, readErr, officialErr.Error())
				require.Nil(t, rejected)
				require.Equal(t, corrupt, evidenceBytes(t, paths))
			})
		}
	}

	if wantStatus == lifecycle.ConnectionStatusPassed {
		// The failed index must not be enough: a failed result without its
		// diagnostic, or a result bound to another descriptor, proves nothing.
		for _, mutation := range []string{"no diagnostic", "wrong descriptor", "passed result"} {
			t.Run(mutation, func(t *testing.T) {
				var value tool.Object
				require.NoError(t, tool.ReadJSON(result.ResultPath, &value))
				switch mutation {
				case "no diagnostic":
					delete(value, "error")
				case "wrong descriptor":
					value["executionSHA256"] = "wrong"
				case "passed result":
					value["status"] = "passed"
					delete(value, "error")
					delete(value, "failureClass")
				}
				require.NoError(t, tool.WriteJSON(result.ResultPath, value))
				t.Cleanup(func() { require.NoError(t, os.WriteFile(result.ResultPath, before[result.ResultPath], 0o600)) })
				rejected, readErr := lifecycle.ReadMockExecutionReport(dir, result.ExecutionID)
				require.Error(t, readErr)
				require.Nil(t, rejected)
			})
		}
	}
}

func evidenceBytes(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	values := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		values[path] = data
	}
	return values
}
