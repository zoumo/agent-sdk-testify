package agenttestify_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

func TestLibraryStartMockAgentFullConsumptionAndReport(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	environment := append([]string(nil), os.Environ()...)
	slices.Sort(environment)
	fixtureDir := filepath.Join(root, "fixture")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)

	input := newControlledInput(libraryMockInput)
	output := &testWriteCloser{}
	var stderr bytes.Buffer
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: output,
		Stderr: &stderr,
	})
	require.NoError(t, err)
	require.NotNil(t, run)
	require.Equal(t, prepared.FixtureID, run.Start.FixtureID)
	require.Equal(t, "main", run.Start.ConnectionID)
	require.Equal(t, agenttestify.MockTransportInProcess, run.Start.Transport)
	require.Equal(t, *prepared.Current, run.Start.Material)
	require.NotEmpty(t, run.Start.ExecutionID)
	require.NotEmpty(t, run.Start.ExecutionSHA256)
	require.NotEqual(t, prepared.Routes["mock"].Workspace, run.Start.Workspace)
	require.NotEqual(t, prepared.Routes["mock"].ProtocolCwd, run.Start.ProtocolCwd)
	require.Equal(t, run.Start.Config, run.Start.Env["CODEX_HOME"])
	require.FileExists(t, run.Start.ExecutionPath)
	running := readLibraryJSON(t, filepath.Join(filepath.Dir(run.Start.ExecutionPath), "receipts", "main.result.json"))
	require.Equal(t, "running", running["status"])
	require.Equal(t, "in-process", running["transport"])
	require.NotContains(t, running, "processId")
	require.NotContains(t, running, "command")
	require.NotContains(t, running, "exitCode")

	select {
	case _, ok := <-run.FullConsumption():
		require.True(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive before client EOF")
	}
	require.Contains(t, output.String(), `"method":"turn/completed"`)
	select {
	case <-run.Done():
		t.Fatal("run completed before client EOF")
	default:
	}
	require.False(t, output.Closed(), "full consumption must not close the writer before client EOF")
	require.NoError(t, input.CloseEOF())

	result, err := run.Wait(t.Context())
	require.NoError(t, err, stderr.String())
	require.NotNil(t, result)
	require.Equal(t, agenttestify.ConnectionStatusPassed, result.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusPassed, result.ExecutionStatus)
	require.True(t, result.ExecutionComplete)
	require.Equal(t, "main", result.ConnectionID)
	require.Equal(t, "main", run.Start.ConnectionID)
	require.FileExists(t, run.Start.ExecutionPath)
	require.NotEmpty(t, run.Start.Env)
	select {
	case <-run.Done():
	default:
		t.Fatal("Done remained open after Wait returned")
	}

	require.NotNil(t, result.Execution)
	require.True(t, result.Execution.Complete)
	require.Equal(t, agenttestify.ExecutionStatusPassed, result.Execution.Status)
	require.Len(t, result.Execution.Connections, 1)
	connection := result.Execution.Connections[0]
	require.Equal(t, "main", connection.ID)
	require.Equal(t, agenttestify.ConnectionStatusPassed, connection.Status)
	require.Equal(t, agenttestify.MockTransportInProcess, connection.Transport)
	require.True(t, connection.FullConsumption)
	require.True(t, connection.ClientEOF)
	require.False(t, connection.NativeLaunched)
	require.True(t, connection.CleanupComplete)
	require.Nil(t, connection.ExitCode)
	require.Empty(t, connection.ProcessReceiptPath)
	require.Empty(t, result.ProcessReceiptPath)
	require.FileExists(t, connection.ReceiptPath)
	require.FileExists(t, connection.ResultPath)
	require.NoFileExists(t, filepath.Join(filepath.Dir(run.Start.ExecutionPath), "receipts", "main.process.json"))
	terminal := readLibraryJSON(t, connection.ResultPath)
	require.Equal(t, "passed", terminal["status"])
	require.Equal(t, "in-process", terminal["transport"])
	require.NotContains(t, terminal, "processId")
	require.NotContains(t, terminal, "command")
	require.NotContains(t, terminal, "processReceipt")
	require.True(t, input.Closed())
	require.True(t, output.Closed())

	require.Equal(t, cwd, workingDirectory(t))
	after := append([]string(nil), os.Environ()...)
	slices.Sort(after)
	require.Equal(t, environment, after)
}

