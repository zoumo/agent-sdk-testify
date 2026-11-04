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

type ActionRun struct {
	Session      *Session
	Plan         tool.Object
	Execution    Execution
	Connection   string
	Dir          string
	OverlayRoot  string
	ArtifactRoot string
	lock         *Lock
}

func (a *ActionRun) Close() error {
	if a == nil {
		return nil
	}
	return a.lock.Close()
}

func canonicalSessionDir(sessionDir string) (string, error) {
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
	dir, err := canonicalSessionDir(sessionDir)
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
	return session, nil
}

func OpenSession(sessionDir string) (*Session, error) {
	return readSessionUnlocked(sessionDir)
}

func OpenActionSession(sessionDir string) (*Session, error) {
	session, err := OpenSession(sessionDir)
	if err != nil {
		return nil, err
	}
	if err := checkOpen(sessionDir); err != nil {
		return nil, err
	}
	return session, nil
}

func checkOpen(sessionDir string) error {
	if _, err := os.Stat(filepath.Join(sessionDir, "closed")); err == nil {
		return errors.New("session is finished")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
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

func newExecution(session *Session, action, id, overlayRoot, artifactRoot string, commandArgs, nativeArgs []string, timeout time.Duration, ids []string, material *corpus.Material, admission *Admission, plan tool.Object, historyFiles map[string]string) (Execution, error) {
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
	return execution, nil
}

func createActionLocked(sessionDir, action string, commandArgs []string, timeout time.Duration, material *corpus.Material, admission *Admission, held *Lock, origin *corpus.OfficialOrigin) (*ActionRun, error) {
	sessionDir, err := canonicalSessionDir(sessionDir)
	if err != nil {
		return nil, err
	}
	session, err := readSessionUnlocked(sessionDir)
	if err != nil {
		return nil, err
	}
	if err := checkOpen(sessionDir); err != nil {
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
	lock := held
	if lock == nil {
		lock, err = actionLock(sessionDir, true)
		if err != nil {
			return nil, err
		}
	}
	success := false
	defer func() {
		if !success {
			_ = lock.Close()
		}
	}()
	routeBound := false
	defer func() {
		if !success && routeBound {
			_ = restoreRouteSlots(session, action)
		}
	}()
	if err := checkOpen(sessionDir); err != nil {
		return nil, err
	}
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
	execution, err := newExecution(session, action, id, overlayRoot, artifactRoot, commandArgs, nil, timeout, ids, material, admission, plan, historyFiles)
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
			Status:           "running",
			StartedAt:        execution.StartedAt,
			UpdatedAt:        now(),
			Connections:      indexConnections,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	run := &ActionRun{Session: session, Plan: plan, Execution: execution, Dir: dir, OverlayRoot: overlayRoot, ArtifactRoot: artifactRoot, lock: lock}
	success = true
	return run, nil
}

// BeginMock either creates the execution that declares all connections or
// joins its unstarted connection slot. The returned lock covers the peer life.
func BeginMock(sessionDir, connection string, nativeArgs []string, timeout time.Duration, progressFD int, material *corpus.Material) (*ActionRun, error) {
	if material == nil {
		return nil, errors.New("mock requires an input material")
	}
	sessionDir, err := canonicalSessionDir(sessionDir)
	if err != nil {
		return nil, err
	}
	session, err := readSessionUnlocked(sessionDir)
	if err != nil {
		return nil, err
	}
	if err := checkOpen(sessionDir); err != nil {
		return nil, err
	}
	route, ok := session.Routes["mock"]
	if !ok || !route.Enabled {
		return nil, errors.New("mock is not available for this session")
	}
	lock, exclusive, err := beginMockLock(sessionDir)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = lock.Close()
		}
	}()
	routeBound := false
	defer func() {
		if !success && routeBound {
			_ = restoreRouteSlots(session, "mock")
		}
	}()
	if err := checkOpen(sessionDir); err != nil {
		return nil, err
	}
	current, err := LatestArtifact(sessionDir, session)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, material) {
		return nil, errors.New("current recording changed before execution")
	}
	if _, err := executionConnections(sessionDir, "mock", current.Connections, current, route); err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}
	if pending := pendingExecution(index); pending != nil {
		for _, existing := range index.Executions {
			if existing.ID != pending.ID {
				continue
			}
			if connectionStarted(&existing, connection) {
				return nil, fmt.Errorf("mock connection %s already started for execution %s", connection, existing.ID)
			}
			run, joinErr := joinMock(sessionDir, session, &existing, connection, nativeArgs, timeout, progressFD, material, lock)
			if joinErr != nil {
				return nil, joinErr
			}
			success = true
			return run, nil
		}
		return nil, fmt.Errorf("session has unfinished execution %s", pending.ID)
	}
	if !exclusive {
		return nil, errors.New("session action busy: wait for the active mock execution")
	}
	inspection, err := inspectMaterial(session, *material)
	if err != nil {
		return nil, err
	}
	plan := inspection.Plan
	ids := ConnectionIDs(session.Source, plan)
	if material != nil && len(material.Connections) > 0 {
		ids = append([]string(nil), material.Connections...)
	}
	found := false
	for _, id := range ids {
		if id == connection {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("undeclared mock connection %s", connection)
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	dir := ExecutionDir(sessionDir, id)
	if err := os.MkdirAll(filepath.Join(dir, "receipts"), 0o700); err != nil {
		return nil, err
	}
	artifactRoot := filepath.Join(dir, "mock")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		return nil, err
	}
	overlayRoot, historyFiles, err := createExecutionOverlay(session, "mock", dir, plan, material)
	if err != nil {
		return nil, err
	}
	routeBound = true
	commandArgs := append([]string{"--session-dir", sessionDir, "--connection", connection}, nativeArgs...)
	execution, err := newExecution(session, "mock", id, overlayRoot, artifactRoot, commandArgs, nativeArgs, timeout, ids, material, nil, plan, historyFiles)
	if err != nil {
		return nil, err
	}
	execution.ProgressFD = progressFD
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
	for _, c := range execution.Connections {
		indexConnections = append(indexConnections, IndexConnection{ID: c.ID, Status: "pending"})
	}
	if err := updateIndex(sessionDir, func(value *Index) error {
		value.Executions = append(value.Executions, IndexExecution{
			ID:               id,
			Command:          "mock",
			DescriptorSHA256: descriptorHash,
			Status:           "open",
			StartedAt:        execution.StartedAt,
			UpdatedAt:        now(),
			Connections:      indexConnections,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := claimMockConnection(execution, connection); err != nil {
		return nil, err
	}
	if err := markMockConnection(sessionDir, id, connection, "running", "open"); err != nil {
		return nil, err
	}
	if err := lock.Downgrade(); err != nil {
		return nil, err
	}
	run := &ActionRun{Session: session, Plan: plan, Execution: execution, Connection: connection, Dir: dir, OverlayRoot: overlayRoot, ArtifactRoot: artifactRoot, lock: lock}
	success = true
	return run, nil
}

func beginMockLock(sessionDir string) (*Lock, bool, error) {
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

func joinMock(sessionDir string, session *Session, indexed *IndexExecution, connection string, nativeArgs []string, timeout time.Duration, progressFD int, material *corpus.Material, lock *Lock) (*ActionRun, error) {
	dir := ExecutionDir(sessionDir, indexed.ID)
	var execution Execution
	if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
		return nil, err
	}
	if err := execution.Validate(*session); err != nil {
		return nil, err
	}
	found := false
	for _, c := range execution.Connections {
		if c.ID == connection {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("undeclared mock connection %s", connection)
	}
	if err := validateMockJoin(execution, nativeArgs, timeout, progressFD, material); err != nil {
		return nil, err
	}
	if err := claimMockConnection(execution, connection); err != nil {
		return nil, err
	}
	if err := markMockConnection(sessionDir, execution.ExecutionID, connection, "running", "open"); err != nil {
		return nil, err
	}
	plan, err := LoadPlan(session)
	if err != nil {
		return nil, err
	}
	return &ActionRun{Session: session, Plan: plan, Execution: execution, Connection: connection, Dir: dir, OverlayRoot: filepath.Dir(execution.Home), ArtifactRoot: execution.ArtifactRoot, lock: lock}, nil
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

func claimMockConnection(execution Execution, id string) error {
	var connection *ExecutionConnection
	for i := range execution.Connections {
		if execution.Connections[i].ID == id {
			connection = &execution.Connections[i]
			break
		}
	}
	if connection == nil {
		return fmt.Errorf("undeclared mock connection %s", id)
	}
	path := filepath.Join(filepath.Dir(connection.Receipt), "."+id+".started")
	if execution.Command == "record" {
		path = filepath.Join(filepath.Dir(execution.ArtifactRoot), "receipts", "."+id+".started")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("mock connection %s already started: %w", id, err)
	}
	data, encodeErr := json.Marshal(tool.Object{"pid": os.Getpid()})
	if encodeErr == nil {
		_, encodeErr = f.Write(data)
	}
	return errors.Join(encodeErr, f.Close())
}

func markMockConnection(sessionDir, executionID, connection, status, executionStatus string) error {
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

func FinishAction(run *ActionRun, status, artifactID string) error {
	if run == nil {
		return nil
	}
	err := updateIndex(filepath.Dir(filepath.Dir(run.Dir)), func(index *Index) error {
		for i := range index.Executions {
			if index.Executions[i].ID != run.Execution.ExecutionID {
				continue
			}
			index.Executions[i].Status = status
			index.Executions[i].UpdatedAt = now()
			index.Executions[i].ArtifactID = artifactID
			for j := range index.Executions[i].Connections {
				if index.Executions[i].Connections[j].Status == "pending" ||
					index.Executions[i].Connections[j].Status == "running" {
					index.Executions[i].Connections[j].Status = status
				}
			}
			return nil
		}
		return errors.New("execution is absent from index")
	})
	if err != nil {
		return err
	}
	return CleanupAction(run)
}

// CleanupAction restores the tool-owned route before verification can be adopted.
func CleanupAction(run *ActionRun) error {
	if run.Execution.Command == "record" {
		return restoreRouteSlots(run.Session, run.Execution.Command)
	}
	return nil
}

// CompleteMockConnection records one peer result without closing the execution
// while another declared peer is still pending or running.
func CompleteMockConnection(run *ActionRun, status string) error {
	if run == nil || run.Connection == "" {
		return errors.New("mock completion requires an execution and connection")
	}
	sessionDir := filepath.Dir(filepath.Dir(run.Dir))
	complete := false
	err := updateIndex(sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != run.Execution.ExecutionID {
				continue
			}
			found := false
			allBound := true
			failed := false
			for j := range execution.Connections {
				connection := &execution.Connections[j]
				if connection.ID == run.Connection {
					connection.Status = status
					found = true
				}
				switch connection.Status {
				case "pending", "running", "open":
					allBound = false
				case "passed":
				default:
					failed = true
				}
			}
			if !found {
				return fmt.Errorf("mock connection %s is absent from execution index", run.Connection)
			}
			execution.UpdatedAt = now()
			if !allBound {
				execution.Status = "open"
			} else if failed {
				execution.Status = "failed"
				complete = true
			} else {
				execution.Status = "passed"
				complete = true
			}
			return nil
		}
		return errors.New("execution is absent from index")
	})
	if err != nil || !complete {
		return err
	}
	return restoreRouteSlots(run.Session, "mock")
}

func PublishCapture(sessionDir string, run *ActionRun, artifact corpus.Material) error {
	if _, err := inspectMaterial(run.Session, artifact); err != nil {
		return err
	}
	if err := validateRecordReport(artifact.Path); err != nil {
		return err
	}
	for _, connection := range run.Execution.Connections {
		if err := validateCaptureTransport(connection.Receipt, 0); err != nil {
			return fmt.Errorf("connection %s: %w", connection.ID, err)
		}
	}
	if err := restoreRouteSlots(run.Session, "record"); err != nil {
		return err
	}
	for _, action := range []string{"mock"} {
		if route, ok := run.Session.Routes[action]; ok && route.Enabled {
			if err := restoreRouteSlots(run.Session, action); err != nil {
				return err
			}
		}
	}
	// Authentication links and route cleanup must succeed before any publication.
	if err := ScrubExecutionAuthentication(run); err != nil {
		return err
	}
	materialRoot := filepath.Join(sessionDir, "materials", artifact.ID)
	stage, err := os.MkdirTemp(filepath.Join(sessionDir, "materials"), ".record-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyMaterial(artifact.Path, stage); err != nil {
		return err
	}
	files, err := history.HashTree(stage)
	if err != nil {
		return err
	}
	delete(files, "material.json")
	artifact.Path = materialRoot
	artifact.Files = files
	if err := writeExclusive(filepath.Join(stage, "material.json"), artifact); err != nil {
		return err
	}
	if _, err := os.Lstat(materialRoot); !os.IsNotExist(err) {
		return errors.New("material identity already exists")
	}
	if err := os.Rename(stage, materialRoot); err != nil {
		return err
	}
	if _, err := inspectMaterial(run.Session, artifact); err != nil {
		return err
	}
	descriptorSHA256, err := bindExecutionCapture(run, artifact)
	if err != nil {
		return err
	}
	// All fallible finalization precedes the atomic selection change. A failed
	// publication can leave an unselected immutable package, never a partial current.
	if err := updateIndex(sessionDir, func(index *Index) error {
		for _, existing := range index.Artifacts {
			if existing.ID == artifact.ID {
				return errors.New("artifact identity already exists")
			}
		}
		index.Artifacts = append(index.Artifacts, artifact)
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID == run.Execution.ExecutionID {
				execution.ArtifactID = artifact.ID
				execution.DescriptorSHA256 = descriptorSHA256
				execution.Status = "passed"
				execution.UpdatedAt = now()
				for j := range execution.Connections {
					execution.Connections[j].Status = "passed"
				}
				return nil
			}
		}
		return errors.New("execution is absent from index")
	}); err != nil {
		return err
	}
	return writeCurrent(sessionDir, run.Session, artifact.ID)
}

func bindExecutionCapture(run *ActionRun, artifact corpus.Material) (string, error) {
	var ids []string
	for _, connection := range run.Execution.Connections {
		ids = append(ids, connection.ID)
	}
	connections, err := executionConnections(run.ArtifactRoot, "record", ids, &artifact, run.Session.Routes["record"])
	if err != nil {
		return "", err
	}
	run.Execution.Connections = connections
	run.Execution.Artifact = artifact
	if err := run.Execution.Validate(*run.Session); err != nil {
		return "", err
	}
	path := filepath.Join(run.Dir, "execution.json")
	if err := writeAtomic(path, run.Execution); err != nil {
		return "", err
	}
	return tool.FileDigest(path)
}
