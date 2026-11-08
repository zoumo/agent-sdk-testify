package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// CanonicalSessionDir validates and resolves one prepared-session directory.
func CanonicalSessionDir(sessionDir string) (string, error) {
	if !filepath.IsAbs(sessionDir) {
		return "", errors.New("--session-dir must be absolute")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(sessionDir))
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, filepath.Base(sessionDir))
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("session directory is not an unlinked directory")
	}
	return dir, nil
}

func readSessionUnlocked(sessionDir string) (*Session, error) {
	dir, err := CanonicalSessionDir(sessionDir)
	if err != nil {
		return nil, err
	}
	session, err := readSession(SessionPath(dir))
	if err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(dir))
	if err != nil {
		return nil, err
	}
	hash, err := tool.FileDigest(SessionPath(dir))
	if err != nil {
		return nil, err
	}
	if hash != index.SessionSHA256 || index.SessionID != session.SessionID {
		return nil, errors.New("session descriptor changed after prepare")
	}
	if session.ResourceRoot != filepath.Join(dir, "runtime") {
		return nil, errors.New("session runtime must be inside session/runtime")
	}
	return session, nil
}

func OpenSession(sessionDir string) (*Session, error) {
	return readSessionUnlocked(sessionDir)
}

func updateIndex(sessionDir string, mutate func(*Index) error) error {
	lock, err := metadataLock(sessionDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return err
	}
	oldMaterials := append([]corpus.Material(nil), index.Artifacts...)
	if err := mutate(index); err != nil {
		return err
	}
	if !reflect.DeepEqual(oldMaterials, index.Artifacts) {
		if err := writeMaterialIndex(sessionDir, index); err != nil {
			return err
		}
	}
	index.LastUpdatedUTC = now()
	return writeAtomic(IndexPath(sessionDir), index)
}

func pendingExecution(index *Index) *IndexExecution {
	for i := range index.Executions {
		execution := &index.Executions[i]
		if execution.Status == "running" || execution.Status == "open" {
			return execution
		}
	}
	return nil
}

func connectionStarted(execution *IndexExecution, id string) bool {
	for _, connection := range execution.Connections {
		if connection.ID == id && connection.Status != "pending" {
			return true
		}
	}
	return false
}

func setConnectionStarted(execution *IndexExecution, id, status string) {
	for i := range execution.Connections {
		if execution.Connections[i].ID == id {
			execution.Connections[i].Status = status
		}
	}
}

func executionConnections(dir, action string, ids []string, material *corpus.Material, route Route) ([]ExecutionConnection, error) {
	var connections []ExecutionConnection
	for _, id := range ids {
		template, ok := connectionTemplate(route, id)
		if !ok {
			return nil, fmt.Errorf("route does not declare connection %s", id)
		}
		connection := ExecutionConnection{
			ID:       id,
			Command:  template.Command,
			Args:     append([]string(nil), template.Args...),
			Launcher: template.Launcher,
			Embedded: template.Embedded,
		}
		switch action {
		case "record":
			base := dir
			if id == "probe" {
				base = filepath.Join(dir, "remote-probe")
			}
			connection.Receipt = filepath.Join(base, "raw", "transport.json")
		case "mock":
			connection.Receipt = filepath.Join(dir, "receipts", id+".json")
			connection.ProcessReceipt = filepath.Join(dir, "receipts", id+".process.json")
		}
		if action == "record" {
			connection.Material = dir
		}
		if material != nil {
			recording := filepath.Join(material.Path, "raw", "raw.jsonl")
			if id == "probe" {
				recording = filepath.Join(material.Path, "remote-probe", "raw", "raw.jsonl")
			}
			recordingHash, err := tool.FileDigest(recording)
			if err != nil {
				return nil, err
			}
			connection.Material = material.Path
			connection.PlanSHA256 = material.PlanSHA256
			connection.ManifestSHA256 = material.ManifestSHA256
			connection.RecordingSHA256 = recordingHash
		}
		connections = append(connections, connection)
	}
	return connections, nil
}

