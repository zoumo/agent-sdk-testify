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
	mock := sessionTool(t, env, "agent", "mock", "--session-dir", session, "--", "app-server", "--listen", "stdio://")
	mock.Stdin = strings.NewReader(`{"id":1,"method":"ping"}` + "\n")
	var stderr strings.Builder
	mock.Stderr = &stderr
	data, err := mock.Output()
	require.NoError(t, err, "%s", stderr.String())
	require.JSONEq(t, `{"id":1,"result":{"synthetic":true}}`, string(data))
	execution, _ := latestSessionExecution(t, session, "mock")
	return execution
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
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "materials", "index.json"), &index))
	var current tool.Object
	require.NoError(t, tool.ReadJSON(filepath.Join(session, "current.json"), &current))
	for _, value := range tool.Array(index["materials"]) {
		artifact := tool.Obj(value)
		if artifact["id"] == current["materialId"] {
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
