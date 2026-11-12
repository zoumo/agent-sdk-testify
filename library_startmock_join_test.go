package agenttestify_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

func TestLibraryStartMockAgentExplicitJoinCoversAllConnections(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-032")
	args := []string{"app-server", "--listen", "stdio://"}
	const timeout = 10 * time.Second

	mainInput := newControlledInput(clientFrames(t, filepath.Join(prepared.Current.Path, "raw", "raw.jsonl")))
	mainRun, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		Connection: "main",
		Timeout:    timeout,
		NativeArgs: args,
	}, agenttestify.StartMockAgentStreams{
		Reader: mainInput,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	require.NotNil(t, mainRun)
	cleanupMockRun(t, mainRun)
	executionID := mainRun.Start.ExecutionID
	require.NotEmpty(t, executionID)

	pending, err := prepared.ExecutionReport(executionID)
	require.NoError(t, err)
	require.Equal(t, agenttestify.ExecutionStatusOpen, pending.Status)
	require.False(t, pending.Complete)
	require.Len(t, pending.Connections, 2)
	require.Equal(t, "main", pending.Connections[0].ID)
	require.Equal(t, agenttestify.ConnectionStatusRunning, pending.Connections[0].Status)
	require.Equal(t, "probe", pending.Connections[1].ID)
	require.Equal(t, agenttestify.ConnectionStatusPending, pending.Connections[1].Status)
	require.NoFileExists(t, pending.Connections[1].ResultPath)

	probeInput := newControlledInput(clientFrames(t, filepath.Join(prepared.Current.Path, "remote-probe", "raw", "raw.jsonl")))
	probeRun, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		Connection:  "probe",
		ExecutionID: executionID,
		Timeout:     timeout,
		NativeArgs:  args,
	}, agenttestify.StartMockAgentStreams{
		Reader: probeInput,
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	require.NotNil(t, probeRun)
	cleanupMockRun(t, probeRun)
	require.Equal(t, executionID, probeRun.Start.ExecutionID)
	require.Equal(t, "probe", probeRun.Start.ConnectionID)

	running, err := prepared.ExecutionReport(executionID)
	require.NoError(t, err)
	require.Equal(t, agenttestify.ExecutionStatusOpen, running.Status)
	require.Len(t, running.Connections, 2)
	for _, connection := range running.Connections {
		require.Equal(t, agenttestify.ConnectionStatusRunning, connection.Status)
		require.Equal(t, agenttestify.MockTransportInProcess, connection.Transport)
		require.False(t, connection.NativeLaunched)
		require.Nil(t, connection.ExitCode)
		require.Empty(t, connection.ProcessReceiptPath)
	}

	awaitFullConsumption(t, mainRun)
	awaitFullConsumption(t, probeRun)
	require.NoError(t, mainInput.CloseEOF())
	mainResult, err := mainRun.Wait(testContext(t))
	require.NoError(t, err)
	require.False(t, mainResult.ExecutionComplete)
	require.Equal(t, agenttestify.ExecutionStatusOpen, mainResult.ExecutionStatus)
	requireConnectionStatus(t, mainResult.Execution, "main", agenttestify.ConnectionStatusPassed)
	requireConnectionStatus(t, mainResult.Execution, "probe", agenttestify.ConnectionStatusRunning)

	require.NoError(t, probeInput.CloseEOF())
	probeResult, err := probeRun.Wait(testContext(t))
	require.NoError(t, err)
	require.True(t, probeResult.ExecutionComplete)
	require.Equal(t, agenttestify.ExecutionStatusPassed, probeResult.ExecutionStatus)
	requireConnectionStatus(t, probeResult.Execution, "main", agenttestify.ConnectionStatusPassed)
	requireConnectionStatus(t, probeResult.Execution, "probe", agenttestify.ConnectionStatusPassed)

	final, err := prepared.ExecutionReport(executionID)
	require.NoError(t, err)
	require.Equal(t, probeResult.Execution, final)
	require.True(t, final.Complete)
	for _, connection := range final.Connections {
		require.Equal(t, agenttestify.MockTransportInProcess, connection.Transport)
		require.True(t, connection.FullConsumption)
		require.True(t, connection.ClientEOF)
		require.False(t, connection.NativeLaunched)
		require.True(t, connection.CleanupComplete)
		require.Nil(t, connection.ExitCode)
		require.Empty(t, connection.ProcessReceiptPath)
		require.FileExists(t, connection.ReceiptPath)
		require.FileExists(t, connection.ResultPath)
	}

	final.Connections[0].ID = "mutated"
	final.Material.Connections[0] = "mutated"
	again, err := prepared.ExecutionReport(executionID)
	require.NoError(t, err)
	require.Equal(t, "main", again.Connections[0].ID)
	require.Equal(t, "main", again.Material.Connections[0])
}

