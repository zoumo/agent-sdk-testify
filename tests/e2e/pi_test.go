package conformance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiCLIPrepareRecordMock(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	env = append(env, "PI_CODING_AGENT_DIR="+filepath.Join(root, "isolated-pi"))
	caseFixture := prepareFixture(t, env, "--agent", "pi", "--case", "SDK-CASE-001", "--model", "example/synthetic")
	require.Empty(t, currentMaterialID(t, caseFixture))

	plan := filepath.Join(root, "plan.json")
	writeJSON(t, plan, object{
		"version": 1, "agentType": "pi", "caseId": "synthetic-pi", "driverProfile": "consumer", "project": root,
	})
	fixture := prepareFixture(t, env, "--plan", plan)
	peer := filepath.Join(root, "pi")
	require.NoError(t, os.WriteFile(peer, []byte(`#!/bin/sh
set -eu
test "$*" = "--mode rpc"
IFS= read -r request
printf '%s\n' '{"id":"record","type":"response","command":"get_state","success":true,"data":{"sessionId":"synthetic"}}'
`), 0o700))
	record := mustRun(t, env, root, `{"id":"record","type":"get_state"}`+"\n",
		"agent", "record", "--fixture-dir", fixture, "--cmd", peer, "--", "--mode", "rpc")
	const recorded = `{"id":"record","type":"response","command":"get_state","success":true,"data":{"sessionId":"synthetic"}}` + "\n"
	require.JSONEq(t, recorded, record.stdout)
	require.NotEmpty(t, currentMaterialID(t, fixture))

	mock := mustRun(t, env, root, `{"id":"replay","type":"get_state"}`+"\n",
		"agent", "mock", "--fixture-dir", fixture, "--", "--mode", "rpc")
	require.JSONEq(t, `{"id":"replay","type":"response","command":"get_state","success":true,"data":{"sessionId":"synthetic"}}`+"\n", mock.stdout)
}
