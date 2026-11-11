package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// mockConnectionEvidence contains validated durable facts. Replay, process and
// result describe different stages: a successful replay can precede a failed
// finalize, and a passed connection can receive a failed aggregate result.
type mockConnectionEvidence struct {
	replayStatus, processStatus, resultStatus                   string
	fullConsumption, clientEOF, nativeLaunched, cleanupComplete bool
	failure, resultFailure                                      FailureClass
	message, resultMessage                                      string
	exitCode, replayExitCode                                    *int
}

func readMockConnectionEvidence(provider string, execution Execution, connection ExecutionConnection) (*mockConnectionEvidence, error) {
	transport := MockTransport(connection.Transport)
	if transport != MockTransportProcess && transport != MockTransportInProcess {
		return nil, fmt.Errorf("mock connection transport %q is invalid", transport)
	}
	replay, err := readMockReplayReceipt(connection.Receipt, transport, provider, execution.Artifact, connection)
	if err != nil {
		return nil, err
	}
	resultPath := filepath.Join(filepath.Dir(execution.ArtifactRoot), "receipts", connection.ID+".result.json")
	result, err := readMockResult(resultPath, execution, connection, transport)
	if err != nil {
		return nil, err
	}
	facts := &mockConnectionEvidence{
		replayStatus: tool.Str(replay["status"]), resultStatus: tool.Str(result["status"]),
		fullConsumption: replay["fullConsumption"] == true, clientEOF: replay["clientEOF"] == true,
		nativeLaunched: replay["nativeLaunched"] == true, cleanupComplete: replay["cleanupComplete"] == true,
		failure: FailureClass(tool.Str(replay["failureClass"])), message: tool.Str(replay["error"]),
		resultFailure: FailureClass(tool.Str(result["failureClass"])), resultMessage: tool.Str(result["error"]),
	}
	if code, ok := replay["exitCode"].(float64); ok {
		n := int(code)
		facts.replayExitCode = &n
	}
	if transport == MockTransportProcess {
		if connection.ProcessReceipt == "" {
			return nil, errors.New("mock process receipt path is absent")
		}
		process, code, err := readMockProcessReceipt(connection.ProcessReceipt, execution, connection.ID)
		if err != nil {
			return nil, err
		}
		facts.processStatus, facts.exitCode = tool.Str(process["status"]), code
	}
	return facts, nil
}

func (f *mockConnectionEvidence) requirePassedReceipts() error {
	if f.replayStatus != "passed" {
		return errors.New("mock replay receipt status differs from passed connection status")
	}
	if f.processStatus != "" && f.processStatus != "passed" {
		return errors.New("mock process receipt status differs from passed connection status")
	}
	return nil
}

// Missing process transport declarations remain compatible with process v1
// evidence. A declaration, when present, must still have the correct type.
func validateMockTransport(value tool.Object, transport MockTransport, label string) error {
	if declared, present := value["transport"]; present {
		if text, ok := declared.(string); !ok || (text != "" && text != string(transport)) {
			return fmt.Errorf("%s transport differs from connection transport %q", label, transport)
		}
	}
	return nil
}

func validateMockDiagnostics(value tool.Object, label string) error {
	for _, field := range []string{"error", "failureClass"} {
		if v, present := value[field]; present && v != nil {
			text, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s field %q is not a string", label, field)
			}
			if value["status"] == "passed" && (field == "error" || text != "") {
				return fmt.Errorf("%s passed status conflicts with %s", label, field)
			}
		}
	}
	return nil
}

