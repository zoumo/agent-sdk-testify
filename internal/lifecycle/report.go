package lifecycle

import (
	"errors"
	"fmt"
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
	hasResultFailure := false
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
			facts, err := readMockConnectionEvidence(fixture.AgentType, execution, connection)
			if err != nil {
				return nil, err
			}
			if item.Status == ConnectionStatusPassed {
				if err := facts.requirePassedReceipts(); err != nil {
					return nil, err
				}
			}
			item.FullConsumption = facts.fullConsumption
			item.ClientEOF = facts.clientEOF
			item.NativeLaunched = facts.nativeLaunched
			item.CleanupComplete = facts.cleanupComplete
			item.Failure = facts.failure
			item.Error = facts.message
			item.ExitCode = facts.exitCode
			resultFailed := facts.hasResultFailure()
			hasResultFailure = hasResultFailure || resultFailed
			// Finalize can fail after connection settlement. Preserve successful
			// replay/process facts while exposing the action's durable diagnostic.
			if (item.Status == ConnectionStatusFailed || resultFailed) && item.Failure == "" {
				item.Failure = facts.resultFailure
			}
			if (item.Status == ConnectionStatusFailed || resultFailed) && item.Error == "" {
				item.Error = facts.resultMessage
			}
		}
		report.Connections = append(report.Connections, item)
	}
	if err := validateExecutionStatus(status, statuses, hasResultFailure); err != nil {
		return nil, err
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

func validateExecutionStatus(status ExecutionStatus, statuses map[string]string, hasResultFailure bool) error {
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
		if hasResultFailure {
			return errors.New("mock execution status is passed with a failed result")
		}
	case ExecutionStatusOpen:
		if !hasIncomplete {
			return errors.New("mock execution status is open but every connection is terminal")
		}
	case ExecutionStatusFailed:
		if hasIncomplete {
			return errors.New("mock execution status is failed while a connection is still active")
		}
		if !hasFailed && !hasResultFailure {
			return errors.New("mock execution status is failed without a failed connection or validated failure result")
		}
	default:
		return fmt.Errorf("mock execution status %q is invalid", status)
	}
	return nil
}

func writeFailureEvidence(path string, err error) error {
	value := tool.Object{"status": "failed", "error": err.Error()}
	if class := FailureClassOf(err); class != "" {
		value["failureClass"] = string(class)
	}
	return tool.WriteJSON(path, value)
}
