package lifecycle

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type FinishExecution struct {
	ID          string       `json:"id"`
	Command     string       `json:"command"`
	Status      string       `json:"status"`
	ArtifactID  string       `json:"artifactId,omitempty"`
	Evidence    tool.Object  `json:"evidence,omitempty"`
	Error       string       `json:"error,omitempty"`
	Connections []FinishPeer `json:"connections,omitempty"`
}

type FinishPeer struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type FinishReport struct {
	SchemaVersion   int               `json:"schemaVersion"`
	SessionID       string            `json:"sessionId"`
	Status          string            `json:"status"`
	CleanupComplete bool              `json:"cleanupComplete"`
	Executions      []FinishExecution `json:"executions"`
	Error           string            `json:"error,omitempty"`
}

// Finish validates every immutable execution descriptor against the mutable
// index, checks action-specific evidence, writes closure records, and removes
// only the ownership-marked temporary root.
func Finish(sessionDir string) (report *FinishReport, runErr error) {
	sessionDir, err := canonicalSessionDir(sessionDir)
	if err != nil {
		return nil, err
	}
	session, err := readSessionUnlocked(sessionDir)
	if err != nil {
		return nil, err
	}
	report = &FinishReport{
		SchemaVersion: SchemaVersion,
		SessionID:     session.SessionID,
		Status:        "failed",
		Executions:    []FinishExecution{},
	}
	lock, err := actionLock(sessionDir, true)
	if err != nil {
		report.Error = err.Error()
		_ = writeAtomic(filepath.Join(sessionDir, "finish.json"), report)
		return report, err
	}
	defer func() { runErr = errors.Join(runErr, lock.Close()) }()

	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		report.Error = err.Error()
		_ = writeAtomic(filepath.Join(sessionDir, "finish.json"), report)
		return report, err
	}
	if previous, previousErr := completedFinish(sessionDir, session); previous != nil {
		report = previous
		if cleanupErr := ensureClosed(sessionDir, session, report); cleanupErr != nil {
			return report, errors.Join(previousErr, cleanupErr)
		}
		return report, previousErr
	}
	artifacts := make(map[string]Artifact, len(index.Artifacts))
	for _, artifact := range index.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	var validationErr error
	if session.HistorySeed != "" {
		if err := history.VerifyTree(filepath.Dir(session.HistorySeed), session.HistorySeedFiles); err != nil {
			validationErr = fmt.Errorf("session history seed: %w", err)
		}
	}
	for _, indexed := range index.Executions {
		result := validateFinishedExecution(sessionDir, session, indexed, artifacts)
		report.Executions = append(report.Executions, result)
		if result.Status != "passed" {
			validationErr = errors.Join(validationErr, fmt.Errorf("%s: %s", result.ID, result.Error))
		}
		_ = writeAtomic(filepath.Join(ExecutionDir(sessionDir, result.ID), "finish.json"), result)
	}
	if validationErr == nil && len(report.Executions) == 0 {
		report.Status = "abandoned"
	} else if validationErr == nil {
		report.Status = "passed"
	}

	cleanupComplete, cleanupErr := finishCleanup(sessionDir, session)
	report.CleanupComplete = cleanupComplete
	runErr = errors.Join(validationErr, cleanupErr)
	if runErr != nil {
		report.Status = "failed"
		report.Error = runErr.Error()
	}
	cleanup := tool.Object{
		"schemaVersion":   SchemaVersion,
		"sessionId":       session.SessionID,
		"resourceRoot":    session.ResourceRoot,
		"cleanupComplete": cleanupComplete,
		"status":          report.Status,
	}
	if cleanupErr != nil {
		cleanup["error"] = cleanupErr.Error()
	}
	if err := writeAtomic(filepath.Join(sessionDir, "cleanup.json"), cleanup); err != nil {
		runErr = errors.Join(runErr, err)
		report.Status = "failed"
		report.CleanupComplete = false
		report.Error = runErr.Error()
	}
	if err := writeAtomic(filepath.Join(sessionDir, "finish.json"), report); err != nil {
		return report, errors.Join(runErr, err)
	}
	if err := ensureClosed(sessionDir, session, report); err != nil {
		return report, errors.Join(runErr, err)
	}
	return report, runErr
}

