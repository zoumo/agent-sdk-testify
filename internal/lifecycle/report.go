package lifecycle

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// MockTransport distinguishes process and in-process mock connections.
type MockTransport string

const (
	MockTransportInProcess MockTransport = "in-process"
	MockTransportProcess   MockTransport = "process"
)

// ExecutionReport is the validated terminal snapshot returned with a mock
// connection result. It covers every connection declared by the execution.
type ExecutionReport struct {
	FixtureID       string
	ExecutionID     string
	Status          ExecutionStatus
	Complete        bool
	Material        corpus.Material
	ExecutionSHA256 string
	ExecutionPath   string
	Connections     []ConnectionReport
}

// ConnectionReport is one connection's durable protocol and process facts.
type ConnectionReport struct {
	ID     string
	Status ConnectionStatus

	Transport       MockTransport
	FullConsumption bool
	ClientEOF       bool
	NativeLaunched  bool
	CleanupComplete bool
	ExitCode        *int
	Failure         FailureClass
	Error           string

	ReceiptPath        string
	ProcessReceiptPath string
	ResultPath         string
}

func ReadMockExecutionReport(fixtureDir, executionID string) (*ExecutionReport, error) {
	if err := validateExecutionID(executionID); err != nil {
		return nil, err
	}
	fixtureDir, err := CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, err
	}
	fixture, err := readFixtureUnlocked(fixtureDir)
	if err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return nil, err
	}
	indexed, err := findIndexExecution(index, executionID)
	if err != nil {
		return nil, err
	}
	if indexed == nil {
		return nil, errors.New("mock execution is absent from index")
	}
	if indexed.Command != "mock" {
		return nil, errors.New("execution is not a mock execution")
	}
	status := ExecutionStatus(indexed.Status)
	if status != ExecutionStatusOpen && status != ExecutionStatusPassed && status != ExecutionStatusFailed {
		return nil, fmt.Errorf("mock execution status %q is invalid", indexed.Status)
	}
	dir := ExecutionDir(fixtureDir, executionID)
	descriptorPath := filepath.Join(dir, "execution.json")
	descriptorSHA256, err := tool.FileDigest(descriptorPath)
	if err != nil {
		return nil, err
	}
	if indexed.DescriptorSHA256 != descriptorSHA256 {
		return nil, errors.New("mock execution descriptor differs from index")
	}
	var execution Execution
	if err := tool.ReadJSON(descriptorPath, &execution); err != nil {
		return nil, err
	}
	if err := execution.Validate(*fixture); err != nil {
		return nil, err
	}
	if err := validateMockExecutionContract(fixture, fixtureDir, index, *indexed, execution); err != nil {
		return nil, err
	}
	statuses, err := reportConnectionStatuses(*indexed, execution)
	if err != nil {
		return nil, err
	}
	if err := validateExecutionStatus(status, statuses); err != nil {
		return nil, err
	}
	report := &ExecutionReport{
		FixtureID:       execution.FixtureID,
		ExecutionID:     execution.ExecutionID,
		Status:          status,
		Complete:        status == ExecutionStatusPassed || status == ExecutionStatusFailed,
		Material:        cloneTransactionMaterial(execution.Artifact),
		ExecutionSHA256: descriptorSHA256,
		ExecutionPath:   descriptorPath,
		Connections:     make([]ConnectionReport, 0, len(execution.Connections)),
	}
	for _, connection := range execution.Connections {
		connectionStatus := ConnectionStatus(statuses[connection.ID])
		item := ConnectionReport{
			ID:                 connection.ID,
			Status:             connectionStatus,
			Transport:          MockTransport(connection.Transport),
			ReceiptPath:        connection.Receipt,
			ProcessReceiptPath: connection.ProcessReceipt,
			ResultPath:         filepath.Join(dir, "receipts", connection.ID+".result.json"),
		}
		if item.Status == ConnectionStatusPassed || item.Status == ConnectionStatusFailed {
			receipt, err := readReportReceipt(
				item.ReceiptPath,
				item.Status,
				item.Transport,
				fixture.AgentType,
				execution.Artifact,
				connection,
			)
			if err != nil {
				return nil, err
			}
			item.FullConsumption = receipt["fullConsumption"] == true
			item.ClientEOF = receipt["clientEOF"] == true
			item.NativeLaunched = receipt["nativeLaunched"] == true
			item.CleanupComplete = receipt["cleanupComplete"] == true
			if class := tool.Str(receipt["failureClass"]); class != "" {
				item.Failure = FailureClass(class)
			}
			item.Error = tool.Str(receipt["error"])
			result, err := readReportResult(item.ResultPath, execution, connection, item.Transport)
			if err != nil {
				return nil, err
			}
			if item.Status == ConnectionStatusFailed && item.Failure == "" {
				item.Failure = FailureClass(tool.Str(result["failureClass"]))
			}
			if item.Status == ConnectionStatusFailed && item.Error == "" {
				item.Error = tool.Str(result["error"])
			}
			if connection.Transport == ConnectionTransportProcess {
				if item.ProcessReceiptPath == "" {
					return nil, errors.New("mock process receipt path is absent")
				}
				exitCode, err := readReportProcessReceipt(item.ProcessReceiptPath, execution, connection.ID, item.Status)
				if err != nil {
					return nil, err
				}
				item.ExitCode = exitCode
			}
		}
		report.Connections = append(report.Connections, item)
	}
	return report, nil
}

