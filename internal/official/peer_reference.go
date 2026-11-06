package official

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const peerNotificationLimit = 1 << 20

// readPeerReference decodes only notifications produced for one public action
// connection and binds them to the exact process, execution and durable result.
func readPeerReference(
	sessionDir string,
	session *lifecycle.Session,
	root, connectionID, command string,
	validate func(lifecycle.Execution) error,
) (tool.Object, *lifecycle.Execution, *lifecycle.ExecutionConnection, error) {
	var process tool.Object
	processPath := filepath.Join(root, "peer-"+connectionID+".process.json")
	if err := tool.ReadJSON(processPath, &process); err != nil {
		return nil, nil, nil, fmt.Errorf("protocol process not started or settled: %w", err)
	}
	started, finished, err := readPeerNotifications(filepath.Join(root, "peer-"+connectionID+".results.jsonl"))
	var ref tool.Object
	if started != nil {
		ref = tool.Object{"started": started, "process": process}
	}
	if finished != nil {
		ref["finished"] = finished
	}
	if err != nil {
		return ref, nil, nil, err
	}

	executionID := tool.Str(started["executionId"])
	if err := validateExecutionName(executionID); err != nil {
		return ref, nil, nil, err
	}
	ref["executionId"], ref["connectionId"] = executionID, connectionID
	executionDir := filepath.Join(sessionDir, "executions", executionID)
	executionPath := filepath.Join(executionDir, "execution.json")
	resultPath := filepath.Join(executionDir, "receipts", connectionID+".result.json")
	pid, ok := process["pid"].(float64)
	if !ok || pid <= 0 {
		return ref, nil, nil, errors.New("protocol process omitted its process ID")
	}
	binding := peerNotificationBinding{
		sessionID: session.SessionID, processID: pid, connectionID: connectionID,
		command: command, executionID: executionID, executionPath: executionPath, resultPath: resultPath,
		process: process,
	}
	if err := validatePeerNotifications(started, finished, binding); err != nil {
		return ref, nil, nil, err
	}

	var durable tool.Object
	if err := tool.ReadJSON(resultPath, &durable); err != nil {
		return ref, nil, nil, err
	}
	if !reflect.DeepEqual(finished, durable) {
		return ref, nil, nil, errors.New("notification differs from durable result")
	}

	execution, digest, err := loadPeerExecution(executionPath)
	if err != nil {
		return ref, nil, nil, err
	}
	if execution.SchemaVersion != lifecycle.SchemaVersion ||
		execution.ExecutionID != executionID ||
		execution.SessionID != session.SessionID ||
		execution.Command != command {
		return ref, nil, nil, errors.New("execution descriptor identity mismatch")
	}
	startedDigest, finishedDigest := tool.Str(started["executionSHA256"]), tool.Str(finished["executionSHA256"])
	if !validDigest(startedDigest) || !validDigest(finishedDigest) {
		return ref, nil, nil, errors.New("execution descriptor digest mismatch")
	}
	// Record publishes its material by binding the final artifact after early
	// connection notifications. The publishing notification and all mock
	// evidence remain exact. A pre-publication record notification is accepted
	// only when both notifications retain the same digest.
	publishedRecord := command == "record" && len(execution.Artifact.Files) > 0
	notifiedMaterial := tool.Obj(finished["material"])
	if publishedRecord && len(notifiedMaterial) == 0 {
		if startedDigest != finishedDigest {
			return ref, nil, nil, errors.New("execution descriptor digest mismatch")
		}
	} else if finishedDigest != digest || (startedDigest != digest && !publishedRecord) {
		return ref, nil, nil, errors.New("execution descriptor digest mismatch")
	}
	if err := validate(execution); err != nil {
		return ref, nil, nil, err
	}

	var connection *lifecycle.ExecutionConnection
	for i := range execution.Connections {
		if execution.Connections[i].ID != connectionID {
			continue
		}
		if connection != nil {
			return ref, nil, nil, fmt.Errorf("connection %s appears more than once", connectionID)
		}
		connection = &execution.Connections[i]
	}
	if connection == nil {
		return ref, nil, nil, fmt.Errorf("connection %s is absent from the execution", connectionID)
	}
	for _, notification := range []tool.Object{started, finished} {
		if notification["receipt"] != connection.Receipt {
			return ref, nil, nil, errors.New("connection receipt reference mismatch")
		}
	}
	ref["executionSHA256"], ref["connection"] = digest, *connection
	return ref, &execution, connection, nil
}

func readPeerNotifications(path string) (tool.Object, tool.Object, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, peerNotificationLimit+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, nil, err
	}
	if len(data) > peerNotificationLimit {
		return nil, nil, fmt.Errorf("notification file exceeds %d bytes", peerNotificationLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var started tool.Object
	if err := decoder.Decode(&started); err != nil {
		return nil, nil, fmt.Errorf("invalid first notification: %w", err)
	}
	if started == nil {
		return nil, nil, errors.New("first notification is null")
	}
	var finished tool.Object
	if err := decoder.Decode(&finished); err != nil {
		return started, nil, fmt.Errorf("invalid second notification: %w", err)
	}
	if finished == nil {
		return started, nil, errors.New("second notification is null")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return started, finished, errors.New("unexpected execution notifications")
	}
	return started, finished, nil
}

type peerNotificationBinding struct {
	sessionID, connectionID, command string
	executionID, executionPath       string
	resultPath                       string
	processID                        float64
	process                          tool.Object
}

func validatePeerNotifications(started, finished tool.Object, binding peerNotificationBinding) error {
	for _, notification := range []tool.Object{started, finished} {
		if notification["processId"] != binding.processID ||
			notification["sessionId"] != binding.sessionID ||
			notification["connectionId"] != binding.connectionID ||
			notification["executionId"] != binding.executionID ||
			notification["command"] != binding.command ||
			notification["execution"] != binding.executionPath ||
			notification["result"] != binding.resultPath {
			return errors.New("execution notification reference mismatch")
		}
	}
	if _, exists := binding.process["error"]; exists {
		return errors.New("protocol process failed or was not reaped")
	}
	if started["status"] != "running" ||
		finished["status"] != "passed" ||
		binding.process["started"] != true ||
		binding.process["exitCode"] != float64(0) ||
		binding.process["reaped"] != true ||
		binding.process["cleanupComplete"] != true {
		return errors.New("protocol process failed or was not reaped")
	}
	return nil
}

func validateExecutionName(id string) error {
	if id == "" || filepath.Base(id) != id || !filepath.IsLocal(id) {
		return errors.New("invalid execution reference")
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func loadPeerExecution(path string) (lifecycle.Execution, string, error) {
	var execution lifecycle.Execution
	info, err := os.Lstat(path)
	if err != nil {
		return execution, "", err
	}
	if !info.Mode().IsRegular() {
		return execution, "", fmt.Errorf("execution descriptor is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return execution, "", err
	}
	if err := json.Unmarshal(data, &execution); err != nil {
		return execution, "", err
	}
	return execution, tool.DigestBytes(data), nil
}

func collectPeerReferences(ids []string, read func(string) (tool.Object, error)) ([]tool.Object, error) {
	var refs []tool.Object
	var failures []error
	executionID := ""
	for _, id := range ids {
		ref, err := read(id)
		if ref != nil {
			refs = append(refs, ref)
		}
		if err == nil {
			if executionID == "" {
				executionID = tool.Str(ref["executionId"])
			} else if executionID != tool.Str(ref["executionId"]) {
				err = errors.New("official connections used different executions")
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return refs, errors.Join(failures...)
}