func TestLibraryStartMockAgentPostSettleFailureReport(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input := newControlledInput(libraryMockInput)
	var stderr bytes.Buffer
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input, Writer: &testWriteCloser{}, Stderr: &stderr,
	})
	require.NoError(t, err)
	awaitFullConsumption(t, run)
	route := prepared.Routes["mock"].Workspace
	require.NoError(t, os.Remove(route))
	require.NoError(t, os.Mkdir(route, 0o700))
	require.NoError(t, input.CloseEOF())
	result, err := run.Wait(testContext(t))
	require.ErrorContains(t, err, "rename")
	require.NotNil(t, result)
	require.Equal(t, agenttestify.ExecutionStatusFailed, result.ExecutionStatus)
	require.True(t, result.ExecutionComplete)
	require.Equal(t, agenttestify.ConnectionStatusPassed, result.ConnectionStatus)
	require.NotNil(t, result.Execution)
	connection := result.Execution.Connections[0]
	require.True(t, connection.FullConsumption)
	require.True(t, connection.ClientEOF)
	require.True(t, connection.CleanupComplete)
	require.Nil(t, connection.ExitCode)
	require.Empty(t, connection.ProcessReceiptPath)
	require.Contains(t, connection.Error, "rename")
	require.Equal(t, agenttestify.FailureClassCleanup, connection.Failure)
	report, err := prepared.ExecutionReport(result.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, result.Execution, report)
}

func TestLibraryStartMockAgentFullConsumptionOrdering(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input, client := io.Pipe()
	t.Cleanup(func() { _ = client.CloseWithError(errors.New("test cleanup")) })
	output := &testWriteCloser{}
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: output,
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	require.Empty(t, output.String(), "protocol output must not precede the start snapshot")

	lines := strings.SplitAfter(libraryMockInput, "\n")
	lines = slices.DeleteFunc(lines, func(line string) bool { return line == "" })
	require.Len(t, lines, 5)
	prefixDone := make(chan error, 1)
	go func() {
		_, writeErr := client.Write([]byte(strings.Join(lines[:len(lines)-1], "")))
		prefixDone <- writeErr
	}()
	require.NoError(t, <-prefixDone)
	require.Eventually(t, func() bool {
		return strings.Contains(output.String(), `"method":"turn/completed"`)
	}, 5*time.Second, 10*time.Millisecond, "first scripted turn did not complete")
	select {
	case <-run.FullConsumption():
		t.Fatal("full consumption fired while a client request was still pending")
	default:
	}

	_, err = client.Write([]byte(lines[len(lines)-1]))
	require.NoError(t, err)
	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive after the pending request completed")
	}
	select {
	case <-run.Done():
		t.Fatal("run completed before client EOF")
	default:
	}
	require.NoError(t, client.Close())
	result, err := run.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, agenttestify.ConnectionStatusPassed, result.ConnectionStatus)
	require.True(t, result.Execution.Connections[0].FullConsumption)
	require.True(t, result.Execution.Connections[0].ClientEOF)
}

func TestLibraryStartMockAgentFullConsumptionPrecedesExtraByteFailure(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input := newControlledInput(libraryMockInput + "unexpected\n")
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive before the extra-byte check")
	}
	require.NoError(t, input.CloseEOF())
	result, err := run.Wait(t.Context())
	require.ErrorContains(t, err, "extra client byte after scenario")
	requireFailureClass(t, result, agenttestify.FailureClassStrictReplay)
	require.True(t, result.Execution.Connections[0].FullConsumption)
	require.False(t, result.Execution.Connections[0].ClientEOF)
}

