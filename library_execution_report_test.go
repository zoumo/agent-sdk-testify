package agenttestify_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

func TestLibraryExecutionReportFailsClosedOnCorruptEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		want   string
		mutate func(*testing.T, *agenttestify.Fixture, *agenttestify.MockAgentResult, *agenttestify.ExecutionReport)
	}{
		{
			name: "descriptor hash",
			want: "descriptor",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, result *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				appendFile(t, result.ExecutionPath, "\n")
			},
		},
		{
			name: "fixture descriptor hash",
			want: "fixture descriptor",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				appendFile(t, filepath.Join(fixture.FixtureDir, "fixture.json"), "\n")
			},
		},
		{
			name: "duplicate index execution",
			want: "duplicate execution/material identity",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					executions := value["executions"].([]any)
					value["executions"] = append(executions, executions[0])
				})
			},
		},
		{
			name: "descriptor execution identity",
			want: "execution path differs from package identity",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, result *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				rewriteLibraryDescriptor(t, fixture.FixtureDir, result.ExecutionID, func(value map[string]any) {
					value["executionId"] = "other"
				}, nil)
			},
		},
		{
			name: "descriptor fixture identity",
			want: "identity",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, result *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				rewriteLibraryDescriptor(t, fixture.FixtureDir, result.ExecutionID, func(value map[string]any) {
					value["fixtureId"] = "other"
				}, nil)
			},
		},
		{
			name: "material identity",
			want: "material",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "packages", "index.json"), func(value map[string]any) {
					material := value["materials"].([]any)[0].(map[string]any)
					material["planSHA256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "passed execution with failed connection",
			want: "conflicts",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					connection := execution["connections"].([]any)[0].(map[string]any)
					connection["status"] = "failed"
				})
			},
		},
		{
			name: "failed execution with active connection",
			want: "still active",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					execution["status"] = "failed"
					connection := execution["connections"].([]any)[0].(map[string]any)
					connection["status"] = "running"
				})
			},
		},
		{
			name: "failed execution without failure",
			want: "without a failed connection",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					execution["status"] = "failed"
				})
			},
		},
		{
			name: "open execution with terminal connections",
			want: "every connection is terminal",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					execution["status"] = "open"
				})
			},
		},
		{
			name: "invalid connection status",
			want: "invalid status",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					connection := execution["connections"].([]any)[0].(map[string]any)
					connection["status"] = "unknown"
				})
			},
		},
		{
			name: "duplicate index connection",
			want: "duplicate",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					connections := execution["connections"].([]any)
					execution["connections"] = append(connections, connections[0])
				})
			},
		},
		{
			name: "missing index connection",
			want: "inventory",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "executions", "index.json"), func(value map[string]any) {
					execution := value["executions"].([]any)[0].(map[string]any)
					execution["connections"] = []any{}
				})
			},
		},
		{
			name: "missing terminal receipt",
			want: "receipt",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				require.NoError(t, os.Remove(report.Connections[0].ReceiptPath))
			},
		},
		{
			name: "malformed terminal receipt",
			want: "replay receipt",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				writeFile(t, report.Connections[0].ReceiptPath, "{")
			},
		},
		{
			name: "receipt schema",
			want: "schema or version",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["schemaVersion"] = float64(999)
				})
			},
		},
		{
			name: "receipt version",
			want: "schema or version",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["version"] = float64(2)
				})
			},
		},
		{
			name: "receipt boolean",
			want: "not boolean",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["fullConsumption"] = "true"
				})
			},
		},
		{
			name: "receipt status",
			want: "status",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["status"] = "failed"
				})
			},
		},
		{
			name: "receipt provider",
			want: "provider",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["provider"] = "claude"
				})
			},
		},
		{
			name: "receipt recording path",
			want: "recording reference",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["recording"] = "/different"
				})
			},
		},
		{
			name: "receipt plan",
			want: "plan",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["planSHA256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "receipt recording hash",
			want: "recording hash",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["recordingSHA256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "receipt frames",
			want: "frame count",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["frames"] = value["frames"].(float64) + 1
				})
			},
		},
		{
			name: "receipt native launch",
			want: "in-process mock",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["nativeLaunched"] = true
				})
			},
		},
		{
			name: "receipt process fact",
			want: "process fact",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["exitCode"] = float64(0)
				})
			},
		},
		{
			name: "receipt transport",
			want: "transport",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ReceiptPath, func(value map[string]any) {
					value["transport"] = "process"
					value["exitCode"] = float64(0)
				})
			},
		},
		{
			name: "descriptor process receipt",
			want: "process receipt",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, result *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				rewriteLibraryDescriptor(t, fixture.FixtureDir, result.ExecutionID, func(value map[string]any) {
					connection := value["connections"].([]any)[0].(map[string]any)
					connection["processReceipt"] = "/different/process.json"
				}, nil)
			},
		},
		{
			name: "missing terminal result",
			want: "result",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				require.NoError(t, os.Remove(report.Connections[0].ResultPath))
			},
		},
		{
			name: "malformed terminal result",
			want: "result",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				writeFile(t, report.Connections[0].ResultPath, "not-json")
			},
		},
		{
			name: "result schema",
			want: "execution identity",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["schemaVersion"] = float64(2)
				})
			},
		},
		{
			name: "result execution identity",
			want: "execution identity",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["executionId"] = "other"
				})
			},
		},
		{
			name: "result connection identity",
			want: "connection",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["connectionId"] = "other"
				})
			},
		},
		{
			name: "result material identity",
			want: "material",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					material := value["material"].(map[string]any)
					material["recordingSHA256"] = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "result process identity",
			want: "process fact",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["processId"] = float64(123)
				})
			},
		},
		{
			name: "result process receipt",
			want: "process fact",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["processReceipt"] = "/different/process.json"
				})
			},
		},
		{
			name: "result command",
			want: "process fact",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["command"] = "mock"
				})
			},
		},
		{
			name: "result exit code",
			want: "process fact",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				mutateLibraryJSON(t, report.Connections[0].ResultPath, func(value map[string]any) {
					value["exitCode"] = float64(0)
				})
			},
		},
		{
			name: "result process receipt file",
			want: "process receipt file",
			mutate: func(t *testing.T, _ *agenttestify.Fixture, _ *agenttestify.MockAgentResult, report *agenttestify.ExecutionReport) {
				writeFile(t, filepath.Join(filepath.Dir(report.Connections[0].ResultPath), "main.process.json"), "{}\n")
			},
		},
		{
			name: "material bytes",
			want: "current legacy inventory differs",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, _ *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				appendFile(t, filepath.Join(fixture.Current.Path, "raw", "raw.jsonl"), "\n")
			},
		},
		{
			name: "manifest path",
			want: "escapes",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, result *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				repinLibraryManifest(t, fixture, result.ExecutionID, func(value map[string]any) {
					files := value["files"].(map[string]any)
					files["../outside.jsonl"] = files["raw.jsonl"]
					delete(files, "raw.jsonl")
				})
			},
		},
		{
			name: "manifest hash",
			want: "source hash mismatch",
			mutate: func(t *testing.T, fixture *agenttestify.Fixture, result *agenttestify.MockAgentResult, _ *agenttestify.ExecutionReport) {
				repinLibraryManifest(t, fixture, result.ExecutionID, func(value map[string]any) {
					value["files"].(map[string]any)["raw.jsonl"] = strings.Repeat("0", 64)
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, result := completedLibraryMockReport(t)
			report, err := fixture.ExecutionReport(result.ExecutionID)
			require.NoError(t, err)
			require.True(t, report.Complete)

			test.mutate(t, fixture, result, report)
			corrupt, err := fixture.ExecutionReport(result.ExecutionID)
			require.ErrorContains(t, err, test.want)
			require.Nil(t, corrupt)
		})
	}
}