func readMockReplayReceipt(path string, transport MockTransport, provider string, artifact corpus.Material, connection ExecutionConnection) (tool.Object, error) {
	receipt, err := readEvidenceObject(path, "mock replay receipt")
	if err != nil {
		return nil, fmt.Errorf("mock replay receipt: %w", err)
	}
	if receipt["schemaVersion"] != float64(1) || receipt["version"] != float64(1) {
		return nil, errors.New("mock replay receipt schema or version differs from the contract")
	}
	for _, field := range []string{"nativeLaunched", "fullConsumption", "clientEOF", "cleanupComplete"} {
		if _, ok := receipt[field].(bool); !ok {
			return nil, fmt.Errorf("mock replay receipt field %q is not boolean", field)
		}
	}
	receiptStatus := tool.Str(receipt["status"])
	if receiptStatus != "passed" && receiptStatus != "failed" {
		return nil, fmt.Errorf("mock replay receipt status %q is invalid", receiptStatus)
	}
	if err := validateMockDiagnostics(receipt, "mock replay receipt"); err != nil {
		return nil, err
	}
	if tool.Str(receipt["provider"]) != provider {
		return nil, fmt.Errorf("mock replay receipt provider %q differs from fixture agent %q", tool.Str(receipt["provider"]), provider)
	}
	if filepath.Clean(tool.Str(receipt["recording"])) != artifact.Path {
		return nil, errors.New("mock replay receipt recording path differs from execution material")
	}
	if tool.Str(receipt["planSHA256"]) != artifact.PlanSHA256 {
		return nil, errors.New("mock replay receipt plan differs from execution material")
	}
	recordingPath, err := connectionRecordingPath(artifact.Path, connection.ID)
	if err != nil {
		return nil, err
	}
	recordingSHA256, err := tool.FileDigest(recordingPath)
	if err != nil {
		return nil, err
	}
	if connection.RecordingSHA256 == "" ||
		connection.RecordingSHA256 != recordingSHA256 ||
		tool.Str(receipt["recordingSHA256"]) != recordingSHA256 {
		return nil, errors.New("mock replay receipt recording hash differs from execution connection")
	}
	frames, err := recordingFrameCount(recordingPath, provider)
	if err != nil {
		return nil, err
	}
	if receipt["frames"] != float64(frames) {
		return nil, errors.New("mock replay receipt frame count differs from execution material")
	}
	if err := validateMockTransport(receipt, transport, "mock replay receipt"); err != nil {
		return nil, err
	}
	if transport == MockTransportInProcess && tool.Str(receipt["transport"]) != string(transport) {
		return nil, errors.New("mock replay receipt omits the in-process transport")
	}
	if transport == MockTransportInProcess {
		for _, field := range []string{"processId", "processReceipt", "command", "argv", "pid", "executable", "launcher", "exitCode"} {
			if _, present := receipt[field]; present {
				return nil, fmt.Errorf("in-process mock replay receipt declares process fact %q", field)
			}
		}
	}
	if receipt["nativeLaunched"] != false {
		return nil, errors.New("mock replay receipt does not prove an in-process mock")
	}
	if receiptStatus == "passed" &&
		(receipt["fullConsumption"] != true || receipt["clientEOF"] != true || receipt["cleanupComplete"] != true) {
		return nil, errors.New("mock replay receipt does not prove full consumption, client EOF and cleanup")
	}
	if code, present := receipt["exitCode"]; present {
		n, ok := code.(float64)
		if !ok || n != float64(int(n)) || (receiptStatus == "passed" && n != 0) {
			return nil, errors.New("mock replay receipt exit code is invalid or nonzero for a passed receipt")
		}
	}
	return receipt, nil
}