func completedFinish(sessionDir string, session *Session) (*FinishReport, error) {
	if _, err := os.Lstat(session.ResourceRoot); !os.IsNotExist(err) {
		return nil, nil
	}
	var cleanup tool.Object
	if err := tool.ReadJSON(filepath.Join(sessionDir, "cleanup.json"), &cleanup); err != nil {
		err = fmt.Errorf("temporary resource root is absent without cleanup evidence: %w", err)
		return failedFinishReport(session, false, err), err
	}
	if tool.Str(cleanup["sessionId"]) != session.SessionID {
		err := errors.New("cleanup evidence belongs to a different session")
		return failedFinishReport(session, false, err), err
	}
	if cleanup["cleanupComplete"] != true {
		err := errors.New("temporary resource cleanup previously failed")
		return failedFinishReport(session, false, err), err
	}
	var report FinishReport
	if err := tool.ReadJSON(filepath.Join(sessionDir, "finish.json"), &report); err != nil {
		return failedFinishReport(session, true, err), err
	}
	if report.SessionID != session.SessionID || !report.CleanupComplete {
		err := errors.New("persisted finish report belongs to a different or incomplete session")
		return failedFinishReport(session, true, err), err
	}
	if report.Status == "passed" || report.Status == "abandoned" {
		return &report, nil
	}
	if report.Error == "" {
		report.Error = "session finish status is " + report.Status
	}
	return &report, errors.New(report.Error)
}

func failedFinishReport(session *Session, cleanupComplete bool, err error) *FinishReport {
	return &FinishReport{
		SchemaVersion:   SchemaVersion,
		SessionID:       session.SessionID,
		Status:          "failed",
		CleanupComplete: cleanupComplete,
		Executions:      []FinishExecution{},
		Error:           err.Error(),
	}
}

func ensureClosed(sessionDir string, session *Session, report *FinishReport) error {
	return writeAtomic(filepath.Join(sessionDir, "closed"), tool.Object{
		"schemaVersion": SchemaVersion,
		"sessionId":     session.SessionID,
		"status":        report.Status,
	})
}

