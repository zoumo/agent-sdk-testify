package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func validateExecutionID(id string) error {
	if id == "" || id == "." || strings.ContainsAny(id, "\\/\x00") || filepath.Base(id) != id || !filepath.IsLocal(id) {
		return errors.New("execution identity is not a local directory name")
	}
	return nil
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

func validateConnectionInventory(fixtureDir string, execution Execution, artifact *corpus.Material, route Route) error {
	if canonical, err := CanonicalFixtureDir(fixtureDir); err == nil {
		fixtureDir = canonical
	}
	var ids []string
	if execution.Command == "record" {
		for _, connection := range route.Connections {
			ids = append(ids, connection.ID)
		}
	} else if artifact != nil {
		ids = append([]string(nil), artifact.Connections...)
	}
	connections, err := executionConnections(fixtureDir, execution.ExecutionID, execution.Artifact.ID, execution.Command, ids, artifact, route)
	if err != nil {
		return err
	}
	actual := make([]ExecutionConnection, len(execution.Connections))
	for i, connection := range execution.Connections {
		connection.Command, connection.Args, connection.Launcher, connection.Material = "", nil, "", ""
		reference, err := PackageReference(fixtureDir, execution.Artifact.ID, connection.Receipt)
		if err != nil {
			return err
		}
		connection.Receipt = reference
		if connection.ProcessReceipt != "" {
			processReference, err := PackageReference(fixtureDir, execution.Artifact.ID, connection.ProcessReceipt)
			if err != nil {
				return err
			}
			connection.ProcessReceipt = processReference
		}
		actual[i] = connection
	}
	if !reflect.DeepEqual(connections, actual) {
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

// ValidateMockConnection checks the ordinary mock's durable facts against its
// exact material, then applies the official verifier's successful-process policy.
func ValidateMockConnection(provider string, execution Execution, connection ExecutionConnection) error {
	facts, err := readMockConnectionEvidence(provider, execution, connection)
	if err != nil {
		return err
	}
	if err := facts.requirePassedReceipts(); err != nil {
		return err
	}
	if connection.Transport != ConnectionTransportProcess || facts.resultStatus != "passed" {
		return errors.New("official mock does not prove a successful process result")
	}
	if facts.replayExitCode == nil || *facts.replayExitCode != 0 {
		return errors.New("replay receipt exit is absent or nonzero")
	}
	return nil
}

// ValidateMockExecution requires the normal completed index and connection
// layout, in addition to the descriptor's public launch contract.
func ValidateMockExecution(fixtureDir string, fixture *Fixture, execution Execution) error {
	if err := execution.Validate(*fixture); err != nil {
		return err
	}
	if execution.Command != "mock" || execution.ArtifactRoot != filepath.Join(PackageExecutionRoot(fixtureDir, execution.Artifact.ID, execution.ExecutionID), "mock") {
		return errors.New("unexpected mock execution layout")
	}
	if err := validateConnectionInventory(fixtureDir, execution, &execution.Artifact, fixture.Routes["mock"]); err != nil {
		return err
	}
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return err
	}
	digest, err := tool.FileDigest(filepath.Join(PackageExecutionRoot(fixtureDir, execution.Artifact.ID, execution.ExecutionID), "execution.json"))
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

func validateRecordReport(root string) error {
	var report, manifest tool.Object
	var err error
	if report, err = readEvidenceObject(filepath.Join(root, "report.json"), "record report"); err != nil {
		return err
	}
	if manifest, err = readEvidenceObject(filepath.Join(root, "manifest.json"), "record manifest"); err != nil {
		return err
	}
	if manifest["captureStatus"] != "complete" {
		return errors.New("record manifest does not report complete capture")
	}
	status := tool.Str(report["status"])
	if status != "passed" && status != "captured" {
		return fmt.Errorf("record report status is %q", status)
	}
	if _, err := os.Stat(filepath.Join(root, "result.json")); err == nil {
		result, err := readEvidenceObject(filepath.Join(root, "result.json"), "record result")
		if err != nil {
			return err
		}
		if result["status"] != "passed" {
			return fmt.Errorf("record result status is %q", tool.Str(result["status"]))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
