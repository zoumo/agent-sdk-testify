package agenttestify_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agenttestify "github.com/zoumo/agent-sdk-testify"
)

const piHistoryName = "2026-11-24T15-00-00-000Z_session-pi.jsonl"

func TestLibraryPiRecordMockExport(t *testing.T) {
	root := t.TempDir()
	isolateLibraryTest(t, root)
	hostConfig := filepath.Join(root, "host-pi")
	require.NoError(t, os.Mkdir(hostConfig, 0o700))
	t.Setenv("PI_CODING_AGENT_DIR", hostConfig)
	t.Setenv("DEEPSEEK_API_KEY", "synthetic-pi-environment-secret")
	const hostAuth = `{"example":{"type":"api_key","key":"synthetic-pi-auth-secret"}}`
	const hostModels = `{"providers":{"example":{"apiKey":"synthetic-pi-model-secret"}}}`
	writeFile(t, filepath.Join(hostConfig, "auth.json"), hostAuth)
	writeFile(t, filepath.Join(hostConfig, "models.json"), hostModels)
	writeFile(t, filepath.Join(hostConfig, "settings.json"), "{}")

	plan := filepath.Join(root, "pi-plan.json")
	writeFile(t, plan, `{"version":1,"agentType":"pi","caseId":"synthetic-pi","driverProfile":"consumer","project":"`+root+`"}`)
	fixture, err := agenttestify.Prepare(agenttestify.PrepareOptions{FixtureDir: filepath.Join(root, "fixture"), Plan: plan})
	require.NoError(t, err)
	require.Nil(t, fixture.Current)

	const input = `{"id":"record-id","type":"prompt","message":"synthetic"}` + "\n"
	const response = `{"type":"response","id":"record-id","command":"prompt","success":true,"data":{"disposition":"started"}}`
	const event = `{"type":"agent_settled","futureField":9007199254740993}`
	record := func(command, history, stdout string) (*agenttestify.RecordAgentResult, error) {
		t.Helper()
		peer := filepath.Join(t.TempDir(), "synthetic-pi")
		script := `#!/bin/sh
set -eu
test "$*" = "--mode rpc"
test "$PWD" = "${PI_CODING_AGENT_DIR%/config}/workspace"
test "$HOME" = "${PI_CODING_AGENT_DIR%/config}/home"
test "$TMPDIR" = "${PI_CODING_AGENT_DIR%/config}/tmp"
test "$DEEPSEEK_API_KEY" = "synthetic-pi-environment-secret"
test -f "$PI_CODING_AGENT_DIR/auth.json"
test -f "$PI_CODING_AGENT_DIR/models.json"
test ! -L "$PI_CODING_AGENT_DIR/auth.json"
test ! -e "$PI_CODING_AGENT_DIR/settings.json"
IFS= read -r request
history="$PI_CODING_AGENT_DIR/sessions/--synthetic--"
/bin/mkdir -p "$history"
printf '{"type":"session","version":3,"id":"session-pi","timestamp":"2026-11-24T15:00:00.000Z","cwd":"%s"}\n' "$PWD" > "$history/` + piHistoryName + `"
printf '%s\n' ` + shellQuote(history) + ` >> "$history/` + piHistoryName + `"
printf ' ' >> "$PI_CODING_AGENT_DIR/auth.json"
printf '%s\n' ` + shellQuote(command) + ` ` + shellQuote(stdout) + `
`
		require.NoError(t, os.WriteFile(peer, []byte(script), 0o700))
		result, out, stderr, err := runLibraryRecord(t, fixture, agenttestify.RecordAgentOptions{
			Command: peer, NativeArgs: []string{"--mode", "rpc"}, Timeout: 10 * time.Second,
		}, input)
		require.Empty(t, stderr)
		require.Equal(t, command+"\n"+stdout+"\n", out)
		return result, err
	}

	const entry = `{"type":"message","id":"entry1","parentId":null,"timestamp":"2026-11-24T15:00:00.000Z","message":{"role":"user","content":"synthetic"}}`
	captured, err := record(response, entry, event)
	require.NoError(t, err)
	material := assertPublishedLibraryRecord(t, fixture, captured)
	planData := readLibraryJSON(t, filepath.Join(material.Path, "plan.json"))
	binding := planData["historyBindings"].([]any)
	require.Len(t, binding, 1)
	saved := binding[0].(map[string]any)
	require.Equal(t, "session-pi", saved["sessionId"])
	nativeTarget := saved["nativeTarget"].(string)
	historyPath := saved["source"].(string)
	historyBytes := readFile(t, filepath.Join(material.Path, historyPath))
	require.Contains(t, string(historyBytes), entry+"\n")
	require.NoFileExists(t, filepath.Join(filepath.Dir(saved["project"].(string)), "config", "auth.json"))
	require.Equal(t, []byte(hostAuth), readFile(t, filepath.Join(hostConfig, "auth.json")))

	exported := filepath.Join(root, "export")
	require.NoError(t, agenttestify.Export(agenttestify.ExportOptions{FixtureDir: fixture.FixtureDir, Output: exported}))
	require.Equal(t, treeHashes(t, material.Path), treeHashes(t, exported))
	require.NoFileExists(t, filepath.Join(exported, "config", "auth.json"))
	require.NoFileExists(t, filepath.Join(exported, "config", "models.json"))

	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "missing-pi"))
	replay, err := agenttestify.Prepare(agenttestify.PrepareOptions{FixtureDir: filepath.Join(root, "replay"), Recording: exported})
	require.NoError(t, err)
	const mockInput = `{"type":"prompt","id":"live-id","message":"synthetic"}` + "\n"
	output := &testWriteCloser{}
	run, err := replay.StartMockAgent(t.Context(), agenttestify.MockAgentOptions{
		NativeArgs: []string{"--mode", "rpc"}, Timeout: 10 * time.Second,
	}, agenttestify.StartMockAgentStreams{Reader: io.NopCloser(strings.NewReader(mockInput)), Writer: output, Stderr: io.Discard})
	require.NoError(t, err)
	start := run.Start
	result, err := run.Wait(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, saved["project"], start.Workspace)
	require.Equal(t, start.Config, start.Env["PI_CODING_AGENT_DIR"])
	require.NotContains(t, start.Env, "DEEPSEEK_API_KEY")
	require.NoFileExists(t, filepath.Join(start.Config, "auth.json"))
	require.NoFileExists(t, filepath.Join(start.Config, "models.json"))
	require.Equal(t, historyBytes, readFile(t, filepath.Join(filepath.Dir(start.Config), nativeTarget)))
	require.True(t, result.Execution.Connections[0].FullConsumption)
	require.Equal(t, strings.Replace(response, "record-id", "live-id", 1)+"\n"+event+"\n", output.String())

	require.NoError(t, os.Remove(filepath.Join(exported, historyPath)))
	_, err = agenttestify.Prepare(agenttestify.PrepareOptions{FixtureDir: filepath.Join(root, "no-history"), Recording: exported})
	require.Error(t, err)

	for name, badInput := range map[string]string{"changed parameter": strings.Replace(mockInput, "synthetic", "changed", 1), "early EOF": ""} {
		t.Run(name, func(t *testing.T) {
			failed, _, _, err := runLibraryMock(t, replay, agenttestify.MockAgentOptions{}, badInput)
			require.Error(t, err)
			require.Equal(t, agenttestify.FailureClassStrictReplay, failed.Execution.Connections[0].Failure)
			require.False(t, failed.Execution.Connections[0].FullConsumption)
		})
	}

	t.Setenv("PI_CODING_AGENT_DIR", hostConfig)
	bad, err := record(response, `{"type":"message"`, event)
	require.ErrorContains(t, err, "invalid pi history entry")
	require.Equal(t, agenttestify.ExecutionStatusFailed, bad.ExecutionStatus)
	require.FileExists(t, bad.ReceiptPath)
	opened, err := agenttestify.Open(fixture.FixtureDir)
	require.NoError(t, err)
	require.Equal(t, material.ID, opened.Current.ID)

	leaked, err := record(response, entry, `{"type":"agent_settled","error":"synthetic-pi-model-secret"}`)
	require.ErrorContains(t, err, "private evidence quarantined")
	require.Equal(t, agenttestify.ExecutionStatusFailed, leaked.ExecutionStatus)

	const wrongResponse = `{"type":"response","id":"record-id","command":"abort","success":false,"error":"synthetic"}`
	_, err = record(wrongResponse, entry, event)
	require.NoError(t, err)
	failed, _, _, err := runLibraryMock(t, fixture, agenttestify.MockAgentOptions{}, mockInput)
	require.ErrorContains(t, err, "pi response command mismatch")
	require.Equal(t, agenttestify.FailureClassStrictReplay, failed.Execution.Connections[0].Failure)
	require.False(t, failed.Execution.Connections[0].FullConsumption)
}
