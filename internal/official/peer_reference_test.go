package official

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestReadPeerReferenceAcceptsCompleteEvidence(t *testing.T) {
	f := newPeerReferenceFixture(t)

	ref, execution, connection, err := f.read()

	require.NoError(t, err)
	require.Equal(t, f.execution.ExecutionID, execution.ExecutionID)
	require.Equal(t, "main", connection.ID)
	require.Equal(t, f.executionDigest, ref["executionSHA256"])
	require.Equal(t, f.execution.ExecutionID, ref["executionId"])
	require.Equal(t, "main", ref["connectionId"])
	require.Equal(t, f.started, ref["started"])
	require.Equal(t, f.finished, ref["finished"])
	require.Equal(t, f.process, ref["process"])
}

func TestReadPeerReferenceRejectsInvalidNotificationFraming(t *testing.T) {
	started, finished := newPeerReferenceFixture(t).notifications()
	for _, tc := range []struct {
		name string
		data string
		ref  bool
	}{
		{"missing final notification", started, true},
		{"malformed first notification", `{"status":`, false},
		{"malformed final notification", started + "\n{\"status\":", true},
		{"null first notification", "null\n" + finished, false},
		{"null final notification", started + "\nnull", true},
		{"extra notification", started + "\n" + finished + "\n{}\n", true},
		{"trailing garbage", started + "\n" + finished + "\ntrailing", true},
		{"oversize with complete prefix", started + "\n" + finished + strings.Repeat(" ", peerNotificationLimit), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeerReferenceFixture(t)
			f.writeNotifications([]byte(tc.data))
			ref, _, _, err := f.read()
			require.Error(t, err)
			require.Equal(t, tc.ref, ref != nil)
		})
	}
}

func TestReadPeerReferenceRequiresSettledProcess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*peerReferenceFixture)
	}{
		{"not started", func(f *peerReferenceFixture) { f.process["started"] = false }},
		{"process error", func(f *peerReferenceFixture) { f.process["error"] = "synthetic" }},
		{"null process error", func(f *peerReferenceFixture) { f.process["error"] = nil }},
		{"nonzero exit", func(f *peerReferenceFixture) { f.process["exitCode"] = float64(9) }},
		{"not reaped", func(f *peerReferenceFixture) { f.process["reaped"] = false }},
		{"incomplete cleanup", func(f *peerReferenceFixture) { f.process["cleanupComplete"] = false }},
		{"missing process ID", func(f *peerReferenceFixture) { delete(f.process, "pid") }},
		{"started status differs", func(f *peerReferenceFixture) { f.started["status"] = "passed" }},
		{"final status differs", func(f *peerReferenceFixture) { f.finished["status"] = "failed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeerReferenceFixture(t)
			tc.mutate(f)
			f.write()
			_, _, _, err := f.read()
			require.Error(t, err)
		})
	}
}

func TestReadPeerReferenceBindsEveryNotificationField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		value any
	}{
		{"process ID", "processId", float64(43)},
		{"session", "sessionId", "other-session"},
		{"execution", "executionId", "other-execution"},
		{"connection", "connectionId", "probe"},
		{"command", "command", "mock"},
		{"descriptor path", "execution", "/other/execution.json"},
		{"result path", "result", "/other/result.json"},
		{"receipt path", "receipt", "/other/receipt.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeerReferenceFixture(t)
			f.started[tc.field], f.finished[tc.field] = tc.value, tc.value
			f.write()
			_, _, _, err := f.read()
			require.Error(t, err)
		})
	}
}

