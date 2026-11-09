package agenttestify_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

const (
	libraryModule    = "github.com/zoumo/agent-sdk-testify"
	libraryModuleSum = "h1:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
)

const libraryMockInput = `{"id":"4ef523ca-fb64-44f9-827e-7111bb9dda9e","method":"initialize","params":{"clientInfo":{"name":"official-python-story-recorder","title":"Protocol recording","version":"0.0.0.dev0"},"capabilities":{"experimentalApi":true}}}
{"method":"initialized"}
{"id":"edd297bd-7d60-47c4-b087-3674310f01c3","method":"thread/start","params":{"cwd":"/private/var/folders/v7/hcm12_5x49lf7szbrsz4p_p80000gp/T/agent-sdk-testify-session-1136140093/routes/record/workspace","sandbox":"workspace-write","approvalPolicy":"on-request","ephemeral":true,"baseInstructions":"Complete the supplied synthetic task in the working directory. Be concise.","developerInstructions":"Complete the supplied task in the synthetic project.","model":"theta/DeepSeek-V4.1-Flash-1017"}}
{"id":"7b2dd7f7-fa40-48be-a5b2-536ec05fc74a","method":"turn/start","params":{"input":[{"type":"text","text":"Reply with the word Ready."}],"threadId":"01a1a7ec-721f-70f3-bf6e-8b6e80a76dcd"}}
{"id":"66875618-03af-4a8f-be4c-5320ea64a5dd","method":"turn/start","params":{"input":[{"type":"text","text":"Reply with the word Done."}],"threadId":"01a1a7ec-721f-70f3-bf6e-8b6e80a76dcd"}}
`