func TestLibraryStartMockAgentJoinFailuresDoNotCreateOrAdvanceExecution(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-032")
	args := []string{"app-server", "--listen", "stdio://"}
	const timeout = 10 * time.Second

	mainRun, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		Connection: "main",
		Timeout:    timeout,
		NativeArgs: args,
	}, agenttestify.StartMockAgentStreams{
		Reader: &blockingInput{},
		Writer: &testWriteCloser{},
		Stderr: &bytes.Buffer{},
	})
	require.NoError(t, err)
	require.NotNil(t, mainRun)
	cleanupMockRun(t, mainRun)
	executionID := mainRun.Start.ExecutionID
	indexPath := filepath.Join(prepared.FixtureDir, "executions", "index.json")
	indexBefore := readFile(t, indexPath)
	executionsBefore := executionDirectoryCount(t, prepared.FixtureDir)

	for _, test := range []struct {
		name    string
		options agenttestify.MockAgentOptions
		want    string
	}{
		{
			name: "empty ID stays busy",
			options: agenttestify.MockAgentOptions{
				Connection: "probe",
				Timeout:    timeout,
				NativeArgs: args,
			},
			want: "unfinished execution",
		},
		{
			name: "unknown execution",
			options: agenttestify.MockAgentOptions{
				Connection:  "probe",
				ExecutionID: "missing",
				Timeout:     timeout,
				NativeArgs:  args,
			},
			want: "absent from index",
		},
		{
			name: "connection already started",
			options: agenttestify.MockAgentOptions{
				Connection:  "main",
				ExecutionID: executionID,
				Timeout:     timeout,
				NativeArgs:  args,
			},
			want: "already started",
		},
		{
			name: "native declaration differs",
			options: agenttestify.MockAgentOptions{
				Connection:  "probe",
				ExecutionID: executionID,
				Timeout:     timeout,
			},
			want: "native arguments differ",
		},
		{
			name: "timeout differs",
			options: agenttestify.MockAgentOptions{
				Connection:  "probe",
				ExecutionID: executionID,
				Timeout:     timeout - time.Nanosecond,
				NativeArgs:  args,
			},
			want: "timeout differs",
		},
		{
			name: "undeclared connection",
			options: agenttestify.MockAgentOptions{
				Connection:  "missing",
				ExecutionID: executionID,
				Timeout:     timeout,
				NativeArgs:  args,
			},
			want: "undeclared mock connection missing",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			run, err := prepared.StartMockAgent(t.Context(), test.options, agenttestify.StartMockAgentStreams{
				Reader: newControlledInput(""),
				Writer: &testWriteCloser{},
				Stderr: &bytes.Buffer{},
			})
			require.Nil(t, run)
			require.ErrorContains(t, err, test.want)
			require.Equal(t, indexBefore, readFile(t, indexPath))
			require.Equal(t, executionsBefore, executionDirectoryCount(t, prepared.FixtureDir))
		})
	}

	require.NoError(t, mainRun.Abort())
	_, err = mainRun.Wait(testContext(t))
	require.Error(t, err)
}

