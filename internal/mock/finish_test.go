package mock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func finishSession(t *testing.T, session string, env []string) (object, error) {
	t.Helper()
	data, err := sessionTool(t, env, "finish", "--session-dir", session).Output()
	var report object
	require.NoError(t, json.Unmarshal(data, &report), string(data))
	return report, err
}

func runPreparedMock(t *testing.T, session string, env []string) object {
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
	for name, tamper := range map[string]func(object){
		"recording hash": func(receipt object) { receipt["recordingSHA256"] = strings.Repeat("0", 64) },
		"frame count":    func(receipt object) { receipt["frames"] = 999 },
	} {
		t.Run(name, func(t *testing.T) {
			session, env := consumerSession(t)
			recordConsumerCapture(t, session, env)
			execution := runPreparedMock(t, session, env)
			connection := obj(array(execution["connections"])[0])
			path := str(connection["receipt"])

			var receipt object
			require.NoError(t, readJSON(path, &receipt))
			tamper(receipt)
			require.NoError(t, writeJSON(path, receipt))

			report, err := finishSession(t, session, env)
			require.Error(t, err)
			assert.Equal(t, "failed", report["status"])
			assert.Equal(t, true, report["cleanupComplete"])
			var mockResult object
			for _, value := range array(report["executions"]) {
				row := obj(value)
				if row["id"] == execution["executionId"] {
					mockResult = row
				}
			}
			require.NotEmpty(t, mockResult)
			assert.Equal(t, "failed", mockResult["status"])
			require.Len(t, array(mockResult["connections"]), 1)
			assert.Equal(t, "failed", obj(array(mockResult["connections"])[0])["status"])
			require.FileExists(t, path)
			require.FileExists(t, filepath.Join(session, "executions", str(execution["executionId"]), "finish.json"))
		})
	}
}

func TestPreparedFinishRejectsProcessReceiptTamper(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	execution := runPreparedMock(t, session, env)
	connection := obj(array(execution["connections"])[0])
	path := str(connection["processReceipt"])

	var receipt object
	require.NoError(t, readJSON(path, &receipt))
	receipt["cwd"] = filepath.Join(t.TempDir(), "different-workspace")
	require.NoError(t, writeJSON(path, receipt))

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
	executionID := str(execution["executionId"])

	var index object
	indexPath := filepath.Join(session, "executions", "index.json")
	require.NoError(t, readJSON(indexPath, &index))
	rows := array(index["executions"])
	kept := rows[:0]
	for _, value := range rows {
		if obj(value)["id"] != executionID {
			kept = append(kept, value)
		}
	}
	index["executions"] = kept
	require.NoError(t, writeJSON(indexPath, index))

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Contains(t, str(report["error"]), "absent from execution index")
	require.FileExists(t, filepath.Join(session, "executions", executionID, "execution.json"))
	require.FileExists(t, str(obj(array(execution["connections"])[0])["receipt"]))
}

func TestPreparedFinishCleanupFailureCanRetry(t *testing.T) {
	session, env := consumerSession(t)
	recordConsumerCapture(t, session, env)
	execution := runPreparedMock(t, session, env)

	var descriptor object
	require.NoError(t, readJSON(filepath.Join(session, "session.json"), &descriptor))
	resourceRoot := str(descriptor["resourceRoot"])
	require.NoError(t, os.WriteFile(filepath.Join(resourceRoot, ".owner"), []byte("different-session"), 0o600))

	callerFile := filepath.Join(filepath.Dir(session), "caller.txt")
	require.NoError(t, os.WriteFile(callerFile, []byte("preserve\n"), 0o600))
	capture := ""
	var index object
	require.NoError(t, readJSON(filepath.Join(session, "executions", "index.json"), &index))
	for _, value := range array(index["artifacts"]) {
		artifact := obj(value)
		if artifact["id"] == index["latestCapture"] {
			capture = str(artifact["path"])
		}
	}
	require.NotEmpty(t, capture)
	sourcePath := str(obj(descriptor["source"])["path"])
	receipt := str(obj(array(execution["connections"])[0])["receipt"])

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Equal(t, false, report["cleanupComplete"])
	require.DirExists(t, resourceRoot)

	require.NoError(t, os.WriteFile(filepath.Join(resourceRoot, ".owner"), []byte(str(descriptor["sessionId"])), 0o600))
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
	resultPath := filepath.Join(session, "executions", str(execution["executionId"]), "finish.json")
	require.NoError(t, os.Mkdir(resultPath, 0o700))

	report, err := finishSession(t, session, env)
	require.Error(t, err)
	assert.Equal(t, "failed", report["status"])
	assert.Contains(t, str(report["error"]), "write execution finish result")
}