func TestLibraryExecutionReportMissingExecutionDoesNotCreateOne(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	before := executionDirectoryCount(t, prepared.FixtureDir)

	report, err := prepared.ExecutionReport("missing")
	require.ErrorContains(t, err, "absent")
	require.Nil(t, report)
	require.Equal(t, before, executionDirectoryCount(t, prepared.FixtureDir))
	require.NoDirExists(t, filepath.Join(prepared.FixtureDir, "executions", "missing"))

	report, err = prepared.ExecutionReport("../escape")
	require.ErrorContains(t, err, "identity")
	require.Nil(t, report)
	require.Equal(t, before, executionDirectoryCount(t, prepared.FixtureDir))
}

func TestLibraryMockWaitFailsClosedWhenReportCannotBeBuilt(t *testing.T) {
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	input := newControlledInput(libraryMockInput)
	var stderr strings.Builder
	run, err := prepared.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, agenttestify.StartMockAgentStreams{
		Reader: input,
		Writer: &testWriteCloser{},
		Stderr: &stderr,
	})
	require.NoError(t, err)
	awaitFullConsumption(t, run)
	appendFile(t, run.Start.ExecutionPath, "\n")
	require.NoError(t, input.CloseEOF())

	result, err := run.Wait(testContext(t))
	require.ErrorContains(t, err, "descriptor")
	require.NotNil(t, result)
	require.Nil(t, result.Execution)
	require.False(t, result.ExecutionComplete)
	require.Equal(t, agenttestify.ExecutionStatusUnknown, result.ExecutionStatus)
	require.Contains(t, stderr.String(), "descriptor")
	select {
	case <-run.Done():
	default:
		t.Fatal("Done remained open after a failed report")
	}
}