func finishCleanup(sessionDir string, session *Session) (bool, error) {
	info, err := os.Lstat(session.ResourceRoot)
	if os.IsNotExist(err) {
		var previous tool.Object
		if readErr := tool.ReadJSON(filepath.Join(sessionDir, "cleanup.json"), &previous); readErr != nil {
			return false, errors.New("temporary resource root is absent without cleanup evidence")
		}
		if tool.Str(previous["sessionId"]) != session.SessionID {
			return false, errors.New("cleanup evidence belongs to a different session")
		}
		if previous["cleanupComplete"] != true {
			return false, errors.New("temporary resource cleanup previously failed")
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("temporary resource root is not an unlinked directory")
	}
	owner, err := os.ReadFile(filepath.Join(session.ResourceRoot, ".owner"))
	if err != nil {
		return false, fmt.Errorf("temporary resource ownership is missing: %w", err)
	}
	if string(owner) != session.SessionID {
		return false, errors.New("temporary resource ownership mismatch")
	}
	if err := os.RemoveAll(session.ResourceRoot); err != nil {
		return false, err
	}
	return true, nil
}

func validateFinishedExecution(sessionDir string, session *Session, indexed IndexExecution, artifacts map[string]Artifact) FinishExecution {
	result := FinishExecution{
		ID:         indexed.ID,
		Command:    indexed.Command,
		Status:     "passed",
		ArtifactID: indexed.ArtifactID,
		Evidence:   tool.Object{},
	}
	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if indexed.Status != "passed" {
		fail(fmt.Errorf("execution status is %s", indexed.Status))
	}
	dir := ExecutionDir(sessionDir, indexed.ID)
	descriptorPath := filepath.Join(dir, "execution.json")
	digest, err := digestFile(descriptorPath)
	if err != nil {
		fail(err)
	} else if digest != indexed.DescriptorSHA256 {
		fail(errors.New("execution descriptor changed after creation"))
	}
	var execution Execution
	if err := tool.ReadJSON(descriptorPath, &execution); err != nil {
		fail(err)
	} else if err := execution.Validate(*session); err != nil {
		fail(err)
	} else {
		fail(validateExecutionLocation(sessionDir, session, dir, execution))
		if execution.Command != indexed.Command {
			fail(errors.New("execution command differs from index"))
		}
		var artifact *Artifact
		if indexed.ArtifactID != "" {
			value, ok := artifacts[indexed.ArtifactID]
			if !ok {
				fail(errors.New("execution artifact is absent from artifact index"))
			} else {
				artifact = &value
				if value.ID != execution.Artifact.ID {
					fail(errors.New("execution artifact differs from published artifact"))
				} else if value.Kind != execution.Artifact.Kind ||
					value.Action != execution.Artifact.Action ||
					value.Provenance != execution.Artifact.Provenance ||
					value.Path != execution.Artifact.Path ||
					!reflect.DeepEqual(value.Connections, execution.Artifact.Connections) {
					fail(errors.New("execution artifact identity differs from published artifact"))
				}
			}
		} else if execution.Artifact.Path != "" && execution.Artifact.PlanSHA256 != "" {
			value := execution.Artifact
			artifact = &value
		}
		if execution.Command == "record" && artifact == nil {
			fail(errors.New("record execution has no published capture"))
		}
		if artifact != nil {
			inspection, materialErr := inspectMaterial(session, *artifact, execution.Command)
			fail(materialErr)
			if materialErr == nil {
				expected, provenanceErr := expectedFinishedProvenance(session, execution.Command, inspection)
				fail(provenanceErr)
				if provenanceErr == nil && execution.Provenance != expected {
					fail(fmt.Errorf("execution provenance %q differs from %s material %q", execution.Provenance, execution.Command, expected))
				}
				if execution.Command == "mock" || execution.Command == "verify" {
					if !maps.Equal(execution.HistoryFiles, inspection.HistoryFiles) {
						fail(errors.New("execution history bindings differ from selected material"))
					} else {
						fail(history.Check(filepath.Join(ExecutionDir(sessionDir, execution.ExecutionID), "overlay"), execution.HistoryFiles))
					}
				}
			}
			result.Evidence["material"] = tool.Object{
				"path":            artifact.Path,
				"planSHA256":      artifact.PlanSHA256,
				"manifestSHA256":  artifact.ManifestSHA256,
				"recordingSHA256": artifact.RecordingSHA256,
			}
		}
		for _, connection := range execution.Connections {
			peer := FinishPeer{ID: connection.ID, Status: "passed"}
			switch execution.Command {
			case "record":
				peerErr := validateTransport(connection.Receipt)
				peer.Status, peer.Error = peerResult(peerErr)
				fail(peerErr)
			case "verify":
				peerErr := validateReplayReceipt(connection.Receipt, session.AgentType)
				peer.Status, peer.Error = peerResult(peerErr)
				fail(peerErr)
			case "mock":
				peerErr := validateMockReceipts(connection.Receipt, connection.ProcessReceipt, execution, session.AgentType)
				peer.Status, peer.Error = peerResult(peerErr)
				fail(peerErr)
			default:
				fail(fmt.Errorf("unknown execution command %q", execution.Command))
			}
			result.Connections = append(result.Connections, peer)
		}
		switch execution.Command {
		case "record":
			fail(validateRecordReport(execution.ArtifactRoot))
		case "verify":
			fail(validateVerifyReport(execution.ArtifactRoot))
		case "mock":
			fail(validateMockExecution(execution.ArtifactRoot))
		}
	}
	if len(errs) != 0 {
		result.Status = "failed"
		result.Error = errors.Join(errs...).Error()
	}
	return result
}

func peerResult(err error) (string, string) {
	if err == nil {
		return "passed", ""
	}
	return "failed", err.Error()
}

func validateExecutionLocation(sessionDir string, session *Session, dir string, execution Execution) error {
	expectedArtifact := filepath.Join(dir, execution.Command)
	overlayRoot := filepath.Join(dir, "overlay")
	workspace := filepath.Join(overlayRoot, "workspace")
	route, ok := session.Routes[execution.Command]
	if !ok || !route.Enabled {
		return fmt.Errorf("%s route is unavailable", execution.Command)
	}
	plan, err := LoadPlan(session)
	if err != nil {
		return err
	}
	switch {
	case execution.ArtifactRoot != expectedArtifact:
		return errors.New("execution artifact root differs from session")
	case execution.Workspace != workspace:
		return errors.New("execution workspace differs from its overlay")
	case execution.ProtocolCwd != executionProtocolCwd(execution.Command, plan, workspace):
		return errors.New("execution protocol cwd differs from its action")
	case execution.Home != filepath.Join(overlayRoot, "home"):
		return errors.New("execution home differs from its overlay")
	case execution.Config != filepath.Join(overlayRoot, "config"):
		return errors.New("execution config differs from its overlay")
	case execution.Tmp != filepath.Join(overlayRoot, "tmp"):
		return errors.New("execution tmp differs from its overlay")
	case execution.HistorySeed != filepath.Join(overlayRoot, "history-seed"):
		return errors.New("execution history seed differs from its overlay")
	case validateExecutionEnvironment(session, execution) != nil:
		return validateExecutionEnvironment(session, execution)
	case execution.Executor != route.Executor:
		return errors.New("execution executor differs from session")
	case execution.Target != route.Target:
		return errors.New("execution target differs from session")
	}
	for _, connection := range execution.Connections {
		if !withinPath(dir, connection.Receipt) {
			return fmt.Errorf("connection %s receipt escapes execution directory", connection.ID)
		}
		if connection.ProcessReceipt != "" && !withinPath(dir, connection.ProcessReceipt) {
			return fmt.Errorf("connection %s process receipt escapes execution directory", connection.ID)
		}
	}
	if execution.Source.Kind == "case" || execution.Source.Kind == "plan" {
		if !withinPath(dir, execution.Source.Path) &&
			!withinPath(session.ResourceRoot, execution.Source.Path) &&
			!withinPath(sessionDir, execution.Source.Path) {
			return errors.New("execution source snapshot escapes owned resources")
		}
	}
	return nil
}

func validateExecutionEnvironment(session *Session, execution Execution) error {
	expected := map[string]string{
		"HOME":            execution.Home,
		"TMPDIR":          execution.Tmp,
		"TMP":             execution.Tmp,
		"TEMP":            execution.Tmp,
		"XDG_CONFIG_HOME": filepath.Join(execution.Home, ".config"),
		"XDG_CACHE_HOME":  filepath.Join(execution.Home, ".cache"),
		"XDG_DATA_HOME":   filepath.Join(execution.Home, ".local", "share"),
	}
	for key, value := range expected {
		current, ok := execution.Env[key]
		if !ok || current != value {
			return fmt.Errorf("execution environment %s differs from its overlay", key)
		}
	}
	configKey := "CODEX_HOME"
	if session.AgentType == "claude" {
		configKey = "CLAUDE_CONFIG_DIR"
	}
	if execution.Env[configKey] != execution.Config {
		return fmt.Errorf("execution environment %s differs from its config", configKey)
	}
	for key, value := range execution.Env {
		switch key {
		case "CODEX_HOME", "CLAUDE_CONFIG_DIR":
			if value != execution.Config {
				return fmt.Errorf("execution environment %s differs from its config", key)
			}
		}
	}
	return nil
}

func withinPath(root, path string) bool {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || (rel != ".." && filepath.IsLocal(rel)))
}