func TestLibraryPrepareOpenExportWithoutCLI(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	fixtureDir := filepath.Join(root, "fixture")

	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	require.Equal(t, cwd, workingDirectory(t))
	canonicalFixture, err := filepath.EvalSymlinks(fixtureDir)
	require.NoError(t, err)
	require.Equal(t, canonicalFixture, prepared.FixtureDir)
	require.Equal(t, agenttestify.ToolSourceEmbedded, prepared.Tool.Source)
	require.Equal(t, libraryModule, prepared.Tool.Module)
	require.NotEmpty(t, prepared.Tool.Version)
	require.NotEmpty(t, prepared.Tool.ModuleRevision)
	require.NotEmpty(t, prepared.Tool.ModuleTime)
	require.NotEmpty(t, prepared.Tool.GoVersion)
	require.Empty(t, prepared.Tool.Command)
	require.Empty(t, prepared.Tool.Args)
	require.Empty(t, prepared.Tool.SHA256)
	require.NotNil(t, prepared.Current)

	require.FileExists(t, filepath.Join(fixtureDir, "fixture.json"))
	require.FileExists(t, filepath.Join(fixtureDir, "current.json"))
	fixtureDescriptor := readLibraryJSON(t, filepath.Join(fixtureDir, "fixture.json"))
	require.EqualValues(t, 5, fixtureDescriptor["schemaVersion"])
	tool := fixtureDescriptor["tool"].(map[string]any)
	require.Equal(t, "embedded", tool["source"])
	require.Equal(t, libraryModule, tool["module"])
	require.NotContains(t, tool, "command")
	require.NotContains(t, tool, "args")
	require.NotContains(t, tool, "sha256")
	for _, path := range []string{
		"runtime/baseline/workspace",
		"runtime/routes/record",
		"runtime/routes/mock",
		"runtime/templates/record",
		"runtime/templates/mock",
		"source",
	} {
		require.DirExists(t, filepath.Join(fixtureDir, filepath.FromSlash(path)))
	}
	for _, path := range []string{
		"executions/index.json",
		"materials/index.json",
	} {
		require.FileExists(t, filepath.Join(fixtureDir, filepath.FromSlash(path)))
	}
	descriptor, err := os.Stat(filepath.Join(fixtureDir, "fixture.json"))
	require.NoError(t, err)
	require.Zero(t, descriptor.Mode().Perm()&0o222)

	for _, action := range []string{"record", "mock"} {
		route := prepared.Routes[action]
		require.True(t, route.Enabled)
		require.NotEmpty(t, route.Connections)
		for _, connection := range route.Connections {
			require.Equal(t, agenttestify.ConnectionTransportInProcess, connection.Transport)
			require.Empty(t, connection.Command)
			require.Empty(t, connection.Args)
			require.Empty(t, connection.Launcher)
		}
	}

	opened, err := agenttestify.Open(fixtureDir)
	require.NoError(t, err)
	require.Equal(t, cwd, workingDirectory(t))
	require.Equal(t, prepared.FixtureID, opened.FixtureID)
	require.Equal(t, prepared.Tool, opened.Tool)
	require.Equal(t, prepared.AgentType, opened.AgentType)
	require.Equal(t, prepared.Source, opened.Source)
	require.Equal(t, prepared.Workspace, opened.Workspace)
	require.Equal(t, prepared.ProtocolCwd, opened.ProtocolCwd)
	require.Equal(t, prepared.Routes, opened.Routes)
	require.Equal(t, prepared.Current, opened.Current)

	output := filepath.Join(root, "exported")
	err = agenttestify.Export(agenttestify.ExportOptions{FixtureDir: fixtureDir, Output: output})
	require.NoError(t, err)
	require.Equal(t, cwd, workingDirectory(t))
	require.Equal(t, treeHashes(t, prepared.Current.Path), treeHashes(t, output))
	require.Equal(t, treeContents(t, prepared.Current.Path), treeContents(t, output))
	require.NoFileExists(t, filepath.Join(output, "fixture.json"))
	require.NoFileExists(t, filepath.Join(output, "current.json"))
	require.NoDirExists(t, filepath.Join(output, "executions"))
	require.NoDirExists(t, filepath.Join(output, "runtime"))

	importedDir := filepath.Join(root, "imported")
	imported, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: importedDir,
		Agent:      "codex",
		Recording:  output,
	})
	require.NoError(t, err)
	require.NotNil(t, imported.Current)
	expectedMaterial := *prepared.Current
	expectedMaterial.Path = imported.Current.Path
	require.Equal(t, expectedMaterial, *imported.Current)
	exportedHashes, importedHashes := treeHashes(t, output), treeHashes(t, imported.Current.Path)
	delete(exportedHashes, "material.json")
	delete(importedHashes, "material.json")
	require.Equal(t, exportedHashes, importedHashes)
	assertImportedMaterialBytes(t, output, imported.Current.Path)

	reopened, err := agenttestify.Open(importedDir)
	require.NoError(t, err)
	require.Equal(t, imported.Current, reopened.Current)
}

func TestLibraryPrepareAcceptsExplicitEmbeddedIdentity(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	first, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: filepath.Join(root, "first"),
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)

	identity := &agenttestify.EmbeddedIdentity{
		Version:   first.Tool.Version,
		Sum:       libraryModuleSum,
		Revision:  first.Tool.ModuleRevision,
		Time:      first.Tool.ModuleTime,
		GoVersion: first.Tool.GoVersion,
	}
	second, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir:       filepath.Join(root, "second"),
		Agent:            "codex",
		Case:             "SDK-CASE-001",
		EmbeddedIdentity: identity,
	})
	require.NoError(t, err)
	require.Equal(t, first.Tool, second.Tool)

	identity.GoVersion = ""
	invalidDir := filepath.Join(root, "invalid")
	_, err = agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir:       invalidDir,
		Agent:            "codex",
		Case:             "SDK-CASE-001",
		EmbeddedIdentity: identity,
	})
	require.ErrorContains(t, err, "requires the Go version")
	require.NoDirExists(t, invalidDir)
}