func completedLibraryMockReport(t *testing.T) (*agenttestify.Fixture, *agenttestify.MockAgentResult) {
	t.Helper()
	prepared := prepareStartMockFixture(t, "SDK-CASE-001")
	result, _, stderr, err := runLibraryMock(t, prepared, agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, libraryMockInput)
	require.NoError(t, err, stderr)
	require.NotNil(t, result)
	return prepared, result
}

func mutateLibraryJSON(t *testing.T, path string, mutate func(map[string]any)) {
	t.Helper()
	value := readLibraryJSON(t, path)
	mutate(value)
	data, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	writeFile(t, path, string(append(data, '\n')))
}

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(value), 0o600))
}

func appendFile(t *testing.T, path, value string) {
	t.Helper()
	require.NoError(t, os.Chmod(path, 0o600))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(value)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func rewriteLibraryDescriptor(
	t *testing.T,
	fixtureDir string,
	executionID string,
	mutateDescriptor func(map[string]any),
	mutateIndexExecution func(map[string]any),
) {
	t.Helper()
	descriptorPath := libraryExecutionPath(t, fixtureDir, executionID)
	mutateLibraryJSON(t, descriptorPath, mutateDescriptor)
	digest := libraryFileDigest(t, descriptorPath)
	mutateLibraryJSON(t, filepath.Join(fixtureDir, "executions", "index.json"), func(value map[string]any) {
		for _, raw := range value["executions"].([]any) {
			execution := raw.(map[string]any)
			if execution["id"] != executionID {
				continue
			}
			execution["descriptorSHA256"] = digest
			if mutateIndexExecution != nil {
				mutateIndexExecution(execution)
			}
			return
		}
		t.Fatalf("execution %s is absent from index", executionID)
	})
}

func libraryExecutionPath(t *testing.T, fixtureDir, executionID string) string {
	t.Helper()
	index := readLibraryJSON(t, filepath.Join(fixtureDir, "executions", "index.json"))
	for _, raw := range index["executions"].([]any) {
		execution := raw.(map[string]any)
		if execution["id"] == executionID {
			return filepath.Join(fixtureDir, "packages", execution["materialId"].(string), "executions", executionID, "execution.json")
		}
	}
	t.Fatalf("execution %s is absent from index", executionID)
	return ""
}

func repinLibraryManifest(t *testing.T, fixture *agenttestify.Fixture, executionID string, mutate func(map[string]any)) {
	t.Helper()
	manifestPath := filepath.Join(fixture.Current.Path, "manifest.json")
	mutateLibraryJSON(t, manifestPath, mutate)
	digest := libraryFileDigest(t, manifestPath)
	repinMaterialManifest := func(material map[string]any) {
		material["manifestSHA256"] = digest
		if files, ok := material["files"].(map[string]any); ok {
			files["manifest.json"] = digest
		}
	}
	files := treeHashes(t, fixture.Current.Path)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var inventory strings.Builder
	for _, name := range names {
		inventory.WriteString(name + "\x00" + files[name] + "\n")
	}
	sum := sha256.Sum256([]byte(inventory.String()))
	legacyHash := hex.EncodeToString(sum[:])
	mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "packages", "index.json"), func(value map[string]any) {
		for _, raw := range value["materials"].([]any) {
			material := raw.(map[string]any)
			if material["id"] != fixture.Current.ID {
				continue
			}
			repinMaterialManifest(material)
			material["files"] = files
			material["legacyInventorySHA256"] = legacyHash
			return
		}
		t.Fatalf("material %s is absent from index", fixture.Current.ID)
	})
	// This fixture imports a descriptorless legacy tree. Rebind its control
	// inventory so the test reaches manifest validation without inventing a core
	// descriptor or altering any recorded source file beyond the test mutation.
	require.NoFileExists(t, filepath.Join(fixture.Current.Path, "material.json"))
	mutateLibraryJSON(t, filepath.Join(fixture.FixtureDir, "current.json"), func(value map[string]any) {
		value["manifestSHA256"] = digest
		value["legacyInventorySHA256"] = legacyHash
	})
	rewriteLibraryDescriptor(t, fixture.FixtureDir, executionID, func(value map[string]any) {
		artifact := value["artifact"].(map[string]any)
		repinMaterialManifest(artifact)
		artifact["files"] = files
		artifact["legacyInventorySHA256"] = legacyHash
		for _, raw := range value["connections"].([]any) {
			connection := raw.(map[string]any)
			connection["manifestSHA256"] = digest
		}
	}, nil)
}

func libraryFileDigest(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256(readFile(t, path))
	return hex.EncodeToString(sum[:])
}