func validateMockExecutionContract(fixture *Fixture, fixtureDir string, index *Index, indexed IndexExecution, execution Execution) error {
	if execution.Command != "mock" || indexed.Command != "mock" {
		return errors.New("execution descriptor is not a mock execution")
	}
	if execution.ExecutionID != indexed.ID ||
		execution.FixtureID != fixture.FixtureID ||
		execution.FixtureID != index.FixtureID {
		return errors.New("mock execution descriptor identity differs from index")
	}
	if execution.ArtifactRoot != filepath.Join(ExecutionDir(fixtureDir, indexed.ID), "mock") {
		return errors.New("mock execution artifact root differs from execution layout")
	}
	if err := validateExecutionMaterial(index, execution); err != nil {
		return err
	}
	if _, err := inspectMaterial(fixture, execution.Artifact); err != nil {
		return fmt.Errorf("mock execution material: %w", err)
	}
	if err := validateConnectionInventory(execution, &execution.Artifact, fixture.Routes["mock"]); err != nil {
		return fmt.Errorf("mock execution connection inventory: %w", err)
	}
	return nil
}

func validateExecutionMaterial(index *Index, execution Execution) error {
	for _, material := range index.Artifacts {
		if material.ID != execution.Artifact.ID {
			continue
		}
		if !reflect.DeepEqual(material, execution.Artifact) {
			return errors.New("mock execution material identity differs from material index")
		}
		return nil
	}
	return errors.New("mock execution material is absent from material index")
}

func reportConnectionStatuses(indexed IndexExecution, execution Execution) (map[string]string, error) {
	statuses := make(map[string]string, len(indexed.Connections))
	for _, connection := range indexed.Connections {
		if _, exists := statuses[connection.ID]; exists {
			return nil, fmt.Errorf("duplicate execution connection %s in index", connection.ID)
		}
		statuses[connection.ID] = connection.Status
	}
	if len(indexed.Connections) != len(execution.Connections) {
		return nil, errors.New("execution connection inventory differs from index")
	}
	for _, connection := range execution.Connections {
		status, ok := statuses[connection.ID]
		if !ok {
			return nil, errors.New("execution connection inventory differs from index")
		}
		switch ConnectionStatus(status) {
		case ConnectionStatusPending, ConnectionStatusRunning, ConnectionStatusPassed, ConnectionStatusFailed:
		default:
			return nil, fmt.Errorf("execution connection %s has invalid status %q", connection.ID, status)
		}
	}
	return statuses, nil
}