func TestReadPeerReferenceRequiresExactDescriptorAndDurableResult(t *testing.T) {
	t.Run("started descriptor digest", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		f.started["executionSHA256"] = strings.Repeat("0", 64)
		f.write()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("final descriptor digest", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		f.finished["executionSHA256"] = strings.Repeat("0", 64)
		f.write()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("changed descriptor", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		f.execution.Target = "changed without notification"
		f.writeExecution()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("published final descriptor", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		startedDigest := f.executionDigest
		f.execution.Artifact.Files = map[string]string{"plan.json": strings.Repeat("a", 64)}
		f.writeExecution()
		f.started["executionSHA256"] = startedDigest
		f.finished["executionSHA256"] = strings.Repeat("0", 64)
		f.finished["material"] = tool.Object{"id": "material"}
		f.write()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("published final malformed start", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		f.execution.Artifact.Files = map[string]string{"plan.json": strings.Repeat("a", 64)}
		f.writeExecution()
		f.refreshNotificationDigest()
		f.started["executionSHA256"] = "wrong"
		f.finished["material"] = tool.Object{"id": "material"}
		f.write()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("published mock start", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		f.command = "mock"
		f.execution.Command = "mock"
		f.started["command"], f.finished["command"] = "mock", "mock"
		f.execution.Artifact.Files = map[string]string{"plan.json": strings.Repeat("a", 64)}
		f.writeExecution()
		f.refreshNotificationDigest()
		f.finished["material"] = tool.Object{"id": "material"}
		f.started["executionSHA256"] = strings.Repeat("0", 64)
		f.write()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("published final record connection", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		startedDigest := f.executionDigest
		f.execution.Artifact.Files = map[string]string{"plan.json": strings.Repeat("a", 64)}
		f.writeExecution()
		f.started["executionSHA256"] = startedDigest
		f.finished["executionSHA256"] = f.executionDigest
		f.finished["material"] = tool.Object{"id": "material"}
		f.write()
		_, _, _, err := f.read()
		require.NoError(t, err)
	})
	t.Run("published earlier record connection", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		startedDigest := f.executionDigest
		f.execution.Artifact.Files = map[string]string{"plan.json": strings.Repeat("a", 64)}
		f.writeExecution()
		f.started["executionSHA256"], f.finished["executionSHA256"] = startedDigest, startedDigest
		f.write()
		_, _, _, err := f.read()
		require.NoError(t, err)
	})
	t.Run("published earlier record connection changes binding", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		startedDigest := f.executionDigest
		f.execution.Artifact.Files = map[string]string{"plan.json": strings.Repeat("a", 64)}
		f.writeExecution()
		f.started["executionSHA256"] = startedDigest
		f.finished["executionSHA256"] = f.executionDigest
		f.write()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "descriptor digest")
	})
	t.Run("durable result differs", func(t *testing.T) {
		f := newPeerReferenceFixture(t)
		f.finished["tampered"] = true
		f.writeNotificationsFromFixture()
		_, _, _, err := f.read()
		require.ErrorContains(t, err, "durable result")
	})
}

func TestReadPeerReferenceRequiresMatchingDescriptorIdentityAndValidator(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*peerReferenceFixture)
	}{
		{"schema version", func(f *peerReferenceFixture) { f.execution.SchemaVersion++ }},
		{"execution identity", func(f *peerReferenceFixture) { f.execution.ExecutionID = "other-execution" }},
		{"session identity", func(f *peerReferenceFixture) { f.execution.SessionID = "other-session" }},
		{"command identity", func(f *peerReferenceFixture) { f.execution.Command = "mock" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeerReferenceFixture(t)
			tc.mutate(f)
			f.writeExecution()
			f.refreshNotificationDigest()
			f.write()
			_, _, _, err := f.read()
			require.Error(t, err)
		})
	}

	f := newPeerReferenceFixture(t)
	calls := 0
	_, _, _, err := f.invoke(func(lifecycle.Execution) error {
		calls++
		return errors.New("synthetic validator failure")
	})
	require.ErrorContains(t, err, "synthetic validator failure")
	require.Equal(t, 1, calls)
}

func TestReadPeerReferenceRequiresOneDeclaredConnection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		connections []lifecycle.ExecutionConnection
	}{
		{"absent connection", nil},
		{"duplicate connection", []lifecycle.ExecutionConnection{{ID: "main"}, {ID: "main"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPeerReferenceFixture(t)
			f.execution.Connections = tc.connections
			f.writeExecution()
			f.refreshNotificationDigest()
			f.write()
			_, _, _, err := f.invoke(func(lifecycle.Execution) error { return nil })
			require.Error(t, err)
		})
	}
}

func TestCollectPeerReferencesPreservesFailuresAndOneExecution(t *testing.T) {
	same, err := collectPeerReferences([]string{"main", "probe"}, func(id string) (tool.Object, error) {
		return tool.Object{"executionId": "execution", "connectionId": id}, nil
	})
	require.NoError(t, err)
	require.Len(t, same, 2)

	different, err := collectPeerReferences([]string{"main", "probe"}, func(id string) (tool.Object, error) {
		return tool.Object{"executionId": id}, nil
	})
	require.ErrorContains(t, err, "different executions")
	require.Len(t, different, 2)

	failed, err := collectPeerReferences([]string{"main", "probe"}, func(id string) (tool.Object, error) {
		if id == "main" {
			return tool.Object{"connectionId": id}, errors.New("synthetic failure")
		}
		return tool.Object{"executionId": "execution", "connectionId": id}, nil
	})
	require.ErrorContains(t, err, "main: synthetic failure")
	require.Len(t, failed, 2)
}