func readMockResult(path string, execution Execution, connection ExecutionConnection, transport MockTransport) (tool.Object, error) {
	result, err := readEvidenceObject(path, "mock result")
	if err != nil {
		return nil, fmt.Errorf("mock result: %w", err)
	}
	if result["schemaVersion"] != float64(1) ||
		result["fixtureId"] != execution.FixtureID ||
		result["executionId"] != execution.ExecutionID {
		return nil, errors.New("mock result execution identity differs from execution")
	}
	if result["connectionId"] != connection.ID {
		return nil, errors.New("mock result connection identity differs from execution")
	}
	if resultStatus := tool.Str(result["status"]); resultStatus != "passed" && resultStatus != "failed" {
		return nil, fmt.Errorf("mock result status %q is invalid", resultStatus)
	}
	if err := validateMockDiagnostics(result, "mock result"); err != nil {
		return nil, err
	}
	expectedExecution := filepath.Join(filepath.Dir(filepath.Dir(path)), "execution.json")
	if filepath.Clean(tool.Str(result["execution"])) != expectedExecution {
		return nil, errors.New("mock result execution path differs from execution")
	}
	digest, err := tool.FileDigest(expectedExecution)
	if err != nil {
		return nil, err
	}
	if result["executionSHA256"] != digest {
		return nil, errors.New("mock result execution hash differs from execution")
	}
	if filepath.Clean(tool.Str(result["result"])) != path {
		return nil, errors.New("mock result path differs from execution")
	}
	if filepath.Clean(tool.Str(result["receipt"])) != connection.Receipt {
		return nil, errors.New("mock result receipt path differs from execution")
	}
	if err := validateMockTransport(result, transport, "mock result"); err != nil {
		return nil, err
	}
	if transport == MockTransportInProcess {
		for _, field := range []string{"processId", "processReceipt", "command", "argv", "pid", "executable", "launcher", "exitCode", "nativeLaunched"} {
			if _, present := result[field]; present {
				return nil, fmt.Errorf("in-process mock result declares process fact %q", field)
			}
		}
		if tool.Str(result["transport"]) != string(transport) {
			return nil, errors.New("in-process mock result omits its transport")
		}
		processPath := filepath.Join(filepath.Dir(path), connection.ID+".process.json")
		if _, err := os.Lstat(processPath); err == nil {
			return nil, errors.New("in-process mock result has a process receipt file")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		if processID, ok := result["processId"].(float64); !ok || processID <= 0 || processID != float64(int(processID)) {
			return nil, errors.New("process mock result omits its process identity")
		}
		if tool.Str(result["command"]) != execution.Command {
			return nil, errors.New("process mock result command differs from execution")
		}
		if connection.ProcessReceipt == "" || filepath.Clean(tool.Str(result["processReceipt"])) != connection.ProcessReceipt {
			return nil, errors.New("process mock result receipt differs from execution")
		}
	}
	material := tool.Obj(result["material"])
	if material["id"] != execution.Artifact.ID ||
		material["path"] != execution.Artifact.Path ||
		material["provenance"] != execution.Artifact.Provenance ||
		material["planSHA256"] != execution.Artifact.PlanSHA256 ||
		material["manifestSHA256"] != execution.Artifact.ManifestSHA256 ||
		material["recordingSHA256"] != execution.Artifact.RecordingSHA256 {
		return nil, errors.New("mock result material identity differs from execution")
	}
	return result, nil
}

func readMockProcessReceipt(path string, execution Execution, connectionID string) (tool.Object, *int, error) {
	process, err := readEvidenceObject(path, "mock process receipt")
	if err != nil {
		return nil, nil, fmt.Errorf("mock process receipt: %w", err)
	}
	if process["schemaVersion"] != float64(1) ||
		process["fixtureId"] != execution.FixtureID ||
		process["executionId"] != execution.ExecutionID {
		return nil, nil, errors.New("mock process receipt execution identity differs from execution")
	}
	if process["connectionId"] != connectionID {
		return nil, nil, errors.New("mock process receipt connection identity differs from execution")
	}
	processStatus := tool.Str(process["status"])
	if processStatus != "passed" && processStatus != "failed" {
		return nil, nil, fmt.Errorf("mock process receipt status %q is invalid", processStatus)
	}
	if err := validateMockDiagnostics(process, "mock process receipt"); err != nil {
		return nil, nil, err
	}
	if err := validateMockTransport(process, MockTransportProcess, "mock process receipt"); err != nil {
		return nil, nil, err
	}
	code, ok := process["exitCode"].(float64)
	if !ok || code != float64(int(code)) {
		return nil, nil, errors.New("mock process receipt exit code is absent")
	}
	exitCode := int(code)
	if processStatus == "passed" && exitCode != 0 {
		return nil, nil, fmt.Errorf("mock process receipt exit code %d is nonzero for a passed connection", exitCode)
	}
	return process, &exitCode, nil
}
