package agenttestify_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

const libraryModule = "github.com/zoumo/agent-sdk-testify"

func TestLibraryPrepareOpenExportWithoutCLI(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	sessionDir := filepath.Join(root, "session")

	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: sessionDir,
		Agent:      "codex",
		Case:       "SDK-CASE-001",
	})
	require.NoError(t, err)
	require.Equal(t, cwd, workingDirectory(t))
	canonicalSession, err := filepath.EvalSymlinks(sessionDir)
	require.NoError(t, err)
	require.Equal(t, canonicalSession, prepared.SessionDir)
	require.Equal(t, agenttestify.ToolSourceEmbedded, prepared.Tool.Source)
	require.Equal(t, libraryModule, prepared.Tool.Command)
	require.NotEmpty(t, prepared.Tool.Version)
	require.Empty(t, prepared.Tool.SHA256)
	require.NotNil(t, prepared.Current)

	require.FileExists(t, filepath.Join(sessionDir, "session.json"))
	require.FileExists(t, filepath.Join(sessionDir, "current.json"))
	for _, path := range []string{
		"runtime/baseline/workspace",
		"runtime/routes/record",
		"runtime/routes/mock",
		"runtime/templates/record",
		"runtime/templates/mock",
		"source",
	} {
		require.DirExists(t, filepath.Join(sessionDir, filepath.FromSlash(path)))
	}
	for _, path := range []string{
		"executions/index.json",
		"materials/index.json",
	} {
		require.FileExists(t, filepath.Join(sessionDir, filepath.FromSlash(path)))
	}
	descriptor, err := os.Stat(filepath.Join(sessionDir, "session.json"))
	require.NoError(t, err)
	require.Zero(t, descriptor.Mode().Perm()&0o222)

	for _, action := range []string{"record", "mock"} {
		route := prepared.Routes[action]
		require.True(t, route.Enabled)
		require.NotEmpty(t, route.Connections)
		for _, connection := range route.Connections {
			require.True(t, connection.Embedded)
			require.Equal(t, libraryModule, connection.Command)
			require.Empty(t, connection.Launcher)
		}
	}

	opened, err := agenttestify.Open(sessionDir)
	require.NoError(t, err)
	require.Equal(t, cwd, workingDirectory(t))
	require.Equal(t, prepared.SessionID, opened.SessionID)
	require.Equal(t, prepared.Tool, opened.Tool)
	require.Equal(t, prepared.AgentType, opened.AgentType)
	require.Equal(t, prepared.Source, opened.Source)
	require.Equal(t, prepared.Workspace, opened.Workspace)
	require.Equal(t, prepared.ProtocolCwd, opened.ProtocolCwd)
	require.Equal(t, prepared.Routes, opened.Routes)
	require.Equal(t, prepared.Current, opened.Current)

	output := filepath.Join(root, "exported")
	err = agenttestify.Export(agenttestify.ExportOptions{SessionDir: sessionDir, Output: output})
	require.NoError(t, err)
	require.Equal(t, cwd, workingDirectory(t))
	require.Equal(t, treeHashes(t, prepared.Current.Path), treeHashes(t, output))
	require.NoFileExists(t, filepath.Join(output, "session.json"))
	require.NoFileExists(t, filepath.Join(output, "current.json"))
	require.NoDirExists(t, filepath.Join(output, "executions"))
	require.NoDirExists(t, filepath.Join(output, "runtime"))
}

func TestLibraryOpenWithoutCurrent(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)
	sessionDir := filepath.Join(root, "session")
	plan := writeLibraryPlan(t, root)

	prepared, err := agenttestify.Prepare(agenttestify.PrepareOptions{
		SessionDir: sessionDir,
		Plan:       plan,
	})
	require.NoError(t, err)
	require.Nil(t, prepared.Current)
	require.Equal(t, cwd, workingDirectory(t))

	opened, err := agenttestify.Open(sessionDir)
	require.NoError(t, err)
	require.Nil(t, opened.Current)
	require.Equal(t, prepared.SessionID, opened.SessionID)
	require.Equal(t, cwd, workingDirectory(t))

	output := filepath.Join(root, "exported")
	err = agenttestify.Export(agenttestify.ExportOptions{SessionDir: sessionDir, Output: output})
	require.ErrorContains(t, err, "no current recording")
	require.NoDirExists(t, output)
	require.Equal(t, cwd, workingDirectory(t))
}

func TestLibraryPrepareRejectsInvalidOptionsWithoutPartialSession(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	cwd := workingDirectory(t)

	for _, test := range []struct {
		name    string
		options func(string) agenttestify.PrepareOptions
	}{
		{
			name: "missing session directory",
			options: func(string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{Agent: "codex", Case: "SDK-CASE-001"}
			},
		},
		{
			name: "relative session directory",
			options: func(string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{SessionDir: "relative", Agent: "codex", Case: "SDK-CASE-001"}
			},
		},
		{
			name: "missing source",
			options: func(sessionDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{SessionDir: sessionDir, Agent: "codex"}
			},
		},
		{
			name: "negative limits",
			options: func(sessionDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{
					SessionDir: sessionDir, Agent: "codex", Case: "SDK-CASE-001", MaxFrames: -1,
				}
			},
		},
		{
			name: "unknown agent",
			options: func(sessionDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{SessionDir: sessionDir, Agent: "unknown", Case: "SDK-CASE-001"}
			},
		},
		{
			name: "missing plan",
			options: func(sessionDir string) agenttestify.PrepareOptions {
				return agenttestify.PrepareOptions{
					SessionDir: sessionDir, Plan: filepath.Join(root, "missing-plan.json"),
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionDir := filepath.Join(t.TempDir(), "session")
			_, err := agenttestify.Prepare(test.options(sessionDir))
			require.Error(t, err)
			require.NoDirExists(t, sessionDir)
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
		SessionDir: filepath.Join(root, "missing-session"),
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