func createExecutionOverlay(session *Session, action, dir string, plan tool.Object, material *corpus.Material) (string, map[string]string, error) {
	route, ok := session.Routes[action]
	if !ok || !route.Enabled {
		return "", nil, fmt.Errorf("%s is not available for this session", action)
	}
	overlayRoot := filepath.Join(session.ResourceRoot, "executions", filepath.Base(dir))
	success := false
	slotsTouched := false
	defer func() {
		if !success {
			if slotsTouched {
				_ = restoreRouteSlots(session, action)
			}
			_ = os.RemoveAll(overlayRoot)
		}
	}()
	if err := tool.CopyTree(filepath.Join(session.ResourceRoot, "baseline"), overlayRoot); err != nil {
		return "", nil, err
	}
	if err := prepareExecutionAuthentication(session, action, overlayRoot); err != nil {
		return "", nil, err
	}
	if action == "mock" {
		if err := cleanMockConfig(overlayRoot, plan, session.AgentType); err != nil {
			return "", nil, err
		}
		if err := history.Reset(overlayRoot); err != nil {
			return "", nil, err
		}
		if err := os.MkdirAll(filepath.Join(overlayRoot, "history-seed"), 0o700); err != nil {
			return "", nil, err
		}
	}
	var historyFiles map[string]string
	if (action == "mock") && material != nil {
		var err error
		historyFiles, err = history.Materialize(material.Path, overlayRoot)
		if err != nil {
			return "", nil, err
		}
	}
	if action == "record" && session.Source.Kind == "recording" {
		if _, err := history.Materialize(session.Source.Path, overlayRoot); err != nil {
			return "", nil, err
		}
	}
	slotsTouched = true
	if err := bindRouteSlots(filepath.Join(session.ResourceRoot, "routes", action), overlayRoot); err != nil {
		return "", nil, err
	}
	success = true
	return overlayRoot, historyFiles, nil
}

func executionProtocolCwd(action string, plan tool.Object, workspace string) string {
	if action == "record" {
		return workspace
	}
	return planProtocolCwd(plan, workspace)
}

func newExecution(session *Session, action, id, overlayRoot, artifactRoot string, commandArgs, nativeArgs []string, timeout time.Duration, progressFD int, ids []string, material *corpus.Material, admission *Admission, plan tool.Object, historyFiles map[string]string) (Execution, error) {
	workspace := filepath.Join(overlayRoot, "workspace")
	limits := session.Limits
	if timeout > 0 {
		limits.TimeoutNS = int64(timeout)
	}
	route := session.Routes[action]
	provenance, err := executionProvenance(session, action, material)
	if err != nil {
		return Execution{}, err
	}
	connections, err := executionConnections(artifactRoot, action, ids, material, route)
	if err != nil {
		return Execution{}, err
	}
	execution := Execution{
		SchemaVersion: SchemaVersion,
		ExecutionID:   id,
		SessionID:     session.SessionID,
		Command:       action,
		Args:          append([]string(nil), commandArgs...),
		NativeArgs:    append([]string(nil), nativeArgs...),
		Source:        session.Source,
		Executor:      route.Executor,
		Target:        route.Target,
		Provenance:    provenance,
		Workspace:     workspace,
		ProtocolCwd:   executionProtocolCwd(action, plan, workspace),
		Home:          filepath.Join(overlayRoot, "home"),
		Config:        filepath.Join(overlayRoot, "config"),
		Tmp:           filepath.Join(overlayRoot, "tmp"),
		HistorySeed:   filepath.Join(overlayRoot, "history-seed"),
		HistoryFiles:  historyFiles,
		Env:           baseEnvironment(session.AgentType, overlayRoot),
		Connections:   connections,
		ArtifactRoot:  artifactRoot,
		Admission:     admission,
		Limits:        limits,
		StartedAt:     now(),
	}
	runtimeEnv, err := executionRuntimeEnvironment(session, action, overlayRoot)
	if err != nil {
		return Execution{}, err
	}
	execution.RuntimeEnv = runtimeEnv
	if action == "record" {
		artifactID, err := randomID()
		if err != nil {
			return Execution{}, err
		}
		execution.Artifact = corpus.Material{
			ID:          artifactID,
			Kind:        "capture",
			Action:      "record",
			Provenance:  provenance,
			Path:        artifactRoot,
			Connections: append([]string(nil), ids...),
			CreatedAt:   now(),
		}
	} else if material != nil {
		execution.Artifact = *material
	}
	if action == "mock" {
		execution.ProgressFD = progressFD
	}
	return execution, nil
}