func TestLibraryMockWithoutCLI(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	fixtureDir := filepath.Join(root, "fixture")

	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)

	mockResult, stdout, stderr, mockErr := runLibraryMock(t, prepared, agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, libraryMockInput)
	require.NoError(t, mockErr, stderr)
	require.Empty(t, stderr)
	requireJSONLines(t, stdout)
	require.Contains(t, stdout, `"method":"turn/completed"`)
	require.Equal(t, cwd, workingDirectory(t))

	require.NotNil(t, mockResult)
	require.Equal(t, agenttestify.ConnectionStatusPassed, mockResult.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusPassed, mockResult.ExecutionStatus)
	require.True(t, mockResult.ExecutionComplete)
	require.Equal(t, prepared.FixtureID, mockResult.FixtureID)
	require.Equal(t, "main", mockResult.ConnectionID)
	require.NotEmpty(t, mockResult.ExecutionID)
	require.Equal(t, prepared.Current, mockResult.Material)
	require.FileExists(t, mockResult.ExecutionPath)
	require.FileExists(t, mockResult.ResultPath)
	require.Empty(t, mockResult.ProcessReceiptPath)
	require.NoFileExists(t, filepath.Join(filepath.Dir(mockResult.ExecutionPath), "receipts", "main.process.json"))
	receipt := readLibraryJSON(t, mockResult.ReceiptPath)
	require.Equal(t, "passed", receipt["status"])
	require.Equal(t, true, receipt["fullConsumption"])
	require.Equal(t, true, receipt["clientEOF"])
	require.Equal(t, "in-process", receipt["transport"])
	require.NotContains(t, receipt, "exitCode")
	require.Equal(t, prepared.Current.RecordingSHA256, receipt["recordingSHA256"])
	require.NotNil(t, mockResult.Execution)
	connection := mockResult.Execution.Connections[0]
	require.Equal(t, agenttestify.MockTransportInProcess, connection.Transport)
	require.Nil(t, connection.ExitCode)
	require.Empty(t, connection.ProcessReceiptPath)
	require.True(t, connection.CleanupComplete)
}

func TestLibraryMockFailureRetainsEvidence(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	fixtureDir := filepath.Join(root, "fixture")

	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	currentBefore := readFile(t, filepath.Join(fixtureDir, "current.json"))

	mockResult, stdout, stderr, mockErr := runLibraryMock(t, prepared, agenttestify.MockAgentOptions{}, "")
	require.ErrorContains(t, mockErr, "early client EOF")
	require.Empty(t, stdout)
	require.Contains(t, stderr, "early client EOF")
	require.Equal(t, cwd, workingDirectory(t))
	require.Equal(t, currentBefore, readFile(t, filepath.Join(fixtureDir, "current.json")))

	require.NotNil(t, mockResult)
	require.Equal(t, agenttestify.ConnectionStatusFailed, mockResult.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusFailed, mockResult.ExecutionStatus)
	require.True(t, mockResult.ExecutionComplete)
	require.Equal(t, prepared.FixtureID, mockResult.FixtureID)
	require.Equal(t, "main", mockResult.ConnectionID)
	require.NotEmpty(t, mockResult.ExecutionID)
	require.Equal(t, prepared.Current, mockResult.Material)
	receipt := readLibraryJSON(t, mockResult.ReceiptPath)
	require.Equal(t, "failed", receipt["status"])
	require.Equal(t, false, receipt["fullConsumption"])
	require.Equal(t, false, receipt["clientEOF"])
	require.Equal(t, prepared.Current.RecordingSHA256, receipt["recordingSHA256"])
	require.Contains(t, receipt["error"], "early client EOF")
	require.Equal(t, "strict-replay", receipt["failureClass"])
	require.Empty(t, mockResult.ProcessReceiptPath)
	require.NoFileExists(t, filepath.Join(filepath.Dir(mockResult.ExecutionPath), "receipts", "main.process.json"))
	require.NotNil(t, mockResult.Execution)
	require.Equal(t, agenttestify.FailureClassStrictReplay, mockResult.Execution.Connections[0].Failure)

	failurePath := filepath.Join(filepath.Dir(mockResult.ExecutionPath), "receipts", "main.failure.json")
	require.FileExists(t, failurePath)
	failure := readLibraryJSON(t, failurePath)
	require.Equal(t, "failed", failure["status"])
	require.Contains(t, failure["error"], "early client EOF")
}