func TestLibraryStartMockAgentHistoryDriftFailsConsistently(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: filepath.Join(root, "fixture"),
		Agent:      "claude",
		Case:       "SDK-CASE-016",
	})
	require.NoError(t, err)

	input := newControlledInput(clientFrames(t, filepath.Join(prepared.Current.Path, "raw", "raw.jsonl")))
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive before EOF")
	}

	source := readLibraryJSON(t, filepath.Join(prepared.Current.Path, "manifest.json"))["savedFiles"].(map[string]any)
	name := ""
	for candidate := range run.Start.HistoryFiles {
		if _, ok := source[candidate]; !ok {
			name = candidate
			break
		}
	}
	require.NotEmpty(t, name, "material has no derived history binding")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(run.Start.Workspace), name), []byte("changed\n"), 0o600))
	require.NoError(t, input.CloseEOF())

	result, err := run.Wait(t.Context())
	require.ErrorContains(t, err, "history hash mismatch")
	require.ErrorContains(t, err, "internal:")
	require.NotNil(t, result)
	require.Equal(t, agenttestify.ConnectionStatusFailed, result.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusFailed, result.ExecutionStatus)
	require.True(t, result.ExecutionComplete)
	requireFailureClass(t, result, agenttestify.FailureClassInternal)
	require.Equal(t, agenttestify.MockTransportInProcess, result.Execution.Connections[0].Transport)
	require.True(t, result.Execution.Connections[0].FullConsumption)
	require.True(t, result.Execution.Connections[0].ClientEOF)
	require.True(t, result.Execution.Connections[0].CleanupComplete)
	require.Nil(t, result.Execution.Connections[0].ExitCode)
	require.Empty(t, result.ProcessReceiptPath)
	require.NoFileExists(t, filepath.Join(filepath.Dir(run.Start.ExecutionPath), "receipts", "main.process.json"))

	receipt := readLibraryJSON(t, result.ReceiptPath)
	require.Equal(t, "failed", receipt["status"])
	require.Equal(t, true, receipt["fullConsumption"])
	require.Equal(t, true, receipt["clientEOF"])
	require.Equal(t, true, receipt["cleanupComplete"])
	require.Equal(t, "internal", receipt["failureClass"])
	require.Contains(t, receipt["error"], "history hash mismatch")
	action := readLibraryJSON(t, result.ResultPath)
	require.Equal(t, "failed", action["status"])
	require.Equal(t, "internal", action["failureClass"])
	require.Contains(t, action["error"], "history hash mismatch")

	report, err := prepared.ExecutionReport(result.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, agenttestify.ExecutionStatusFailed, report.Status)
	require.True(t, report.Complete)
	require.Len(t, report.Connections, 1)
	require.Equal(t, agenttestify.ConnectionStatusFailed, report.Connections[0].Status)
	require.Equal(t, agenttestify.FailureClassInternal, report.Connections[0].Failure)
	require.Equal(t, agenttestify.MockTransportInProcess, report.Connections[0].Transport)
	require.True(t, report.Connections[0].FullConsumption)
	require.True(t, report.Connections[0].ClientEOF)
	require.True(t, report.Connections[0].CleanupComplete)
	require.Equal(t, result.Execution, report)
}

func TestLibraryStartMockAgentRejectsPreflightFailures(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	fixtureDir := filepath.Join(root, "fixture")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	indexBefore := readFile(t, filepath.Join(fixtureDir, "executions", "index.json"))

	same := &testDuplex{}
	valid := agenttestify.StartMockAgentStreams{
		Reader: newControlledInput(""),
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	}
	for _, test := range []struct {
		name    string
		options agenttestify.MockAgentOptions
		streams agenttestify.StartMockAgentStreams
		want    string
	}{
		{
			name:    "missing reader",
			streams: agenttestify.StartMockAgentStreams{Writer: valid.Writer, Stderr: valid.Stderr},
			want:    "requires input, output and error streams",
		},
		{
			name:    "missing writer",
			streams: agenttestify.StartMockAgentStreams{Reader: valid.Reader, Stderr: valid.Stderr},
			want:    "requires input, output and error streams",
		},
		{
			name:    "missing stderr",
			streams: agenttestify.StartMockAgentStreams{Reader: valid.Reader, Writer: valid.Writer},
			want:    "requires input, output and error streams",
		},
		{
			name:    "same stream",
			streams: agenttestify.StartMockAgentStreams{Reader: same, Writer: same, Stderr: valid.Stderr},
			want:    "reader and writer must be separate",
		},
		{
			name:    "unknown explicit join",
			options: agenttestify.MockAgentOptions{ExecutionID: "explicit"},
			streams: valid,
			want:    "absent from index",
		},
		{
			name:    "undeclared connection",
			options: agenttestify.MockAgentOptions{Connection: "missing"},
			streams: valid,
			want:    "undeclared mock connection missing",
		},
		{
			name:    "unsupported native arguments",
			options: agenttestify.MockAgentOptions{NativeArgs: []string{"unsupported"}},
			streams: valid,
			want:    `unsupported native startup argument "unsupported"`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, err := prepared.StartMockAgent(t.Context(), test.options, test.streams)
			require.Nil(t, run)
			require.ErrorContains(t, err, test.want)
			require.Equal(t, indexBefore, readFile(t, filepath.Join(fixtureDir, "executions", "index.json")))
		})
	}

	emptyFixtureDir := filepath.Join(root, "empty-fixture")
	_, err = agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: emptyFixtureDir,
		Plan:       writeLibraryPlan(t, root),
	})
	require.NoError(t, err)
	empty, err := agenttestify.Open(emptyFixtureDir)
	require.NoError(t, err)
	run, err := empty.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, valid)
	require.Nil(t, run)
	require.ErrorContains(t, err, "fixture has no current recording")
}