func expectedFinishedProvenance(session *Session, action string, material *materialInspection) (string, error) {
	switch action {
	case "record":
		switch session.Source.Kind {
		case "case":
			return "official", nil
		case "plan":
			return "consumer", nil
		}
	case "mock", "verify":
		return material.Provenance, nil
	}
	return "", fmt.Errorf("unknown execution action %q", action)
}

func validateTransport(path string) error {
	var transport tool.Object
	if err := tool.ReadJSON(path, &transport); err != nil {
		return err
	}
	if transport["captureStatus"] != "complete" ||
		transport["cleanupComplete"] != true ||
		transport["nativeLaunched"] != true ||
		transport["clientEOF"] != true ||
		transport["reaped"] != true ||
		transport["error"] != nil {
		return errors.New("transport does not prove complete capture, EOF, normal exit and cleanup")
	}
	if code, ok := transport["nativeExitCode"].(float64); !ok || code != 0 {
		return errors.New("native process exit is absent or nonzero")
	}
	return nil
}

func validateReplayReceipt(path, provider string) error {
	var receipt tool.Object
	if err := tool.ReadJSON(path, &receipt); err != nil {
		return err
	}
	if receipt["status"] != "passed" ||
		receipt["provider"] != provider ||
		receipt["fullConsumption"] != true ||
		receipt["clientEOF"] != true ||
		receipt["cleanupComplete"] != true ||
		receipt["nativeLaunched"] != false ||
		receipt["error"] != nil {
		return errors.New("replay receipt does not prove strict full consumption, EOF and cleanup")
	}
	return nil
}

