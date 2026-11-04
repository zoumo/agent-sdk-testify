package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type replayReceiptExpectation struct {
	Provider        string
	RecordingSHA256 string
	PlanSHA256      string
	Material        string
	Frames          int
	RequirePlan     bool
	RequireExitCode bool
}

func validateExecutionID(id string) error {
	if id == "" || filepath.Base(id) != id || !filepath.IsLocal(id) {
		return errors.New("execution identity is not a local directory name")
	}
	return nil
}

func validateExecutionInventory(sessionDir string, index *Index) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(sessionDir, "executions"))
	if err != nil {
		return nil, err
	}
	indexed := make(map[string]bool, len(index.Executions))
	var errs []error
	for _, execution := range index.Executions {
		if indexed[execution.ID] {
			errs = append(errs, fmt.Errorf("duplicate execution identity %s in index", execution.ID))
		}
		indexed[execution.ID] = true
	}
	artifacts := make(map[string]bool, len(index.Artifacts))
	for _, artifact := range index.Artifacts {
		if artifact.ID == "" || artifacts[artifact.ID] {
			errs = append(errs, fmt.Errorf("duplicate or empty artifact identity %q in index", artifact.ID))
		}
		switch artifact.Kind {
		case "capture":
			if artifact.Action != "record" {
				errs = append(errs, fmt.Errorf("capture artifact %s does not identify a record action", artifact.ID))
			}
		case "material":
			if artifact.Action != "mock" && artifact.Action != "verify" {
				errs = append(errs, fmt.Errorf("material artifact %s has an invalid action", artifact.ID))
			}
		default:
			errs = append(errs, fmt.Errorf("artifact %s has invalid kind %q", artifact.ID, artifact.Kind))
		}
		artifacts[artifact.ID] = true
	}
	var orphans []string
	for _, entry := range entries {
		if entry.Name() == "index.json" {
			if !entry.Type().IsRegular() {
				errs = append(errs, errors.New("execution index is not a regular file"))
			}
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("unexpected execution entry %s", entry.Name()))
			continue
		}
		if !indexed[entry.Name()] {
			orphans = append(orphans, entry.Name())
			errs = append(errs, fmt.Errorf("execution directory %s is absent from execution index", entry.Name()))
		}
	}
	return orphans, errors.Join(errs...)
}

func validateIndexedConnections(indexed IndexExecution, execution Execution) error {
	if len(indexed.Connections) != len(execution.Connections) {
		return errors.New("execution connection inventory differs from index")
	}
	statuses := make(map[string]string, len(indexed.Connections))
	for _, connection := range indexed.Connections {
		if _, exists := statuses[connection.ID]; exists {
			return fmt.Errorf("duplicate execution connection %s in index", connection.ID)
		}
		statuses[connection.ID] = connection.Status
	}
	for _, connection := range execution.Connections {
		status, ok := statuses[connection.ID]
		if !ok {
			return fmt.Errorf("connection %s is absent from the execution index", connection.ID)
		}
		if status != "passed" {
			return fmt.Errorf("connection %s index status is %s", connection.ID, status)
		}
	}
	return nil
}

func validateConnectionInventory(execution Execution, artifact *corpus.Material, route Route) error {
	var ids []string
	if execution.Command == "record" {
		for _, connection := range route.Connections {
			ids = append(ids, connection.ID)
		}
	} else if artifact != nil {
		ids = append([]string(nil), artifact.Connections...)
	}
	connections, err := executionConnections(execution.ArtifactRoot, execution.Command, ids, artifact, route)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(connections, execution.Connections) {
		return errors.New("execution connections differ from their route or material")
	}
	return nil
}

func connectionRecordingPath(material, connection string) (string, error) {
	switch connection {
	case "main":
		return filepath.Join(material, "raw", "raw.jsonl"), nil
	case "probe":
		return filepath.Join(material, "remote-probe", "raw", "raw.jsonl"), nil
	default:
		return "", fmt.Errorf("connection %s has no recording phase", connection)
	}
}

func recordingFrameCount(path, provider string) (int, error) {
	source, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	document, readErr := recording.Read(source, provider, recording.Limits{})
	return len(document.Frames), errors.Join(readErr, source.Close())
}