func TestLibraryStartMockAgentRejectsCorruptJoinEvidence(t *testing.T) {
	for _, test := range []struct {
		name       string
		want       string
		mutate     func(*testing.T, *agenttestify.Fixture, string)
		finishMain bool
	}{
		{
			name: "duplicate execution",
			want: "duplicate execution/material identity",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ string) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					executions := value["executions"].([]any)
					value["executions"] = append(executions, executions[0])
				})
			},
		},
		{
			name: "descriptor execution identity",
			want: "descriptor identity",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, executionID string) {
				rewriteLibraryDescriptor(t, fixture.FixtureDir, executionID, func(value map[string]any) {
					value["executionId"] = "other"
				}, nil)
			},
		},
		{
			name: "synchronized connection deletion",
			want: "inventory",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, executionID string) {
				rewriteLibraryDescriptor(t, fixture.FixtureDir, executionID, func(value map[string]any) {
					value["connections"] = libraryConnectionsWithout(value["connections"].([]any), "probe")
				}, func(value map[string]any) {
					value["connections"] = libraryConnectionsWithout(value["connections"].([]any), "probe")
				})
			},
		},
		{
			name: "inconsistent aggregate status",
			want: "every connection is terminal",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ string) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					for _, raw := range execution["connections"].([]any) {
						raw.(map[string]any)["status"] = "passed"
					}
				})
			},
		},
		{
			name:       "material bytes",
			want:       "current legacy inventory differs",
			finishMain: true,
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ string) {
				appendFile(t, filepath.Join(fixture.Current.Path, "remote-probe", "raw", "raw.jsonl"), "\n")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared := prepareStartMockFixture(t, "SDK-CASE-032")
			args := []string{"app-server", "--listen", "stdio://"}
			const timeout = 10 * time.Second
			var mainInput *controlledInput
			var reader io.ReadCloser = &blockingInput{}
			if test.finishMain {
				mainInput = newControlledInput(clientFrames(t, filepath.Join(prepared.Current.Path, "raw", "raw.jsonl")))
				reader = mainInput
			}
			mainRun, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
				Connection: "main",
				Timeout:    timeout,
				NativeArgs: args,
			}, agenttestify.StartMockAgentStreams{
				Reader: reader,
				Writer: &testWriteCloser{},
				Stderr: &bytes.Buffer{},
			})
			require.NoError(t, err)
			require.NotNil(t, mainRun)
			cleanupMockRun(t, mainRun)
			if mainInput != nil {
				awaitFullConsumption(t, mainRun)
				require.NoError(t, mainInput.CloseEOF())
				result, err := mainRun.Wait(testContext(t))
				require.NoError(t, err)
				require.False(t, result.ExecutionComplete)
				require.Equal(t, agenttestify.ExecutionStatusOpen, result.ExecutionStatus)
				requireConnectionStatus(t, result.Execution, "main", agenttestify.ConnectionStatusPassed)
				requireConnectionStatus(t, result.Execution, "probe", agenttestify.ConnectionStatusPending)
			}

			test.mutate(t, prepared, mainRun.Start.ExecutionID)
			indexPath := filepath.Join(prepared.FixtureDir, "executions", "index.json")
			indexBefore := readFile(t, indexPath)
			executionsBefore := executionDirectoryCount(t, prepared.FixtureDir)

			run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
				Connection:  "probe",
				ExecutionID: mainRun.Start.ExecutionID,
				Timeout:     timeout,
				NativeArgs:  args,
			}, agenttestify.StartMockAgentStreams{
				Reader: newControlledInput(""),
				Writer: &testWriteCloser{},
				Stderr: &bytes.Buffer{},
			})
			require.Nil(t, run)
			require.ErrorContains(t, err, test.want)
			require.Equal(t, indexBefore, readFile(t, indexPath))
			require.Equal(t, executionsBefore, executionDirectoryCount(t, prepared.FixtureDir))
		})
	}
}