func validateMockReceipts(receiptPath, processPath string, execution Execution, provider string) error {
	if err := validateReplayReceipt(receiptPath, provider); err != nil {
		return err
	}
	var process tool.Object
	if err := tool.ReadJSON(processPath, &process); err != nil {
		return err
	}
	if process["status"] != "passed" ||
		process["executionId"] != execution.ExecutionID ||
		process["sessionId"] != execution.SessionID ||
		process["error"] != nil {
		return errors.New("mock process receipt is absent or unsuccessful")
	}
	if code, ok := process["exitCode"].(float64); !ok || code != 0 {
		return errors.New("mock process exit is absent or nonzero")
	}
	return nil
}

func validateRecordReport(root string) error {
	var report, manifest tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "report.json"), &report); err != nil {
		return err
	}
	if err := tool.ReadJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
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
		var result tool.Object
		if err := tool.ReadJSON(filepath.Join(root, "result.json"), &result); err != nil {
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

func validateVerifyReport(root string) error {
	var verification, replay, summary tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "verification.json"), &verification); err != nil {
		return err
	}
	if verification["status"] != "passed" {
		return fmt.Errorf("verification status is %q", tool.Str(verification["status"]))
	}
	if err := tool.ReadJSON(filepath.Join(root, "replay-report.json"), &replay); err != nil {
		return err
	}
	if replay["status"] != "passed" ||
		replay["branchStatus"] != "passed" ||
		replay["captureStatus"] != "complete" ||
		len(tool.Array(replay["gaps"])) != 0 {
		return errors.New("official replay report is absent or incomplete")
	}
	if err := tool.ReadJSON(filepath.Join(root, "summary.json"), &summary); err != nil {
		return err
	}
	if summary["error"] != nil {
		return errors.New("verification summary contains an error")
	}
	result := tool.Obj(summary["result"])
	if result["status"] != "passed" {
		return errors.New("verification summary result is not passed")
	}
	return nil
}

func validateMockExecution(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("mock execution evidence root is invalid")
	}
	return nil
}