func readEvidenceObject(path, label string) (tool.Object, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular unlinked file", label)
	}
	var value tool.Object
	if err := tool.ReadJSON(path, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func validateReplayReceipt(path string, expected replayReceiptExpectation) error {
	receipt, err := readEvidenceObject(path, "replay receipt")
	if err != nil {
		return err
	}
	if receipt["version"] != float64(1) ||
		receipt["status"] != "passed" ||
		receipt["provider"] != expected.Provider ||
		receipt["fullConsumption"] != true ||
		receipt["clientEOF"] != true ||
		receipt["cleanupComplete"] != true ||
		receipt["nativeLaunched"] != false ||
		receipt["error"] != nil {
		return errors.New("replay receipt does not prove strict full consumption, EOF and cleanup")
	}
	if expected.RecordingSHA256 == "" || receipt["recordingSHA256"] != expected.RecordingSHA256 {
		return errors.New("replay receipt recording differs from the selected material")
	}
	if expected.Frames > 0 && receipt["frames"] != float64(expected.Frames) {
		return errors.New("replay receipt frame count differs from the selected recording")
	}
	planSHA256 := tool.Str(receipt["planSHA256"])
	if expected.RequirePlan && planSHA256 != expected.PlanSHA256 {
		return errors.New("replay receipt plan differs from the selected material")
	}
	if planSHA256 != "" && planSHA256 != expected.PlanSHA256 {
		return errors.New("replay receipt plan differs from the selected material")
	}
	material := tool.Str(receipt["recording"])
	if expected.Material != "" && material != expected.Material {
		return errors.New("replay receipt material path differs from the execution")
	}
	if expected.RequireExitCode {
		if code, ok := receipt["exitCode"].(float64); !ok || code != 0 {
			return errors.New("replay receipt exit is absent or nonzero")
		}
	}
	return nil
}

func validateCaptureTransport(path string, frames int) error {
	transport, err := readEvidenceObject(path, "capture transport receipt")
	if err != nil {
		return err
	}
	if transport["captureStatus"] != "complete" ||
		transport["cleanupComplete"] != true || transport["clientEOF"] != true || transport["error"] != nil {
		return errors.New("transport does not prove complete capture, EOF and cleanup")
	}
	if transport["target"] == "remote-websocket" {
		if transport["remoteConnected"] != true || transport["remoteClosed"] != true ||
			transport["nativeLaunched"] != false || transport["reaped"] != false ||
			transport["nativeExitCode"] != nil || transport["nativeSignal"] != nil ||
			transport["stdinEOFAction"] != nil || transport["stdinEOFSignal"] != nil {
			return errors.New("remote transport does not prove owned connection cleanup without a native process")
		}
	} else {
		if transport["target"] != nil || transport["nativeLaunched"] != true || transport["reaped"] != true {
			return errors.New("transport does not prove native process launch and reaping")
		}
		if code, ok := transport["nativeExitCode"].(float64); !ok || code != 0 {
			return errors.New("native process exit is absent or nonzero")
		}
	}
	if frames > 0 && transport["frames"] != float64(frames) {
		return errors.New("capture transport frame count differs from the recorded stream")
	}
	return nil
}

func validateMockReceipts(receiptPath, processPath string, execution Execution, connection ExecutionConnection, expected replayReceiptExpectation) error {
	replayErr := validateReplayReceipt(receiptPath, expected)
	process, err := readEvidenceObject(processPath, "mock process receipt")
	if err != nil {
		return errors.Join(replayErr, err)
	}
	if process["schemaVersion"] != float64(1) ||
		process["status"] != "passed" ||
		process["sessionId"] != execution.SessionID ||
		process["executionId"] != execution.ExecutionID ||
		process["connectionId"] != connection.ID ||
		process["cwd"] != execution.Workspace ||
		process["error"] != nil {
		return errors.Join(replayErr, errors.New("mock process receipt is absent or unsuccessful"))
	}
	if code, ok := process["exitCode"].(float64); !ok || code != 0 {
		return errors.Join(replayErr, errors.New("mock process exit is absent or nonzero"))
	}
	return replayErr
}

// ValidateMockConnection checks the ordinary mock's durable protocol and process
// facts against its exact material. Official orchestration never writes receipts.
func ValidateMockConnection(provider string, execution Execution, connection ExecutionConnection) error {
	raw, err := connectionRecordingPath(execution.Artifact.Path, connection.ID)
	if err != nil {
		return err
	}
	hash, err := tool.FileDigest(raw)
	if err != nil {
		return err
	}
	frames, err := recordingFrameCount(raw, provider)
	if err != nil {
		return err
	}
	return validateMockReceipts(connection.Receipt, connection.ProcessReceipt, execution, connection, replayReceiptExpectation{
		Provider: provider, RecordingSHA256: hash, PlanSHA256: execution.Artifact.PlanSHA256,
		Material: execution.Artifact.Path, Frames: frames, RequirePlan: true, RequireExitCode: true,
	})
}

// ValidateMockExecution requires the normal completed index and connection
// layout, in addition to the descriptor's public launch contract.
func ValidateMockExecution(sessionDir string, session *Session, execution Execution) error {
	if err := execution.Validate(*session); err != nil {
		return err
	}
	if execution.Command != "mock" || execution.ArtifactRoot != filepath.Join(sessionDir, "executions", execution.ExecutionID, "mock") {
		return errors.New("unexpected mock execution layout")
	}
	if err := validateConnectionInventory(execution, &execution.Artifact, session.Routes["mock"]); err != nil {
		return err
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return err
	}
	digest, err := tool.FileDigest(filepath.Join(sessionDir, "executions", execution.ExecutionID, "execution.json"))
	if err != nil {
		return err
	}
	for _, indexed := range index.Executions {
		if indexed.ID != execution.ExecutionID {
			continue
		}
		if indexed.Command != "mock" || indexed.Status != "passed" || indexed.DescriptorSHA256 != digest {
			return errors.New("mock execution index is incomplete or differs")
		}
		return validateIndexedConnections(indexed, execution)
	}
	return errors.New("mock execution is absent from index")
}
