package build

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

func TestLibraryExecutionReportRejectsFailedResultWithoutDiagnostic(t *testing.T) {
	binary := buildCLI(t, false)
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	runCLI(t, binary, "prepare", "--agent", "codex", "--case", "SDK-CASE-001", "--fixture-dir", dir)
	fixture, err := agenttestify.Open(dir)
	require.NoError(t, err)
	input := writeBuildStream(t, root, "input", clientInput(t, fixture.Current.Path))
	output := writeBuildStream(t, root, "output", "")
	stderr := writeBuildStream(t, root, "stderr", "")
	result, err := fixture.MockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.Streams{In: input, Out: output, Err: stderr})
	require.NoError(t, err, readOpenBuildFile(t, stderr))
	require.NotNil(t, result)
	require.NotNil(t, result.Execution)
	require.Equal(t, agenttestify.ExecutionStatusPassed, result.Execution.Status)
	require.True(t, result.Execution.Complete)

	// Change only the result's declaration, retaining its valid identity and hash
	// bindings and the successful replay, process and execution index facts.
	mutateBuildJSON(t, result.ResultPath, func(value map[string]any) {
		require.NotContains(t, value, "error")
		require.NotContains(t, value, "failureClass")
		value["status"] = "failed"
	})
	paths := []string{
		result.ExecutionPath, result.ResultPath, result.ReceiptPath, result.ProcessReceiptPath,
		filepath.Join(dir, "executions", "index.json"), filepath.Join(dir, "current.json"),
		filepath.Join(fixture.Current.Path, "raw", "raw.jsonl"),
	}
	before := make(map[string][]byte, len(paths))
	for _, path := range paths {
		before[path] = mustRead(t, path)
	}
	reopened, err := agenttestify.Open(dir)
	require.NoError(t, err)
	report, err := reopened.ExecutionReport(result.ExecutionID)
	require.ErrorContains(t, err, "passed with a failed result")
	require.Nil(t, report)
	for _, path := range paths {
		require.Equal(t, before[path], mustRead(t, path), "reader must preserve %s", path)
	}
}