func TestLibraryMockOmitsProcessEvidenceAndPreservesCallerCWD(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	fixtureDir := filepath.Join(root, "fixture")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)

	callerCWD := t.TempDir()
	t.Chdir(callerCWD)
	mockResult, _, stderr, mockErr := runLibraryMock(t, prepared, agenttestify.MockAgentOptions{
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, libraryMockInput)
	require.NoError(t, mockErr, stderr)
	require.Equal(t, callerCWD, workingDirectory(t))

	require.Empty(t, mockResult.ProcessReceiptPath)
	require.NotNil(t, mockResult.Execution)
	require.Nil(t, mockResult.Execution.Connections[0].ExitCode)
	require.False(t, mockResult.Execution.Connections[0].NativeLaunched)

	execution := readLibraryJSON(t, mockResult.ExecutionPath)
	workspace, ok := execution["workspace"].(string)
	require.True(t, ok)
	require.True(t, filepath.IsAbs(workspace))
	protocolCwd, ok := execution["protocolCwd"].(string)
	require.True(t, ok)
	require.True(t, filepath.IsAbs(protocolCwd))
}

func TestLibraryMockFirstConnectionLeavesMultiConnectionExecutionOpen(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	fixtureDir := filepath.Join(root, "fixture")
	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Agent:      "codex",
		Case:       "SDK-CASE-032",
	})
	require.NoError(t, err)
	require.NotNil(t, prepared.Current)
	require.Equal(t, []string{"main", "probe"}, prepared.Current.Connections)

	input := clientFrames(t, filepath.Join(prepared.Current.Path, "raw", "raw.jsonl"))
	result, _, stderr, mockErr := runLibraryMock(t, prepared, agenttestify.MockAgentOptions{
		Connection: "main",
		NativeArgs: []string{"app-server", "--listen", "stdio://"},
	}, input)
	require.NoError(t, mockErr, stderr)
	require.Equal(t, agenttestify.ConnectionStatusPassed, result.ConnectionStatus)
	require.Equal(t, agenttestify.ExecutionStatusOpen, result.ExecutionStatus)
	require.False(t, result.ExecutionComplete)

	index := readLibraryJSON(t, filepath.Join(fixtureDir, "executions", "index.json"))
	executions := index["executions"].([]any)
	require.Len(t, executions, 1)
	execution := executions[0].(map[string]any)
	require.Equal(t, "open", execution["status"])
	connections := execution["connections"].([]any)
	require.Equal(t, "passed", connections[0].(map[string]any)["status"])
	require.Equal(t, "pending", connections[1].(map[string]any)["status"])
}

func TestLibraryConcurrentMockDoesNotChangeWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	options := agenttestify.MockAgentOptions{NativeArgs: []string{"app-server", "--listen", "stdio://"}}

	type call struct {
		fixture *agenttestify.Fixture
	}
	var calls []call
	for index := range 2 {
		fixtureDir := filepath.Join(root, "fixture-"+strconv.Itoa(index))
		prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
			FixtureDir: fixtureDir,
			Agent:      "codex",
			Case:       "SDK-CASE-001",
		})
		require.NoError(t, err)
		calls = append(calls, call{
			fixture: prepared,
		})
	}

	type outcome struct {
		result *agenttestify.MockAgentResult
		err    error
	}
	start := make(chan struct{})
	done := make(chan outcome, len(calls))
	for _, configured := range calls {
		go func() {
			<-start
			run, err := configured.fixture.StartMockAgent(t.Context(), options, agenttestify.StartMockAgentStreams{
				Reader: io.NopCloser(strings.NewReader(libraryMockInput)),
				Writer: &testWriteCloser{},
				Stderr: &bytes.Buffer{},
			})
			if err != nil {
				done <- outcome{err: err}
				return
			}
			result, err := run.Wait(t.Context())
			done <- outcome{result: result, err: err}
		}()
	}
	close(start)
	for range calls {
		result := <-done
		require.NoError(t, result.err)
		require.NotNil(t, result.result)
		require.Equal(t, agenttestify.ConnectionStatusPassed, result.result.ConnectionStatus)
		require.Equal(t, agenttestify.ExecutionStatusPassed, result.result.ExecutionStatus)
		require.True(t, result.result.ExecutionComplete)
	}
	require.Equal(t, cwd, workingDirectory(t))
}

