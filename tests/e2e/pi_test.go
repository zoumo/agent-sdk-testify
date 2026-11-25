package conformance

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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

func TestPiCLIExtensionAnswerAfterControlReceipt(t *testing.T) {
	root := t.TempDir()
	_, _, env := isolatedCommands(t, root, "codex")
	env = append(env, "PI_CODING_AGENT_DIR="+filepath.Join(root, "isolated-pi"))
	plan := filepath.Join(root, "dialog-plan.json")
	writeJSON(t, plan, object{
		"version": 1, "agentType": "pi", "caseId": "synthetic-pi-extension-dialog",
		"driverProfile": "consumer", "project": root,
	})
	fixture := prepareFixture(t, env, "--plan", plan)
	peer := filepath.Join(root, "synthetic-pi")
	const native = `#!/bin/sh
set -eu
test "$*" = "--mode rpc"
IFS= read -r dialog
test "$dialog" = '{"id":"prompt-record","type":"prompt","message":"Ask before proceeding."}'
printf '%s\n' '{"id":"prompt-record","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}'
printf '%s\n' '{"type":"extension_ui_request","id":"shared","method":"confirm","message":"Proceed?"}'
IFS= read -r control
test "$control" = '{"id":"shared","type":"get_state"}'
printf '%s\n' '{"type":"extension_ui_request","id":"notice","method":"notify","message":"Still waiting"}'
printf '%s\n' '{"id":"shared","type":"response","command":"get_state","success":true,"data":{"sessionId":"shared","isStreaming":true}}'
IFS= read -r answer
test "$answer" = '{"type":"extension_ui_response","id":"shared","confirmed":false}'
printf '%s\n' '{"type":"agent_settled","aborted":false}'
! IFS= read -r extra
`
	require.NoError(t, os.WriteFile(peer, []byte(native), 0o700))

	recordArgs := []string{"agent", "record", "--fixture-dir", fixture, "--cmd", peer, "--", "--mode", "rpc"}
	exchangePiDialog(t, root, env, recordArgs, "prompt-record", "shared", "shared")
	materialID := currentMaterialID(t, fixture)
	require.NotEmpty(t, materialID)
	material := fixtureMaterialRootByID(t, fixture, materialID)
	report := readJSON(t, filepath.Join(material, "report.json"))
	require.Equal(t, "consumer", report["provenance"])
	record := readJSON(t, filepath.Join(material, "raw", "transport.json"))
	require.Equal(t, "complete", record["captureStatus"])
	require.Equal(t, true, record["reaped"])
	original := readAllFiles(t, material)

	mockArgs := []string{"agent", "mock", "--fixture-dir", fixture, "--", "--mode", "rpc"}
	t.Run("answer original dialog after state response", func(t *testing.T) {
		exchangePiDialog(t, root, env, mockArgs, "prompt-live", "control-live", "shared")
		execution := latestExecution(t, fixture, "mock")
		require.Equal(t, "passed", connectionStatus(t, fixture, execution, "main"))
		connection := obj(array(execution["connections"])[0])
		receipt := readJSON(t, resolveFixtureReference(t, executionPackageRoot(t, fixture, execution), str(connection["receipt"])))
		require.Equal(t, true, receipt["fullConsumption"])
		require.Equal(t, true, receipt["clientEOF"])
		require.Equal(t, false, receipt["nativeLaunched"])
	})
	t.Run("control ID cannot answer the dialog", func(t *testing.T) {
		exchangePiDialog(t, root, env, mockArgs, "prompt-live", "control-live", "control-live")
		execution := latestExecution(t, fixture, "mock")
		require.Equal(t, "failed", connectionStatus(t, fixture, execution, "main"))
		connection := obj(array(execution["connections"])[0])
		receipt := readJSON(t, resolveFixtureReference(t, executionPackageRoot(t, fixture, execution), str(connection["receipt"])))
		require.Equal(t, false, receipt["fullConsumption"])
		require.Equal(t, "strict-replay", receipt["failureClass"])
	})
	require.Equal(t, original, readAllFiles(t, material))
}

func exchangePiDialog(t *testing.T, root string, env, args []string, promptID, controlID, answerID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("AGENT_SDK_TESTIFY_BIN"), args...)
	cmd.Dir, cmd.Env = root, env
	cmd.WaitDelay = 5 * time.Second
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	read := bufio.NewReader(stdout)
	send := func(frame string) {
		t.Helper()
		_, err := io.WriteString(stdin, frame+"\n")
		require.NoError(t, err)
	}
	receive := func(expected string) {
		t.Helper()
		line, err := read.ReadString('\n')
		require.NoError(t, err)
		require.JSONEq(t, expected, line)
	}
	send(`{"id":"` + promptID + `","type":"prompt","message":"Ask before proceeding."}`)
	receive(`{"id":"` + promptID + `","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}`)
	receive(`{"type":"extension_ui_request","id":"shared","method":"confirm","message":"Proceed?"}`)
	send(`{"id":"` + controlID + `","type":"get_state"}`)
	receive(`{"type":"extension_ui_request","id":"notice","method":"notify","message":"Still waiting"}`)
	receive(`{"id":"` + controlID + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"shared","isStreaming":true}}`)
	send(`{"type":"extension_ui_response","id":"` + answerID + `","confirmed":false}`)
	if answerID == "shared" {
		receive(`{"type":"agent_settled","aborted":false}`)
	}
	require.NoError(t, stdin.Close())
	remaining, err := io.ReadAll(read)
	require.NoError(t, err)
	require.Empty(t, remaining)
	err = cmd.Wait()
	if answerID == "shared" {
		require.NoError(t, err, stderr.String())
	} else {
		require.Error(t, err)
		require.NotEmpty(t, stderr.String())
	}
}
