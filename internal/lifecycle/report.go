package lifecycle

import (
	"errors"
	"path/filepath"

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

func mockExecutionReport(fixtureDir, executionID string) (*ExecutionReport, error) {
	fixture, err := readFixtureUnlocked(fixtureDir)
	if err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return nil, err
	}
	var indexed *IndexExecution
	for i := range index.Executions {
		if index.Executions[i].ID == executionID {
			indexed = &index.Executions[i]
			break
		}
	}
	if indexed == nil {
		return nil, errors.New("mock execution is absent from index")
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
	statuses := make(map[string]string, len(indexed.Connections))
	for _, connection := range indexed.Connections {
		statuses[connection.ID] = connection.Status
	}
	report := &ExecutionReport{
		FixtureID:       execution.FixtureID,
		ExecutionID:     execution.ExecutionID,
		Status:          ExecutionStatus(indexed.Status),
		Complete:        indexed.Status == string(ExecutionStatusPassed) || indexed.Status == string(ExecutionStatusFailed),
		Material:        cloneTransactionMaterial(execution.Artifact),
		ExecutionSHA256: descriptorSHA256,
		ExecutionPath:   descriptorPath,
		Connections:     make([]ConnectionReport, 0, len(execution.Connections)),
	}
	for _, connection := range execution.Connections {
		status, ok := statuses[connection.ID]
		if !ok {
			return nil, errors.New("execution connection is absent from index")
		}
		item := ConnectionReport{
			ID:                 connection.ID,
			Status:             ConnectionStatus(status),
			Transport:          MockTransport(connection.Transport),
			ReceiptPath:        connection.Receipt,
			ProcessReceiptPath: connection.ProcessReceipt,
			ResultPath:         filepath.Join(dir, "receipts", connection.ID+".result.json"),
		}
		if connection.Transport == ConnectionTransportProcess && item.ProcessReceiptPath != "" {
			process, err := readEvidenceObject(item.ProcessReceiptPath, "mock process receipt")
			if err != nil {
				return nil, err
			}
			if code, ok := process["exitCode"].(float64); ok {
				value := int(code)
				item.ExitCode = &value
			}
			if process["nativeLaunched"] == true {
				item.NativeLaunched = true
			}
		}
		if item.Status == ConnectionStatusPassed || item.Status == ConnectionStatusFailed {
			receipt, err := readEvidenceObject(item.ReceiptPath, "mock replay receipt")
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
			if item.Status == ConnectionStatusPassed && receipt["status"] != "passed" {
				return nil, errors.New("passed connection has a non-passing replay receipt")
			}
		}
		report.Connections = append(report.Connections, item)
	}
	return report, nil
}

func writeFailureEvidence(path string, err error) error {
	value := tool.Object{"status": "failed", "error": err.Error()}
	if class := FailureClassOf(err); class != "" {
		value["failureClass"] = string(class)
	}
	return tool.WriteJSON(path, value)
}