func TestLibraryOpenWithoutCurrent(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	fixtureDir := filepath.Join(root, "fixture")
	plan := writeLibraryPlan(t, root)

	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		FixtureDir: fixtureDir,
		Plan:       plan,
	})
	require.NoError(t, err)
	require.Nil(t, prepared.Current)
	require.Equal(t, cwd, workingDirectory(t))

	opened, err := agenttestify.Open(fixtureDir)
	require.NoError(t, err)
	require.Nil(t, opened.Current)
	require.Equal(t, prepared.FixtureID, opened.FixtureID)
	require.Equal(t, cwd, workingDirectory(t))

	output := filepath.Join(root, "exported")
	err = agenttestify.Export(agenttestify.ExportOptions{FixtureDir: fixtureDir, Output: output})
	require.ErrorContains(t, err, "no current recording")
	require.NoDirExists(t, output)
	require.Equal(t, cwd, workingDirectory(t))
}

func TestLibraryPrepareRejectsInvalidOptionsWithoutPartialFixture(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)

	for _, test := range []struct {
		name    string
		options func(string) agenttestify.PrepareOptions
	}{
		{
			name: "missing fixture directory",
			options: func(string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{Agent: "codex", Case: "SDK-CASE-001"}
			},
		},
		{
			name: "relative fixture directory",
			options: func(string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{FixtureDir: "relative", Agent: "codex", Case: "SDK-CASE-001"}
			},
		},
		{
			name: "missing source",
			options: func(fixtureDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{FixtureDir: fixtureDir, Agent: "codex"}
			},
		},
		{
			name: "negative limits",
			options: func(fixtureDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{
					FixtureDir: fixtureDir, Agent: "codex", Case: "SDK-CASE-001", MaxFrames: -1,
				}
			},
		},
		{
			name: "unknown agent",
			options: func(fixtureDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{FixtureDir: fixtureDir, Agent: "unknown", Case: "SDK-CASE-001"}
			},
		},
		{
			name: "missing plan",
			options: func(fixtureDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{
					FixtureDir: fixtureDir, Plan: filepath.Join(root, "missing-plan.json"),
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixtureDir := filepath.Join(t.TempDir(), "fixture")
			_, err := agenttestify.Prepare(test.options(fixtureDir))
			require.Error(t, err)
			require.NoDirExists(t, fixtureDir)
			require.Equal(t, cwd, workingDirectory(t))
		})
	}
}

func TestLibraryOpenAndExportRejectInvalidInputs(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)

	for _, path := range []string{"", "relative", filepath.Join(root, "missing")} {
		_, err := agenttestify.Open(path)
		require.Error(t, err)
		require.Equal(t, cwd, workingDirectory(t))
	}

	existing := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(existing, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "sentinel"), []byte("preserve\n"), 0o600))
	err := agenttestify.Export(agenttestify.ExportOptions{
		FixtureDir: filepath.Join(root, "missing-fixture"),
		Output:     existing,
	})
	require.Error(t, err)
	require.Equal(t, []byte("preserve\n"), readFile(t, filepath.Join(existing, "sentinel")))
	require.Equal(t, cwd, workingDirectory(t))
}

func isolateLibraryTest(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "empty-path")
	require.NoError(t, os.Mkdir(path, 0o700))
	t.Setenv("PATH", path)
	t.Setenv("HOME", filepath.Join(root, "host-home"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "host-codex"))
	_, err := exec.LookPath("agent-sdk-testify")
	require.Error(t, err, "library tests must not have the CLI on PATH")
}

func workingDirectory(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	return dir
}

func treeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(readFile(t, path))
		hashes[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	}))
	return hashes
}