func TestLibraryProcessMockRejectsExecutionID(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	indexBefore := readFile(t, filepath.Join(prepared.FixtureDir, "executions", "index.json"))
	result, err := prepared.MockAgent(t.Context(), agenttestify.MockAgentOptions{
		ExecutionID: "explicit",
	}, agenttestify.Streams{})
	require.Nil(t, result)
	require.ErrorContains(t, err, "explicit mock execution joins require StartMockAgent")
	require.Equal(t, indexBefore, readFile(t, filepath.Join(prepared.FixtureDir, "executions", "index.json")))
}

func TestLibraryStartMockAgentFailureClasses(t *testing.T) {
	t.Run("strict replay", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		input := newControlledInput("")
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: input,
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		require.NoError(t, input.CloseEOF())
		result, err := run.Wait(t.Context())
		require.ErrorContains(t, err, "early client EOF")
		require.ErrorContains(t, err, "strict-replay:")
		requireFailureClass(t, result, agenttestify.FailureClassStrictReplay)
		requireFullConsumptionAbsent(t, run)
	})

	t.Run("transport", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: &errorInput{err: errors.New("synthetic transport failure")},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		result, err := run.Wait(t.Context())
		require.ErrorContains(t, err, "synthetic transport failure")
		require.ErrorContains(t, err, "transport:")
		requireFailureClass(t, result, agenttestify.FailureClassTransport)
		requireFullConsumptionAbsent(t, run)
	})

	t.Run("short write", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: newControlledInput(libraryMockInput),
			Writer: &shortWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		result, err := run.Wait(t.Context())
		require.ErrorContains(t, err, "short write")
		require.ErrorContains(t, err, "transport:")
		requireFailureClass(t, result, agenttestify.FailureClassTransport)
		requireFullConsumptionAbsent(t, run)
	})

	t.Run("cleanup", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		input := newControlledInput(libraryMockInput)
		output := &testWriteCloser{closeErr: errors.New("synthetic close failure")}
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: input,
			Writer: output,
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		select {
		case <-run.FullConsumption():
		case <-time.After(5 * time.Second):
			t.Fatal("full consumption did not arrive")
		}
		require.NoError(t, input.CloseEOF())
		result, err := run.Wait(t.Context())
		require.ErrorContains(t, err, "synthetic close failure")
		require.ErrorContains(t, err, "cleanup:")
		requireFailureClass(t, result, agenttestify.FailureClassCleanup)
		require.NotNil(t, result.Execution)
		connection := result.Execution.Connections[0]
		require.True(t, connection.FullConsumption)
		require.True(t, connection.ClientEOF)
		require.False(t, connection.CleanupComplete)
	})

	t.Run("timeout", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
			Timeout: 20 * time.Millisecond,
		}, agenttestify.StartMockAgentStreams{
			Reader: &blockingInput{},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		result, err := run.Wait(t.Context())
		require.ErrorContains(t, err, "deadline exceeded")
		require.ErrorContains(t, err, "timeout:")
		requireFailureClass(t, result, agenttestify.FailureClassTimeout)
		requireFullConsumptionAbsent(t, run)
	})
}