func libraryConnectionsWithout(connections []any, id string) []any {
	kept := make([]any, 0, len(connections))
	for _, raw := range connections {
		connection := raw.(map[string]any)
		if connection["id"] != id {
			kept = append(kept, raw)
		}
	}
	return kept
}

func TestLibraryProcessMockRejectsEmbeddedFixture(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	fixtureDir := filepath.Join(root, "fixture")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	indexPath := filepath.Join(fixtureDir, "executions", "index.json")
	indexBefore := readFile(t, indexPath)
	executionsBefore := executionDirectoryCount(t, fixtureDir)
	streams := newLibraryStreams(t, root, libraryMockInput)

	result, err := prepared.MockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, streams.io)
	require.Nil(t, result)
	require.ErrorContains(t, err, "process mock requires executable fixture")
	require.Equal(t, indexBefore, readFile(t, indexPath))
	require.Equal(t, executionsBefore, executionDirectoryCount(t, fixtureDir))
}

func TestLibraryStartMockAgentDoesNotLeakRunGoroutines(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	for range 3 {
		input := newControlledInput(libraryMockInput)
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
			NativeArgs: []string{"app-server", "--listen", "stdio://"},
		}, agenttestify.StartMockAgentStreams{
			Reader: input,
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		awaitFullConsumption(t, run)
		require.NoError(t, input.CloseEOF())
		_, err = run.Wait(testContext(t))
		require.NoError(t, err)
	}
	for range 2 {
		run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{}, agenttestify.StartMockAgentStreams{
			Reader: &blockingInput{},
			Writer: &testWriteCloser{},
			Stderr: &bytes.Buffer{},
		})
		require.NoError(t, err)
		require.NoError(t, run.Abort())
		_, err = run.Wait(testContext(t))
		require.Error(t, err)
	}

	require.Eventually(t, func() bool {
		runtime.GC()
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		text := string(stack[:n])
		return !strings.Contains(text, "internal/application.StartMock") &&
			!strings.Contains(text, "internal/application.(*MockRun)") &&
			!strings.Contains(text, "internal/mock.RunInProcess") &&
			!strings.Contains(text, "internal/mock.replayInProcessConnection")
	}, 5*time.Second, 10*time.Millisecond, "in-process mock goroutines remained after Wait and Done")
}

func cleanupMockRun(t *testing.T, run *agenttestify.MockRun) {
	t.Helper()
	t.Cleanup(func() {
		if run == nil {
			return
		}
		_ = run.Abort()
		select {
		case <-run.Done():
		case <-time.After(5 * time.Second):
			t.Errorf("mock run did not stop during cleanup")
		}
	})
}

func awaitFullConsumption(t *testing.T, run *agenttestify.MockRun) {
	t.Helper()
	select {
	case <-run.FullConsumption():
	case <-run.Done():
		t.Fatal("run finished before full consumption")
	case <-time.After(5 * time.Second):
		t.Fatal("full consumption did not arrive")
	}
}

func requireConnectionStatus(t *testing.T, report *agenttestify.ExecutionReport, id string, want agenttestify.ConnectionStatus) {
	t.Helper()
	require.NotNil(t, report)
	for _, connection := range report.Connections {
		if connection.ID == id {
			require.Equalf(t, want, connection.Status, "connection %s", id)
			return
		}
	}
	t.Fatalf("execution report has no connection %s", id)
}

func executionDirectoryCount(t *testing.T, fixtureDir string) int {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(fixtureDir, "packages", "*", "executions", "*"))
	require.NoError(t, err)
	count := 0
	for _, entry := range entries {
		info, err := os.Stat(entry)
		require.NoError(t, err)
		if info.IsDir() {
			count++
		}
	}
	return count
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func ExampleFixture_ExecutionReport() {
	fmt.Println("report := fixture.ExecutionReport(executionID)")
	// Output:
	// report := fixture.ExecutionReport(executionID)
}