func TestLibraryProcessMockReportAndAdapterBoundaries(t *testing.T) {
	binary := buildCLI(t, false)
	root := t.TempDir()
	cliFixtureDir := filepath.Join(root, "cli-fixture")
	runCLI(t, binary, "prepare", "--agent", "codex", "--case", "SDK-CASE-001", "--fixture-dir", cliFixtureDir)
	executableFixture, err := agenttestify.Open(cliFixtureDir)
	require.NoError(t, err)
	require.Equal(t, agenttestify.ToolSourceExecutable, executableFixture.Tool.Source)

	cliIndexPath := filepath.Join(cliFixtureDir, "executions", "index.json")
	cliIndexBefore := mustRead(t, cliIndexPath)
	run, err := executableFixture.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: io.NopCloser(strings.NewReader("")),
		Writer: nopWriteCloser{Writer: io.Discard},
		Stderr: io.Discard,
	})
	require.Nil(t, run)
	require.ErrorContains(t, err, "in-process mock requires embedded fixture")
	require.Equal(t, cliIndexBefore, mustRead(t, cliIndexPath))

	input := writeBuildStream(t, root, "process-input", clientInput(t, executableFixture.Current.Path))
	output := writeBuildStream(t, root, "process-output", "")
	errorsOutput := writeBuildStream(t, root, "process-errors", "")
	processResult, err := executableFixture.MockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.Streams{In: input, Out: output, Err: errorsOutput})
	require.NoError(t, err, readOpenBuildFile(t, errorsOutput))
	require.NotNil(t, processResult)
	require.NotNil(t, processResult.Execution)
	require.True(t, processResult.ExecutionComplete)
	require.NotEmpty(t, processResult.ProcessReceiptPath)
	require.Len(t, processResult.Execution.Connections, 1)

	report, err := executableFixture.ExecutionReport(processResult.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, processResult.Execution, report)
	require.True(t, report.Complete)
	require.Len(t, report.Connections, 1)
	connection := report.Connections[0]
	require.Equal(t, agenttestify.ConnectionStatusPassed, connection.Status)
	require.Equal(t, agenttestify.MockTransportProcess, connection.Transport)
	require.False(t, connection.NativeLaunched)
	require.True(t, connection.CleanupComplete)
	require.NotNil(t, connection.ExitCode)
	require.Equal(t, 0, *connection.ExitCode)
	require.FileExists(t, connection.ProcessReceiptPath)
	require.Equal(t, connection.ProcessReceiptPath, processResult.ProcessReceiptPath)

	var processReceipt map[string]any
	require.NoError(t, readJSON(connection.ProcessReceiptPath, &processReceipt))
	require.EqualValues(t, 0, processReceipt["exitCode"])
	require.Equal(t, "passed", processReceipt["status"])
	mutateBuildJSON(t, connection.ProcessReceiptPath, func(value map[string]any) {
		value["exitCode"] = float64(7)
	})
	forged, err := executableFixture.ExecutionReport(processResult.ExecutionID)
	require.ErrorContains(t, err, "nonzero for a passed connection")
	require.Nil(t, forged)

	libraryFixtureDir := filepath.Join(root, "library-fixture")
	libraryFixture, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: libraryFixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	libraryIndexPath := filepath.Join(libraryFixtureDir, "executions", "index.json")
	libraryIndexBefore := mustRead(t, libraryIndexPath)
	embeddedInput := writeBuildStream(t, root, "embedded-input", libraryMockClientInput(t, libraryFixture.Current.Path))
	embeddedOutput := writeBuildStream(t, root, "embedded-output", "")
	embeddedErrors := writeBuildStream(t, root, "embedded-errors", "")
	legacy, err := libraryFixture.MockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.Streams{In: embeddedInput, Out: embeddedOutput, Err: embeddedErrors})
	require.Nil(t, legacy)
	require.ErrorContains(t, err, "process mock requires executable fixture")
	require.Equal(t, libraryIndexBefore, mustRead(t, libraryIndexPath))

	command := exec.CommandContext(t.Context(), binary, "agent", "mock",
		"--fixture-dir", libraryFixtureDir,
		"--", "app-server", "--listen", "stdio://")
	command.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	require.Error(t, err)
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "process mock requires executable fixture")
	require.Equal(t, libraryIndexBefore, mustRead(t, libraryIndexPath))
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}

func writeBuildStream(t *testing.T, root, name, value string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(root, name+"-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	_, err = file.WriteString(value)
	require.NoError(t, err)
	_, err = file.Seek(0, 0)
	require.NoError(t, err)
	return file
}

func readOpenBuildFile(t *testing.T, file *os.File) string {
	t.Helper()
	_, err := file.Seek(0, 0)
	require.NoError(t, err)
	data, err := io.ReadAll(file)
	require.NoError(t, err)
	return string(data)
}

func mutateBuildJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	var value map[string]any
	require.NoError(t, readJSON(path, &value))
	mutate(value)
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
}

func clientInput(t *testing.T, material string) string {
	t.Helper()
	return recordingClientInput(t, filepath.Join(material, "raw", "raw.jsonl"))
}

func libraryMockClientInput(t *testing.T, material string) string {
	t.Helper()
	return recordingClientInput(t, filepath.Join(material, "raw", "raw.jsonl"))
}

func recordingClientInput(t *testing.T, path string) string {
	t.Helper()
	data := mustRead(t, path)
	var value strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var frame struct {
			Direction string          `json:"direction"`
			Wire      json.RawMessage `json:"wire"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &frame))
		if frame.Direction == "c2s" {
			value.Write(frame.Wire)
			value.WriteByte('\n')
		}
	}
	require.NotEmpty(t, value.String())
	return value.String()
}