func TestLibraryStartMockAgentReplayDeadlineOutranksClosedTransport(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: filepath.Join(root, "fixture"),
		Recording:  writeLibraryDeadlineMaterial(t, root),
	})
	require.NoError(t, err)

	input, sdkInput := io.Pipe()
	defer sdkInput.Close()
	outer, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	run, err := prepared.StartMockAgent(outer, agenttestify.MockAgentOptions{
		Timeout: 0,
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)

	result, err := run.Wait(outer)
	require.Error(t, err)
	require.NoError(t, outer.Err())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.True(t, strings.HasPrefix(err.Error(), "timeout:"), err.Error())
	require.NotNil(t, result)
	require.Equal(t, agenttestify.ConnectionStatusFailed, result.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusFailed, result.ExecutionStatus)
	requireFailureClass(t, result, agenttestify.FailureClassTimeout)
	require.Equal(t, agenttestify.MockTransportInProcess, result.Execution.Connections[0].Transport)
	require.True(t, result.Execution.Connections[0].FullConsumption)
	require.False(t, result.Execution.Connections[0].ClientEOF)
	require.True(t, result.Execution.Connections[0].CleanupComplete)
	require.Nil(t, result.Execution.Connections[0].ExitCode)
	require.Empty(t, result.ProcessReceiptPath)

	receipt := readLibraryJSON(t, result.ReceiptPath)
	require.Equal(t, "failed", receipt["status"])
	require.Equal(t, "timeout", receipt["failureClass"])
	require.Equal(t, true, receipt["fullConsumption"])
	require.Equal(t, false, receipt["clientEOF"])
	require.Equal(t, true, receipt["cleanupComplete"])
	require.NotContains(t, receipt, "exitCode")
	action := readLibraryJSON(t, result.ResultPath)
	require.Equal(t, "failed", action["status"])
	require.Equal(t, "timeout", action["failureClass"])

	report, err := prepared.ExecutionReport(result.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, agenttestify.ExecutionStatusFailed, report.Status)
	require.Equal(t, agenttestify.FailureClassTimeout, report.Connections[0].Failure)
}

func TestLibraryStartMockAgentAbortAndCancellation(t *testing.T) {
	t.Run("abort", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: &blockingInput{},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		require.NoError(t, run.Abort())
		require.NoError(t, run.Abort())
		result, err := run.Wait(t.Context())
		require.ErrorContains(t, err, "aborted:")
		requireFailureClass(t, result, agenttestify.FailureClassAborted)
		requireFullConsumptionAbsent(t, run)
	})

	t.Run("start context", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		ctx, cancel := context.WithCancel(t.Context())
		run, err := prepared.StartMockAgent(ctx, agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: &blockingInput{},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		cancel()
		select {
		case <-run.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("start cancellation did not finish the run")
		}
		result, err := run.Wait(t.Context())
		require.ErrorIs(t, err, context.Canceled)
		requireFailureClass(t, result, agenttestify.FailureClassCanceled)
	})

	t.Run("wait context", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: &blockingInput{},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result, err := run.Wait(ctx)
		require.ErrorIs(t, err, context.Canceled)
		requireFailureClass(t, result, agenttestify.FailureClassCanceled)
		requireFullConsumptionAbsent(t, run)
	})

	t.Run("canceled before claim", func(t *testing.T) {
		prepared := prepareStartMockFixture(t, "SDK-CASE-001")
		startLock, err := os.OpenFile(filepath.Join(prepared.FixtureDir, "locks", "mock-start.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		require.NoError(t, err)
		t.Cleanup(func() { _ = startLock.Close() })
		require.NoError(t, syscall.Flock(int(startLock.Fd()), syscall.LOCK_EX))

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		run, err := prepared.StartMockAgent(ctx, agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: &blockingInput{},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.Nil(t, run)
		require.ErrorIs(t, err, context.Canceled)

		require.NoError(t, syscall.Flock(int(startLock.Fd()), syscall.LOCK_UN))
		require.Eventually(t, func() bool {
			data, readErr := os.ReadFile(filepath.Join(prepared.FixtureDir, "executions", "index.json"))
			if readErr != nil {
				return false
			}
			var index struct {
				Executions []struct {
					Status string `json:"status"`
				} `json:"executions"`
			}
			if json.Unmarshal(data, &index) != nil {
				return false
			}
			return len(index.Executions) == 1 && index.Executions[0].Status == "failed"
		}, 5*time.Second, 10*time.Millisecond, "canceled pre-claim run did not finish")
	})
}

func TestLibraryStartMockAgentAbortDuringFinalizeFails(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input := newControlledInput(libraryMockInput)
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)

	executionDir := filepath.Dir(run.Start.ExecutionPath)
	finalizeLock, err := os.OpenFile(filepath.Join(executionDir, "mock-finalize.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	t.Cleanup(func() { _ = finalizeLock.Close() })
	require.NoError(t, syscall.Flock(int(finalizeLock.Fd()), syscall.LOCK_EX))

	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive")
	}
	require.NoError(t, input.CloseEOF())

	var execution map[string]any
	require.NoError(t, json.Unmarshal(readFile(t, run.Start.ExecutionPath), &execution))
	connections := execution["connections"].([]any)
	receiptPath := connections[0].(map[string]any)["receipt"].(string)
	require.Eventually(t, func() bool {
		data, readErr := os.ReadFile(receiptPath)
		if readErr != nil {
			return false
		}
		var receipt map[string]any
		if json.Unmarshal(data, &receipt) != nil {
			return false
		}
		return receipt["status"] == "passed" && receipt["clientEOF"] == true
	}, 5*time.Second, 10*time.Millisecond, "replay did not finish before finalize")

	require.NoError(t, run.Abort())
	require.NoError(t, syscall.Flock(int(finalizeLock.Fd()), syscall.LOCK_UN))

	result, err := run.Wait(t.Context())
	require.ErrorContains(t, err, "aborted:")
	require.NotNil(t, result)
	require.Equal(t, agenttestify.ConnectionStatusFailed, result.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusFailed, result.ExecutionStatus)
	terminal := readLibraryJSON(t, result.ResultPath)
	require.Equal(t, "failed", terminal["status"])
	require.Equal(t, "aborted", terminal["failureClass"])
}

func TestLibraryStartMockAgentWaitIsRepeatableAndConcurrent(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input := newControlledInput(libraryMockInput)
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive")
	}
	require.NoError(t, input.CloseEOF())

	type outcome struct {
		result *agenttestify.MockAgentResult
		err    error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			result, err := run.Wait(t.Context())
			done <- outcome{result: result, err: err}
		}()
	}
	first, second := <-done, <-done
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotNil(t, first.result)
	require.NotNil(t, second.result)
	require.Equal(t, first.result.ExecutionID, second.result.ExecutionID)
	require.NotSame(t, first.result, second.result)
	first.result.Material.Connections[0] = "mutated"
	require.NotEqual(t, "mutated", second.result.Material.Connections[0])

	repeated, err := run.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, first.result.ExecutionID, repeated.ExecutionID)
	require.Equal(t, first.result.Execution, repeated.Execution)
}

