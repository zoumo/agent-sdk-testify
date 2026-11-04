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
	SchemaVersion    int               `json:"schemaVersion"`
	SessionID        string            `json:"sessionId"`
	Status           string            `json:"status"`
	CleanupComplete  bool              `json:"cleanupComplete"`
	Executions       []FinishExecution `json:"executions"`
	Verifications    []tool.Object     `json:"verifications,omitempty"`
	OfficialRecords  []tool.Object     `json:"officialRecords,omitempty"`
	Error            string            `json:"error,omitempty"`
	ValidationStatus string            `json:"validationStatus"`
	ValidationError  string            `json:"validationError,omitempty"`
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
		return report, err
	}
	defer func() { runErr = errors.Join(runErr, lock.Close()) }()
	if err := recoverInterrupted(sessionDir, session); err != nil {
		return report, err
	}

	// Closure precedes validation and cleanup. A failed cleanup never reopens actions.
	if err := ensureClosed(sessionDir, session, report); err != nil {
		return report, err
	}
	var previous FinishReport
	if err := tool.ReadJSON(filepath.Join(sessionDir, "finish.json"), &previous); err == nil {
		if previous.SchemaVersion != SchemaVersion || previous.SessionID != session.SessionID ||
			(previous.ValidationStatus != "passed" && previous.ValidationStatus != "failed" && previous.ValidationStatus != "abandoned") {
			return report, errors.New("invalid persisted finish evidence")
		}
		return retryFinishCleanup(sessionDir, session, &previous)
	} else if !os.IsNotExist(err) {
		return report, err
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return report, err
	}
	artifacts := make(map[string]Artifact, len(index.Artifacts))
	for _, artifact := range index.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	orphans, inventoryErr := validateExecutionInventory(sessionDir, index)
	validationErr := inventoryErr
	var officialIDs []string
	if err := tool.ReadJSON(filepath.Join(sessionDir, "official-records", "index.json"), &officialIDs); err != nil && !os.IsNotExist(err) {
		validationErr = errors.Join(validationErr, err)
	}
	for _, id := range officialIDs {
		if err := validateExecutionID(id); err != nil {
			validationErr = errors.Join(validationErr, err)
			continue
		}
		var result tool.Object
		err := tool.ReadJSON(filepath.Join(sessionDir, "official-records", id, "result.json"), &result)
		if err == nil && (result["status"] != "passed" || result["cleanupComplete"] != true || result["sessionId"] != session.SessionID) {
			err = fmt.Errorf("official record %s failed or incomplete", id)
		}
		if err != nil {
			validationErr = errors.Join(validationErr, err)
		}
		report.OfficialRecords = append(report.OfficialRecords, result)
	}

	var verificationIDs []string
	if err := tool.ReadJSON(filepath.Join(sessionDir, "verifications", "index.json"), &verificationIDs); err != nil && !os.IsNotExist(err) {
		validationErr = errors.Join(validationErr, err)
	}
	for _, id := range verificationIDs {
		evidence, err := validateOfficialVerification(sessionDir, session, id)
		report.Verifications = append(report.Verifications, evidence)
		validationErr = errors.Join(validationErr, err)
	}
	for _, id := range orphans {
		result := FinishExecution{
			ID:      id,
			Command: "unknown",
			Status:  "failed",
			Error:   "execution directory is absent from execution index",
		}
		report.Executions = append(report.Executions, result)
		if err := writeAtomic(filepath.Join(ExecutionDir(sessionDir, id), "finish.json"), result); err != nil {
			validationErr = errors.Join(validationErr, fmt.Errorf("%s: write execution finish result: %w", id, err))
		}
	}
	if session.HistorySeed != "" {
		if err := history.VerifyTree(filepath.Dir(session.HistorySeed), session.HistorySeedFiles); err != nil {
			validationErr = errors.Join(validationErr, fmt.Errorf("session history seed: %w", err))
		}
	}
	for _, indexed := range index.Executions {
		result := validateFinishedExecution(sessionDir, session, indexed, artifacts)
		report.Executions = append(report.Executions, result)
		if result.Status != "passed" {
			validationErr = errors.Join(validationErr, fmt.Errorf("%s: %s", result.ID, result.Error))
		}
		if validateExecutionID(result.ID) == nil {
			if err := writeAtomic(filepath.Join(ExecutionDir(sessionDir, result.ID), "finish.json"), result); err != nil {
				validationErr = errors.Join(validationErr, fmt.Errorf("%s: write execution finish result: %w", result.ID, err))
			}
		}
	}
	if validationErr == nil && len(report.Executions) == 0 {
		report.Status = "abandoned"
	} else if validationErr == nil {
		report.Status = "passed"
	}

	report.ValidationStatus = report.Status
	if validationErr != nil {
		report.ValidationError = validationErr.Error()
	}
	// Persist the immutable business conclusion before touching temporary resources.
	if err := writeAtomic(filepath.Join(sessionDir, "finish.json"), report); err != nil {
		report.Status, report.Error = "failed", err.Error()
		return report, err
	}
	return retryFinishCleanup(sessionDir, session, report)
}

