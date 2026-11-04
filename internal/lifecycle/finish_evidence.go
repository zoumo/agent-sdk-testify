package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/protocol/recording"
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

func validateConnectionInventory(execution Execution, artifact *Artifact, route Route) error {
	var ids []string
	if execution.Command == "record" {
		for _, connection := range route.Connections {
			ids = append(ids, connection.ID)
		}
	} else if artifact != nil {
		ids = append([]string(nil), artifact.Connections...)
	}
	connections, receipts, err := executionConnections(execution.ArtifactRoot, execution.Command, ids, artifact, route)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(connections, execution.Connections) {
		return errors.New("execution connections differ from their route or material")
	}
	if !slices.Equal(receipts, execution.Receipts) {
		return errors.New("execution receipt inventory differs from its connections")
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
	if err := validateTransport(path); err != nil {
		return err
	}
	transport, err := readEvidenceObject(path, "capture transport receipt")
	if err != nil {
		return err
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

func validatePeerLifecycle(root, connection string) error {
	lifecycle, err := readEvidenceObject(filepath.Join(root, "peer-"+connection+"-lifecycle.json"), "peer lifecycle receipt")
	if err != nil {
		return err
	}
	if lifecycle["schemaVersion"] != float64(1) ||
		lifecycle["exitCode"] != float64(0) ||
		lifecycle["reaped"] != true ||
		lifecycle["cleanupComplete"] != true ||
		lifecycle["error"] != nil {
		return errors.New("peer process did not exit and clean up successfully")
	}
	return nil
}

func validateAdmission(execution Execution, root string) error {
	args := map[string]string{
		"--index":     "",
		"--scenarios": "",
		"--archive":   "",
	}
	counts := map[string]int{}
	for i, arg := range execution.Args {
		if _, ok := args[arg]; !ok {
			continue
		}
		if i+1 >= len(execution.Args) {
			return fmt.Errorf("%s has no value", arg)
		}
		counts[arg]++
		args[arg] = execution.Args[i+1]
	}
	evidence, err := readEvidenceObject(filepath.Join(root, "verification.json"), "verification evidence")
	if err != nil {
		return err
	}
	evidenceAdmission := tool.Obj(evidence["admission"])
	if execution.Admission == nil {
		for flag := range args {
			if counts[flag] != 0 {
				return fmt.Errorf("%s is present without persisted admission", flag)
			}
		}
		if len(evidenceAdmission) != 0 {
			return errors.New("verification evidence has admission without its execution descriptor")
		}
		return nil
	}
	if execution.Command != "verify" {
		return errors.New("only verify executions may persist admission parameters")
	}
	expected := []string{execution.Admission.Index, execution.Admission.Scenarios, execution.Admission.Archive}
	for _, value := range expected {
		if value == "" || !filepath.IsAbs(value) {
			return errors.New("persisted admission paths must be absolute")
		}
	}
	for flag, value := range map[string]string{
		"--index":     execution.Admission.Index,
		"--scenarios": execution.Admission.Scenarios,
		"--archive":   execution.Admission.Archive,
	} {
		if counts[flag] != 1 || args[flag] != value {
			return fmt.Errorf("persisted admission %s differs from the execution arguments", flag)
		}
	}
	persisted := Admission{
		Index:     tool.Str(evidenceAdmission["index"]),
		Scenarios: tool.Str(evidenceAdmission["scenarios"]),
		Archive:   tool.Str(evidenceAdmission["archive"]),
	}
	if persisted != *execution.Admission {
		return errors.New("persisted admission differs from the verification evidence")
	}
	return nil
}

func validateEvidenceHashes(root string, hashes tool.Object) error {
	if len(hashes) == 0 {
		return errors.New("verification evidence omits its file hashes")
	}
	var errs []error
	for name, value := range hashes {
		digest := tool.Str(value)
		if digest == "" {
			errs = append(errs, fmt.Errorf("verification evidence has no hash for %s", name))
			continue
		}
		if err := corpus.VerifyFile(root, filepath.FromSlash(name), digest); err != nil {
			errs = append(errs, fmt.Errorf("verification evidence %s: %w", name, err))
		}
	}
	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	return nil
}