func treeContents(t *testing.T, root string) map[string][]byte {
	t.Helper()
	contents := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents[filepath.ToSlash(rel)] = append([]byte(nil), readFile(t, path)...)
		return nil
	}))
	return contents
}

func assertImportedMaterialBytes(t *testing.T, exportedRoot, importedRoot string) {
	t.Helper()
	exported, imported := treeContents(t, exportedRoot), treeContents(t, importedRoot)
	require.Len(t, imported, len(exported))
	for name, want := range exported {
		got, present := imported[name]
		require.True(t, present, name)
		if name == "material.json" {
			// The material envelope binds its new fixture-local path; all other
			// package bytes must survive the import unchanged.
			var exportedMaterial, importedMaterial map[string]any
			require.NoError(t, json.Unmarshal(want, &exportedMaterial))
			require.NoError(t, json.Unmarshal(got, &importedMaterial))
			exportedMaterial["path"], importedMaterial["path"] = "", ""
			require.Equal(t, exportedMaterial, importedMaterial)
			continue
		}
		require.Equal(t, want, got, name)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func writeLibraryPlan(t *testing.T, root string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"version":       1,
		"agentType":     "codex",
		"caseId":        "library-ticket-03",
		"driverProfile": "consumer",
		"project":       filepath.Join(root, "project"),
		"scenario": map[string]any{
			"files": map[string]string{"fixture.txt": "fixture\n"},
		},
	})
	require.NoError(t, err)
	path := filepath.Join(root, "plan.json")
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
	return path
}

func runLibraryMock(
	t *testing.T,
	fixture *agenttestify.Fixture,
	options agenttestify.MockAgentOptions,
	input string,
) (*agenttestify.MockAgentResult, string, string, error) {
	t.Helper()
	output := &testWriteCloser{}
	var errorsOutput bytes.Buffer
	run, err := fixture.StartMockAgent(t.Context(), options, agenttestify.StartMockAgentStreams{
		Reader: io.NopCloser(strings.NewReader(input)),
		Writer: output,
		Stderr: &errorsOutput,
	})
	if run == nil {
		return nil, "", errorsOutput.String(), err
	}
	result, runErr := run.Wait(t.Context())
	return result, output.String(), errorsOutput.String(), runErr
}

type libraryStreams struct {
	io     agenttestify.Streams
	out    *os.File
	errOut *os.File
}

func newLibraryStreams(t *testing.T, root, input string) libraryStreams {
	t.Helper()
	out, err := os.CreateTemp(root, "stdout-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = out.Close() })
	errOut, err := os.CreateTemp(root, "stderr-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = errOut.Close() })
	return libraryStreams{
		io: agenttestify.Streams{
			In:  writeLibraryStream(t, root, "stdin", input),
			Out: out,
			Err: errOut,
		},
		out:    out,
		errOut: errOut,
	}
}

func (s libraryStreams) output(t *testing.T) string {
	t.Helper()
	return readOpenFile(t, s.out)
}

func (s libraryStreams) errors(t *testing.T) string {
	t.Helper()
	return readOpenFile(t, s.errOut)
}

func writeLibraryStream(t *testing.T, root, name, value string) *os.File {
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

func readOpenFile(t *testing.T, file *os.File) string {
	t.Helper()
	_, err := file.Seek(0, 0)
	require.NoError(t, err)
	data, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	return string(data)
}

func requireJSONLines(t *testing.T, value string) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(value), "\n") {
		require.True(t, json.Valid([]byte(line)), line)
	}
}

func readLibraryJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(readFile(t, path), &value))
	return value
}

func clientFrames(t *testing.T, path string) string {
	t.Helper()
	var frames strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(string(readFile(t, path))), "\n") {
		var frame struct {
			Direction string          `json:"direction"`
			Wire      json.RawMessage `json:"wire"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &frame))
		if frame.Direction == "c2s" {
			frames.Write(frame.Wire)
			frames.WriteByte('\n')
		}
	}
	require.NotEmpty(t, frames.String())
	return frames.String()
}