func retryFinishCleanup(sessionDir string, session *Session, report *FinishReport) (*FinishReport, error) {
	var cleanupErr error
	if !report.CleanupComplete {
		report.CleanupComplete, cleanupErr = finishCleanup(sessionDir, session)
	}
	report.Status = report.ValidationStatus
	var validationErr error
	if report.ValidationError != "" {
		validationErr = errors.New(report.ValidationError)
	}
	runErr := errors.Join(validationErr, cleanupErr)
	report.Error = ""
	if runErr != nil {
		report.Status, report.Error = "failed", runErr.Error()
	}
	cleanup := tool.Object{
		"schemaVersion":   SchemaVersion,
		"sessionId":       session.SessionID,
		"resourceRoot":    session.ResourceRoot,
		"cleanupComplete": report.CleanupComplete,
	}
	if cleanupErr != nil {
		cleanup["error"] = cleanupErr.Error()
	}
	if err := writeAtomic(filepath.Join(sessionDir, "cleanup.json"), cleanup); err != nil {
		runErr = errors.Join(runErr, err)
		report.Status, report.Error = "failed", runErr.Error()
	}
	if err := writeAtomic(filepath.Join(sessionDir, "finish.json"), report); err != nil {
		runErr = errors.Join(runErr, err)
		report.Status, report.Error = "failed", runErr.Error()
		return report, runErr
	}
	return report, runErr
}