func createTransactionLocked(sessionDir, action string, commandArgs, nativeArgs []string, timeout time.Duration, progressFD int, material *corpus.Material, admission *Admission, origin *corpus.OfficialOrigin) (*transactionContext, error) {
	sessionDir, err := CanonicalSessionDir(sessionDir)
	if err != nil {
		return nil, err
	}
	session, err := readSessionUnlocked(sessionDir)
	if err != nil {
		return nil, err
	}
	if action == "record" && origin == nil {
		if err := checkOfficialIdle(sessionDir); err != nil {
			return nil, err
		}
	}
	route, ok := session.Routes[action]
	if !ok || !route.Enabled {
		if action == "record" {
			return nil, errors.New("record source has no executable route: plan, driver binding or protocol cwd is unavailable")
		}
		return nil, fmt.Errorf("%s is not available for this session", action)
	}
	if action != "record" && material == nil {
		return nil, fmt.Errorf("%s requires an input material", action)
	}
	success := false
	routeBound := false
	defer func() {
		if !success && routeBound {
			_ = restoreRouteSlots(session, action)
		}
	}()
	if action == "record" && origin == nil {
		if err := checkOfficialIdle(sessionDir); err != nil {
			return nil, err
		}
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}
	if pending := pendingExecution(index); pending != nil {
		return nil, fmt.Errorf("session has unfinished execution %s", pending.ID)
	}
	var plan tool.Object
	if action == "record" {
		plan, err = LoadPlan(session)
		if err != nil {
			return nil, err
		}
		if session.Source.Kind == "recording" {
			found := false
			for _, source := range index.Artifacts {
				if source.Path != session.Source.Path {
					continue
				}
				if _, err := inspectMaterial(session, source); err != nil {
					return nil, err
				}
				found = true
			}
			if !found {
				return nil, errors.New("record source is absent from material registry")
			}
		}
		if route.Executor != "external-sdk" {
			return nil, errors.New("record source has no executable route")
		}
		if !filepath.IsAbs(route.ProtocolCwd) {
			return nil, errors.New("record source requires an absolute protocol cwd")
		}
	}
	if material != nil {
		current, err := LatestArtifact(sessionDir, session)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(current, material) {
			return nil, errors.New("current recording changed before execution")
		}
		inspection, err := inspectMaterial(session, *current)
		if err != nil {
			return nil, err
		}
		plan = inspection.Plan
		if _, err := executionConnections(sessionDir, action, current.Connections, current, route); err != nil {
			return nil, err
		}
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	dir := ExecutionDir(sessionDir, id)
	if err := os.MkdirAll(filepath.Join(dir, "receipts"), 0o700); err != nil {
		return nil, err
	}
	artifactRoot := filepath.Join(dir, action)
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		return nil, err
	}
	overlayRoot, historyFiles, err := createExecutionOverlay(session, action, dir, plan, material)
	if err != nil {
		return nil, err
	}
	routeBound = true
	ids := ConnectionIDs(session.Source, plan)
	if material != nil {
		if len(material.Connections) > 0 {
			ids = append([]string(nil), material.Connections...)
		}
	}
	execution, err := newExecution(session, action, id, overlayRoot, artifactRoot, commandArgs, nativeArgs, timeout, progressFD, ids, material, admission, plan, historyFiles)
	if err != nil {
		return nil, err
	}
	if origin != nil {
		execution.Provenance = "official"
		execution.Artifact.Provenance = "official"
		execution.Artifact.OfficialOrigin = origin
	}
	if err := execution.Validate(*session); err != nil {
		return nil, err
	}
	descriptorPath := filepath.Join(dir, "execution.json")
	if err := writeExclusive(descriptorPath, execution); err != nil {
		return nil, err
	}
	descriptorHash, err := tool.FileDigest(descriptorPath)
	if err != nil {
		return nil, err
	}
	indexConnections := make([]IndexConnection, 0, len(execution.Connections))
	for _, connection := range execution.Connections {
		indexConnections = append(indexConnections, IndexConnection{ID: connection.ID, Status: "pending"})
	}
	executionStatus := "running"
	if action == "mock" {
		executionStatus = "open"
	}
	if err := updateIndex(sessionDir, func(value *Index) error {
		for _, existing := range value.Executions {
			if existing.ID == id {
				return errors.New("duplicate execution identity")
			}
		}
		value.Executions = append(value.Executions, IndexExecution{
			ID:               id,
			Command:          action,
			DescriptorSHA256: descriptorHash,
			Status:           executionStatus,
			StartedAt:        execution.StartedAt,
			UpdatedAt:        now(),
			Connections:      indexConnections,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	success = true
	return newTransactionContext(session, plan, execution, sessionDir), nil
}

func beginActionLock(sessionDir string) (*Lock, bool, error) {
	lock, err := actionLock(sessionDir, true)
	if err == nil {
		return lock, true, nil
	}
	if !errors.Is(err, ErrBusy) {
		return nil, false, err
	}
	lock, err = actionLock(sessionDir, false)
	if err != nil {
		return nil, false, err
	}
	return lock, false, nil
}

func validateMockJoin(execution Execution, nativeArgs []string, timeout time.Duration, progressFD int, material *corpus.Material) error {
	if material == nil {
		return errors.New("mock join requires an input material")
	}
	if filepath.Clean(material.Path) != filepath.Clean(execution.Artifact.Path) ||
		material.PlanSHA256 != execution.Artifact.PlanSHA256 ||
		material.ManifestSHA256 != execution.Artifact.ManifestSHA256 ||
		material.RecordingSHA256 != execution.Artifact.RecordingSHA256 {
		return errors.New("mock join material differs from the open execution")
	}
	if int64(timeout) != execution.Limits.TimeoutNS {
		return errors.New("mock join timeout differs from the open execution")
	}
	if progressFD != execution.ProgressFD {
		return errors.New("mock join progress descriptor differs from the open execution")
	}
	if !slices.Equal(nativeArgs, execution.NativeArgs) {
		return errors.New("mock join native arguments differ from the open execution")
	}
	return nil
}

func connectionClaimPath(execution Execution, id string) (string, error) {
	var connection *ExecutionConnection
	for i := range execution.Connections {
		if execution.Connections[i].ID == id {
			connection = &execution.Connections[i]
			break
		}
	}
	if connection == nil {
		return "", fmt.Errorf("undeclared %s connection %s", execution.Command, id)
	}
	path := filepath.Join(filepath.Dir(connection.Receipt), "."+id+".started")
	if execution.Command == "record" {
		path = filepath.Join(filepath.Dir(execution.ArtifactRoot), "receipts", "."+id+".started")
	}
	return path, nil
}

func createConnectionClaim(path string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	data, encodeErr := json.Marshal(tool.Object{"pid": os.Getpid()})
	if encodeErr == nil {
		_, encodeErr = f.Write(data)
	}
	if err := errors.Join(encodeErr, f.Close()); err != nil {
		return err
	}
	success = true
	return nil
}

func markConnection(sessionDir, executionID, connection, status, executionStatus string) error {
	return updateIndex(sessionDir, func(index *Index) error {
		for i := range index.Executions {
			if index.Executions[i].ID != executionID {
				continue
			}
			setConnectionStarted(&index.Executions[i], connection, status)
			index.Executions[i].Status = executionStatus
			index.Executions[i].UpdatedAt = now()
			return nil
		}
		return errors.New("execution is absent from index")
	})
}
