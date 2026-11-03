package mock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func finishSession(t *testing.T, session string, env []string) (tool.Object, error) {
	t.Helper()
	data, err := sessionTool(t, env, "finish", "--session-dir", session).Output()
	var report tool.Object
	require.NoError(t, json.Unmarshal(data, &report), string(data))
	return report, err
}

func runPreparedMock(t *testing.T, session string, env []string) tool.Object {
	t.Helper()
	mock := sessionTool(t, env, "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	var stderr strings.Builder
	mock.Stderr = &stderr
	data, err := mock.Output()
	require.NoError(t, err, "%s", stderr.String())
	require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(data))
	execution, _ := latestSessionExecution(t, session, "mock")
	return execution
}

func TestPreparedFinishRejectsReplayReceiptTamper(t *testing.T) {
	for name, tamper := range map[string]func(tool.Object){
		"recording hash": func(receipt tool.Object) { receipt["recordingSHA256"] = strings.Repeat("0", 64) },
		"frame count":    func(receipt tool.Object) { receipt["frames"] = 999 },
	} {
		t.Run(name, func(t *testing.T) {
			session, env := consumerSession(t)
			recordConsumerCapture(t, session, env)
			execution := runPreparedMock(t, session, env)
			connection := tool.Obj(tool.Array(execution["connections"])[0])
			path := tool.Str(connection["receipt"])

			var receipt tool.Object
			require.NoError(t, tool.ReadJSON(path, &receipt))
			tamper(receipt)
			require.NoError(t, tool.WriteJSON(path, receipt))

			report, err := finishSession(t, session, env)
			require.Error(t, err)
			assert.Equal(t, "failed", report["status"])
			assert.Equal(t, true, report["cleanupComplete"])
			var mockResult tool.Object
			for _, value := range tool.Array(report["executions"]) {
				row := tool.Obj(value)
				if row["id"] == execution["executionId"] {
					mockResult = row
				}
			}
			require.NotEmpty(t, mockResult)
			assert.Equal(t, "failed", mockResult["status"])
			require.Len(t, tool.Array(mockResult["connections"]), 1)
			assert.Equal(t, "failed", tool.Obj(tool.Array(mockResult["connections"])[0])["status"])
			require.FileExists(t, path)
			require.FileExists(t, filepath.Join(session, "executions", tool.Str(execution["executionId"]), "finish.json"))
		})
	}
}

func TestPreparedFinishRejectsProcessReceiptTamper(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	execution := runPreparedMock(t, session, env)
	connection := tool.Obj(tool.Array(execution["connections"])[0])
	path := tool.Str(connection["processReceipt"])

	var receipt tool.Object
	require.NoError(t, tool.ReadJSON(path, &receipt))
	receipt["cwd"] = filepath.Join(t.TempDir(), "different-workspace")
	require.NoError(t, tool.WriteJSON(path, receipt))

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Equal(t, true, report["cleanupComplete"])
	require.FileExists(t, path)
}

func TestPreparedFinishRejectsExecutionMissingFromIndex(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	execution := runPreparedMock(t, session, env)
	executionID := tool.Str(execution["executionId"])

	var index tool.Object
	indexPath := filepath.Join(session, "executions", "index.json")
	require.NoError(t, tool.ReadJSON(indexPath, &index))
	rows := tool.Array(index["executions"])
	kept := rows[:0]
	for _, value := range rows {
		if tool.Obj(value)["id"] != executionID {
			kept = append(kept, value)
		}
	}
	index["executions"] = kept
	require.NoError(t, tool.WriteJSON(indexPath, index))

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Contains(t, tool.Str(report["error"]), "absent from execution index")
	require.FileExists(t, filepath.Join(session, "executions", executionID, "execution.json"))
	require.FileExists(t, tool.Str(tool.Obj(tool.Array(execution["connections"])[0])["receipt"]))
}

func TestPreparedFinishCleanupFailureCanRetry(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	execution := runPreparedMock(t, session, env)

	var descriptor tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "session.json"), &descriptor))
	resourceRoot := tool.Str(descriptor["resourceRoot"])
	require.NoError(t, os.WriteFile(filepath.Join(resourceRoot, ".owner"), []byte("different-session"), 0o600))

	callerFile := filepath.Join(filepath.Dir(session), "caller.txt")
	require.NoError(t, os.WriteFile(callerFile, []byte("preserve\n"), 0o600))
	capture := ""
	var index tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "executions", "index.json"), &index))
	for _, value := range tool.Array(index["artifacts"]) {
		artifact := tool.Obj(value)
		if artifact["id"] == index["latestCapture"] {
			capture = tool.Str(artifact["path"])
		}
	}
	require.NotEmpty(t, capture)
	sourcePath := tool.Str(tool.Obj(descriptor["source"])["path"])
	receipt := tool.Str(tool.Obj(tool.Array(execution["connections"])[0])["receipt"])

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Equal(t, false, report["cleanupComplete"])
	require.DirExists(t, resourceRoot)

	require.NoError(t, os.WriteFile(filepath.Join(resourceRoot, ".owner"), []byte(tool.Str(descriptor["sessionId"])), 0o600))
	report, err = finishSession(t, session, env)
	require.NoError(t, err)
	assert.Equal(t, "passed", report["status"])
	assert.Equal(t, true, report["cleanupComplete"])
	require.NoDirExists(t, resourceRoot)
	require.FileExists(t, filepath.Join(session, "session.json"))
	require.FileExists(t, sourcePath)
	require.FileExists(t, filepath.Join(capture, "manifest.json"))
	require.FileExists(t, receipt)
	require.FileExists(t, callerFile)

	report, err = finishSession(t, session, env)
	require.NoError(t, err)
	assert.Equal(t, "passed", report["status"])
	assert.Equal(t, true, report["cleanupComplete"])
}

func TestPreparedFinishResultWriteFailureIsReported(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	execution := runPreparedMock(t, session, env)
	resultPath := filepath.Join(session, "executions", tool.Str(execution["executionId"]), "finish.json")
	require.NoError(t, os.Mkdir(resultPath, 0o700))

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Contains(t, tool.Str(report["error"]), "write execution finish result")
}