func TestLibraryStartMockAgentDoesNotJoinOpenExecution(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-032")
	require.Equal(t, []string{"main", "probe"}, prepared.Current.Connections)
	input := newControlledInput(clientFrames(t, filepath.Join(prepared.Current.Path, "raw", "raw.jsonl")))
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		Connection: "main",
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive")
	}
	require.NoError(t, input.CloseEOF())
	result, err := run.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, agenttestify.ExecutionStatusOpen, result.ExecutionStatus)

	indexBefore := readFile(t, filepath.Join(prepared.FixtureDir, "executions", "index.json"))
	second, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		Connection: "probe",
	}, agenttestify.StartMockAgentStreams{
		Reader: &blockingInput{},
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.Nil(t, second)
	require.ErrorContains(t, err, "fixture has unfinished execution")
	require.Equal(t, indexBefore, readFile(t, filepath.Join(prepared.FixtureDir, "executions", "index.json")))
}

func TestLibraryStartMockAgentExecutionStartIsDefensiveCopy(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input := newControlledInput(libraryMockInput)
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	run.Start.Env["CODEX_HOME"] = "mutated"
	run.Start.NativeArgs[0] = "mutated"
	run.Start.Material.Connections[0] = "mutated"
	var execution map[string]any
	require.NoError(t, json.Unmarshal(readFile(t, run.Start.ExecutionPath), &execution))
	env := execution["env"].(map[string]any)
	require.NotEqual(t, "mutated", env["CODEX_HOME"])
	nativeArgs := execution["nativeArgs"].([]any)
	require.Equal(t, "app-server", nativeArgs[0])
	connections := execution["artifact"].(map[string]any)["connections"].([]any)
	require.Equal(t, "main", connections[0])

	select {
	case <-run.FullConsumption():
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive")
	}
	require.NoError(t, input.CloseEOF())
	_, err = run.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, "main", run.Start.ConnectionID)
	require.NotEmpty(t, run.Start.Env)
}

func prepareStartMockFixture(t *testing.T, caseID string) *agenttestify.Fixture {
	t.Helper()
	root := t.TempDir()
	isolateLibraryTest(t, root)
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: filepath.Join(root, "fixture"),
		Agent:      "codex",
		Case:       caseID,
	})
	require.NoError(t, err)
	return prepared
}