func validateExecutionStatus(status ExecutionStatus, statuses map[string]string) error {
	hasIncomplete := false
	hasFailed := false
	for id, value := range statuses {
		connectionStatus := ConnectionStatus(value)
		if connectionStatus == ConnectionStatusPending || connectionStatus == ConnectionStatusRunning {
			hasIncomplete = true
		}
		if connectionStatus == ConnectionStatusFailed {
			hasFailed = true
		}
		if status == ExecutionStatusPassed && connectionStatus != ConnectionStatusPassed {
			return fmt.Errorf("mock execution status %q conflicts with connection %s status %q", status, id, connectionStatus)
		}
	}
	switch status {
	case ExecutionStatusPassed:
	case ExecutionStatusOpen:
		if !hasIncomplete {
			return errors.New("mock execution status is open but every connection is terminal")
		}
	case ExecutionStatusFailed:
		if hasIncomplete {
			return errors.New("mock execution status is failed while a connection is still active")
		}
		if !hasFailed {
			return errors.New("mock execution status is failed without a failed connection")
		}
	default:
		return fmt.Errorf("mock execution status %q is invalid", status)
	}
	return nil
}

func readReportReceipt(path string, status ConnectionStatus, transport MockTransport, provider string, artifact corpus.Material, connection ExecutionConnection) (tool.Object, error) {
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
	if status == ConnectionStatusPassed && receiptStatus != "passed" {
		return nil, fmt.Errorf("mock replay receipt status %q differs from connection status %q", receiptStatus, status)
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
	if declaredTransport := tool.Str(receipt["transport"]); declaredTransport != "" && declaredTransport != string(transport) {
		return nil, fmt.Errorf("mock replay receipt transport %q differs from connection transport %q", declaredTransport, transport)
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
	if status == ConnectionStatusPassed &&
		(receipt["fullConsumption"] != true || receipt["clientEOF"] != true || receipt["cleanupComplete"] != true) {
		return nil, errors.New("mock replay receipt does not prove full consumption, client EOF and cleanup")
	}
	return receipt, nil
}

func readReportResult(path string, execution Execution, connection ExecutionConnection, transport MockTransport) (tool.Object, error) {
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
	expectedExecution := filepath.Join(filepath.Dir(filepath.Dir(path)), "execution.json")
	if filepath.Clean(tool.Str(result["execution"])) != expectedExecution {
		return nil, errors.New("mock result execution path differs from execution")
	}
	if filepath.Clean(tool.Str(result["result"])) != path {
		return nil, errors.New("mock result path differs from execution")
	}
	if filepath.Clean(tool.Str(result["receipt"])) != connection.Receipt {
		return nil, errors.New("mock result receipt path differs from execution")
	}
	if declaredTransport := tool.Str(result["transport"]); declaredTransport != "" && declaredTransport != string(transport) {
		return nil, fmt.Errorf("mock result transport %q differs from connection transport %q", declaredTransport, transport)
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
		if processID, ok := result["processId"].(float64); !ok || processID <= 0 {
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

func readReportProcessReceipt(path string, execution Execution, connectionID string, status ConnectionStatus) (*int, error) {
	process, err := readEvidenceObject(path, "mock process receipt")
	if err != nil {
		return nil, fmt.Errorf("mock process receipt: %w", err)
	}
	if process["schemaVersion"] != float64(1) ||
		process["fixtureId"] != execution.FixtureID ||
		process["executionId"] != execution.ExecutionID {
		return nil, errors.New("mock process receipt execution identity differs from execution")
	}
	if process["connectionId"] != connectionID {
		return nil, errors.New("mock process receipt connection identity differs from execution")
	}
	processStatus := tool.Str(process["status"])
	if processStatus != "passed" && processStatus != "failed" {
		return nil, fmt.Errorf("mock process receipt status %q is invalid", processStatus)
	}
	if status == ConnectionStatusPassed && processStatus != "passed" {
		return nil, fmt.Errorf("mock process receipt status %q differs from connection status %q", processStatus, status)
	}
	code, ok := process["exitCode"].(float64)
	if !ok || code != math.Trunc(code) {
		return nil, errors.New("mock process receipt exit code is absent")
	}
	exitCode := int(code)
	if status == ConnectionStatusPassed && exitCode != 0 {
		return nil, fmt.Errorf("mock process receipt exit code %d is nonzero for a passed connection", exitCode)
	}
	return &exitCode, nil
}

func writeFailureEvidence(path string, err error) error {
	value := tool.Object{"status": "failed", "error": err.Error()}
	if class := FailureClassOf(err); class != "" {
		value["failureClass"] = string(class)
	}
	return tool.WriteJSON(path, value)
}