func ensureClosed(sessionDir string, session *Session, report *FinishReport) error {
	if _, err := os.Lstat(filepath.Join(sessionDir, "closed")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
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
	ownerPath := filepath.Join(session.ResourceRoot, ".owner")
	ownerInfo, err := os.Lstat(ownerPath)
	if err != nil {
		return false, fmt.Errorf("temporary resource ownership is missing: %w", err)
	}
	if !ownerInfo.Mode().IsRegular() {
		return false, errors.New("temporary resource ownership is not a regular unlinked file")
	}
	owner, err := os.ReadFile(ownerPath)
	if err != nil {
		return false, fmt.Errorf("temporary resource ownership is unreadable: %w", err)
	}
	if string(owner) != session.SessionID {
		return false, errors.New("temporary resource ownership mismatch")
	}
	// Keep the ownership marker until every child has been removed, so a
	// partially completed cleanup can safely retry the remaining children.
	entries, err := os.ReadDir(session.ResourceRoot)
	if err != nil {
		return false, err
	}
	var cleanupErr error
	for _, entry := range entries {
		if entry.Name() != ".owner" {
			cleanupErr = errors.Join(cleanupErr, os.RemoveAll(filepath.Join(session.ResourceRoot, entry.Name())))
		}
	}
	if cleanupErr != nil {
		return false, cleanupErr
	}
	if err := os.Remove(ownerPath); err != nil {
		return false, err
	}
	if err := os.Remove(session.ResourceRoot); err != nil {
		return false, errors.Join(err, os.WriteFile(ownerPath, owner, 0o600))
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
	if err := validateExecutionID(indexed.ID); err != nil {
		fail(err)
		result.Status = "failed"
		result.Error = errors.Join(errs...).Error()
		return result
	}
	if indexed.Status != "passed" {
		fail(fmt.Errorf("execution status is %s", indexed.Status))
	}
	dir := ExecutionDir(sessionDir, indexed.ID)
	descriptorPath := filepath.Join(dir, "execution.json")
	digest, err := tool.FileDigest(descriptorPath)
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
		if indexed.ID != execution.ExecutionID {
			fail(errors.New("execution identity differs from index"))
		}
		if indexed.Command != execution.Command {
			fail(errors.New("execution command differs from index"))
		}
		if indexed.StartedAt != execution.StartedAt {
			fail(errors.New("execution start time differs from index"))
		}
		fail(validateIndexedConnections(indexed, execution))
		fail(validateExecutionLocation(sessionDir, session, dir, execution))
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
					value.PlanSHA256 != execution.Artifact.PlanSHA256 ||
					value.ManifestSHA256 != execution.Artifact.ManifestSHA256 ||
					value.RecordingSHA256 != execution.Artifact.RecordingSHA256 ||
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
				fail(validateConnectionInventory(execution, artifact, session.Routes[execution.Command]))
				expected, provenanceErr := expectedFinishedProvenance(session, execution.Command, inspection)
				fail(provenanceErr)
				if provenanceErr == nil && execution.Provenance != expected {
					fail(fmt.Errorf("execution provenance %q differs from %s material %q", execution.Provenance, execution.Command, expected))
				}
				if execution.Command == "mock" {
					if !maps.Equal(execution.HistoryFiles, inspection.HistoryFiles) {
						fail(errors.New("execution history bindings differ from selected material"))
					} else {
						fail(history.Check(filepath.Join(session.ResourceRoot, "executions", execution.ExecutionID), execution.HistoryFiles))
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
			expected := replayReceiptExpectation{
				Provider:        session.AgentType,
				RecordingSHA256: connection.RecordingSHA256,
				PlanSHA256:      connection.PlanSHA256,
				RequirePlan:     execution.Command == "mock",
				RequireExitCode: execution.Command == "mock",
			}
			if execution.Command == "mock" {
				expected.Material = connection.Material
			}
			if artifact != nil {
				path, pathErr := connectionRecordingPath(artifact.Path, connection.ID)
				if pathErr == nil {
					expected.Frames, pathErr = recordingFrameCount(path, session.AgentType)
				}
				if pathErr != nil {
					peer.Status, peer.Error = "failed", pathErr.Error()
					fail(pathErr)
					result.Connections = append(result.Connections, peer)
					continue
				}
			}
			switch execution.Command {
			case "record":
				peerErr := validateCaptureTransport(connection.Receipt, expected.Frames)
				peer.Status, peer.Error = peerResult(peerErr)
				fail(peerErr)
			case "mock":
				peerErr := validateMockReceipts(connection.Receipt, connection.ProcessReceipt, execution, connection, expected)
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
			if execution.Admission != nil {
				fail(errors.New("record execution has verify admission parameters"))
			}
		case "mock":
			if execution.Admission != nil {
				fail(errors.New("mock execution has verify admission parameters"))
			}
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
	overlayRoot := filepath.Join(session.ResourceRoot, "executions", execution.ExecutionID)
	workspace := filepath.Join(overlayRoot, "workspace")
	route, ok := session.Routes[execution.Command]
	if !ok || !route.Enabled {
		return fmt.Errorf("%s route is unavailable", execution.Command)
	}
	var plan tool.Object
	if execution.Command == "mock" {
		inspection, err := inspectMaterial(session, execution.Artifact, execution.Command)
		if err != nil {
			return err
		}
		plan = inspection.Plan
	} else {
		var err error
		plan, err = LoadPlan(session)
		if err != nil {
			return err
		}
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
	profile, err := agentProfileFor(session.AgentType)
	if err != nil {
		return err
	}
	if execution.Env[profile.configKey] != execution.Config {
		return fmt.Errorf("execution environment %s differs from its config", profile.configKey)
	}
	for _, known := range agentProfiles {
		if value, ok := execution.Env[known.configKey]; ok && value != execution.Config {
			return fmt.Errorf("execution environment %s differs from its config", known.configKey)
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
		if material != nil && material.Provenance == "official" {
			return "official", nil
		}
		return executionProvenance(session, action, nil)
	case "mock", "verify":
		return material.Provenance, nil
	}
	return "", fmt.Errorf("unknown execution action %q", action)
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