type peerReferenceFixture struct {
	t               *testing.T
	sessionDir      string
	session         *lifecycle.Session
	root            string
	execution       *lifecycle.Execution
	executionDigest string
	command         string
	process         tool.Object
	started         tool.Object
	finished        tool.Object
	executionPath   string
	resultPath      string
}

func newPeerReferenceFixture(t *testing.T) *peerReferenceFixture {
	t.Helper()
	sessionDir := filepath.Join(t.TempDir(), "session")
	root := filepath.Join(sessionDir, "official-records", "run", "driver")
	executionDir := filepath.Join(sessionDir, "executions", "execution")
	require.NoError(t, os.MkdirAll(filepath.Join(executionDir, "receipts"), 0o700))
	require.NoError(t, os.MkdirAll(root, 0o700))

	session := &lifecycle.Session{SessionID: "session"}
	receipt := filepath.Join(executionDir, "receipts", "main.json")
	execution := &lifecycle.Execution{
		SchemaVersion: lifecycle.SchemaVersion,
		ExecutionID:   "execution",
		SessionID:     "session",
		Command:       "record",
		Connections:   []lifecycle.ExecutionConnection{{ID: "main", Receipt: receipt}},
	}
	executionPath := filepath.Join(executionDir, "execution.json")
	resultPath := filepath.Join(executionDir, "receipts", "main.result.json")
	require.NoError(t, tool.WriteJSON(executionPath, execution))
	digest, err := tool.FileDigest(executionPath)
	require.NoError(t, err)

	process := tool.Object{
		"schemaVersion": float64(1), "pid": float64(42), "started": true,
		"exitCode": float64(0), "reaped": true, "cleanupComplete": true,
	}
	started := tool.Object{
		"status": "running", "sessionId": "session", "processId": float64(42),
		"executionId": "execution", "connectionId": "main", "command": "record",
		"execution": executionPath, "result": resultPath, "receipt": receipt,
		"executionSHA256": digest,
	}
	finished := tool.Object{
		"status": "passed", "sessionId": "session", "processId": float64(42),
		"executionId": "execution", "connectionId": "main", "command": "record",
		"execution": executionPath, "result": resultPath, "receipt": receipt,
		"executionSHA256": digest,
	}
	f := &peerReferenceFixture{
		t: t, sessionDir: sessionDir, session: session, root: root,
		execution: execution, executionDigest: digest,
		command: "record",
		process: process, started: started, finished: finished,
		executionPath: executionPath, resultPath: resultPath,
	}
	f.write()
	return f
}

func (f *peerReferenceFixture) write() {
	f.t.Helper()
	require.NoError(f.t, tool.WriteJSON(filepath.Join(f.root, "peer-main.process.json"), f.process))
	require.NoError(f.t, tool.WriteJSON(f.resultPath, f.finished))
	f.writeNotificationsFromFixture()
}

func (f *peerReferenceFixture) writeExecution() {
	f.t.Helper()
	require.NoError(f.t, tool.WriteJSON(f.executionPath, f.execution))
	digest, err := tool.FileDigest(f.executionPath)
	require.NoError(f.t, err)
	f.executionDigest = digest
}

func (f *peerReferenceFixture) refreshNotificationDigest() {
	f.started["executionSHA256"], f.finished["executionSHA256"] = f.executionDigest, f.executionDigest
}

func (f *peerReferenceFixture) writeNotificationsFromFixture() {
	f.t.Helper()
	started, finished := f.notifications()
	f.writeNotifications([]byte(started + "\n" + finished + "\n"))
}

func (f *peerReferenceFixture) notifications() (string, string) {
	f.t.Helper()
	started, err := json.Marshal(f.started)
	require.NoError(f.t, err)
	finished, err := json.Marshal(f.finished)
	require.NoError(f.t, err)
	return string(started), string(finished)
}

func (f *peerReferenceFixture) writeNotifications(data []byte) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.root, "peer-main.results.jsonl"), data, 0o600))
}

func (f *peerReferenceFixture) read() (tool.Object, *lifecycle.Execution, *lifecycle.ExecutionConnection, error) {
	return f.invoke(func(got lifecycle.Execution) error {
		require.Equal(f.t, *f.execution, got)
		return nil
	})
}

func (f *peerReferenceFixture) invoke(validate func(lifecycle.Execution) error) (tool.Object, *lifecycle.Execution, *lifecycle.ExecutionConnection, error) {
	return readPeerReference(f.sessionDir, f.session, f.root, "main", f.command, validate)
}