func writeLibraryDeadlineMaterial(t *testing.T, root string) string {
	t.Helper()
	material := filepath.Join(root, "material")
	require.NoError(t, os.MkdirAll(filepath.Join(material, "raw"), 0o700))
	planPath := writeLibraryPlan(t, material)
	raw := []byte(`{"version":1,"provenance":"synthetic","timeoutMs":40,"frames":[{"ordinal":1,"direction":"s2c","epoch":1,"channel":"protocol","wire":{}}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(material, "raw", "raw.jsonl"), raw, 0o600))

	plan, err := os.ReadFile(planPath)
	require.NoError(t, err)
	digest := func(value []byte) string {
		sum := sha256.Sum256(value)
		return hex.EncodeToString(sum[:])
	}
	manifest, err := json.Marshal(map[string]any{
		"provenance": "consumer",
		"planSHA256": digest(plan),
		"files":      map[string]string{"raw.jsonl": digest(raw)},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(material, "manifest.json"), manifest, 0o600))
	return material
}

func requireFailureClass(t *testing.T, result *agenttestify.MockAgentResult, want agenttestify.FailureClass) {
	t.Helper()
	require.NotNil(t, result)
	require.NotNil(t, result.Execution)
	require.Len(t, result.Execution.Connections, 1)
	require.Equal(t, want, result.Execution.Connections[0].Failure)
}

func requireFullConsumptionAbsent(t *testing.T, run *agenttestify.MockRun) {
	t.Helper()
	select {
	case <-run.Done():
	default:
		t.Fatal("done was still open")
	}
	select {
	case <-run.FullConsumption():
		t.Fatal("full consumption fired on a pre-completion failure")
	default:
	}
}

type controlledInput struct {
	mu     sync.Mutex
	data   *bytes.Reader
	eof    chan struct{}
	closed bool
}

func newControlledInput(value string) *controlledInput {
	return &controlledInput{
		data: bytes.NewReader([]byte(value)),
		eof:  make(chan struct{}),
	}
}

func (r *controlledInput) Read(buffer []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if r.data.Len() > 0 {
		n, err := r.data.Read(buffer)
		r.mu.Unlock()
		return n, err
	}
	r.mu.Unlock()
	<-r.eof
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return 0, io.ErrClosedPipe
	}
	return 0, io.EOF
}

func (r *controlledInput) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	select {
	case <-r.eof:
	default:
		close(r.eof)
	}
	return nil
}

func (r *controlledInput) CloseEOF() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.eof:
	default:
		close(r.eof)
	}
	return nil
}

func (r *controlledInput) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

type testWriteCloser struct {
	mu       sync.Mutex
	content  bytes.Buffer
	closed   bool
	closeErr error
}

func (w *testWriteCloser) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	return w.content.Write(value)
}

func (w *testWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeErr
}

func (w *testWriteCloser) CloseWithError(_ error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return w.closeErr
}

func (w *testWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.content.String()
}

func (w *testWriteCloser) Closed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

type blockingInput struct {
	mu     sync.Mutex
	closed chan struct{}
	did    bool
}

func (r *blockingInput) Read([]byte) (int, error) {
	<-r.channel()
	return 0, errors.New("blocking input closed")
}

func (r *blockingInput) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	channel := r.channelLocked()
	if !r.did {
		r.did = true
		close(channel)
	}
	return nil
}

func (r *blockingInput) channel() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.channelLocked()
}

func (r *blockingInput) channelLocked() chan struct{} {
	if r.closed == nil {
		r.closed = make(chan struct{})
	}
	return r.closed
}

type errorInput struct {
	err error
}

func (r *errorInput) Read([]byte) (int, error) {
	return 0, r.err
}

func (r *errorInput) Close() error {
	return nil
}

type shortWriteCloser struct{}

func (w *shortWriteCloser) Write(value []byte) (int, error) {
	if len(value) == 0 {
		return 0, nil
	}
	return len(value) - 1, nil
}

func (w *shortWriteCloser) Close() error {
	return nil
}

type testDuplex struct{}

func (d *testDuplex) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (d *testDuplex) Write(value []byte) (int, error) {
	return len(value), nil
}

func (d *testDuplex) Close() error {
	return nil
}
