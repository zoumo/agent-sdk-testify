package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	Created      bool
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
	hash, err := digestFile(SessionPath(dir))
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

func SessionOpen(sessionDir string) error {
	if _, err := readSessionUnlocked(sessionDir); err != nil {
		return err
	}
	return checkOpen(sessionDir)
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
	if err := mutate(index); err != nil {
		return err
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

func executionConnections(dir, action string, ids []string, material string) ([]ExecutionConnection, []string) {
	var connections []ExecutionConnection
	var receipts []string
	for _, id := range ids {
		var connection ExecutionConnection
		switch action {
		case "record":
			base := dir
			if id == "probe" {
				base = filepath.Join(dir, "remote-probe")
			}
			connection = ExecutionConnection{ID: id, Receipt: filepath.Join(base, "raw", "transport.json")}
		case "verify":
			base := dir
			if id == "probe" {
				base = filepath.Join(dir, "remote-probe")
			}
			connection = ExecutionConnection{ID: id, Receipt: filepath.Join(base, "replay-peer.json")}
		case "mock":
			connection = ExecutionConnection{
				ID:             id,
				Material:       material,
				Receipt:        filepath.Join(dir, "receipts", id+".json"),
				ProcessReceipt: filepath.Join(dir, "receipts", id+".process.json"),
			}
		}
		connections = append(connections, connection)
		receipts = append(receipts, connection.Receipt)
		if connection.ProcessReceipt != "" {
			receipts = append(receipts, connection.ProcessReceipt)
		}
	}
	return connections, receipts
}

func createExecutionOverlay(session *Session, action, dir string, plan tool.Object, material *Artifact) (string, map[string]string, error) {
	route, ok := session.Routes[action]
	if !ok || !route.Enabled {
		return "", nil, fmt.Errorf("%s is not available for this session", action)
	}
	overlayRoot := filepath.Join(dir, "overlay")
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
	if err := copyTree(filepath.Join(session.ResourceRoot, "baseline"), overlayRoot); err != nil {
		return "", nil, err
	}
	if action == "mock" || action == "verify" {
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
	if (action == "mock" || action == "verify") && material != nil {
		var err error
		historyFiles, err = history.Materialize(material.Path, overlayRoot)
		if err != nil {
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
	if action == "record" || action == "verify" {
		return workspace
	}
	return planProtocolCwd(plan, workspace)
}

func newExecution(session *Session, action, id, overlayRoot, artifactRoot string, commandArgs []string, timeout time.Duration, ids []string, material *Artifact, plan tool.Object, historyFiles map[string]string) (Execution, error) {
	workspace := filepath.Join(overlayRoot, "workspace")
	limits := session.Limits
	if timeout > 0 {
		limits.TimeoutMS = timeout.Milliseconds()
	}
	route := session.Routes[action]
	provenance, err := executionProvenance(session, action, material)
	if err != nil {
		return Execution{}, err
	}
	materialPath := ""
	if material != nil {
		materialPath = material.Path
	}
	connections, receipts := executionConnections(artifactRoot, action, ids, materialPath)
	execution := Execution{
		SchemaVersion: SchemaVersion,
		ExecutionID:   id,
		SessionID:     session.SessionID,
		Command:       action,
		Args:          append([]string(nil), commandArgs...),
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
		Env:           executionEnvironment(session, action, overlayRoot),
		Connections:   connections,
		Receipts:      receipts,
		ArtifactRoot:  artifactRoot,
		Limits:        limits,
		StartedAt:     now(),
	}
	if action == "record" {
		artifactID, err := randomID()
		if err != nil {
			return Execution{}, err
		}
		execution.Artifact = Artifact{
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

func createAction(sessionDir, action string, commandArgs []string, timeout time.Duration, material *Artifact) (*ActionRun, error) {
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
	route, ok := session.Routes[action]
	if !ok || !route.Enabled {
		return nil, fmt.Errorf("%s is not available for this session", action)
	}
	if action != "record" && material == nil {
		return nil, fmt.Errorf("%s requires an input material", action)
	}
	lock, err := actionLock(sessionDir, true)
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
			_ = restoreRouteSlots(session, action)
		}
	}()
	if err := checkOpen(sessionDir); err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}
	if pending := pendingExecution(index); pending != nil {
		return nil, fmt.Errorf("session has unfinished execution %s", pending.ID)
	}
	plan, err := LoadPlan(session)
	if err != nil {
		return nil, err
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
	ids := connectionIDs(session.Source, plan)
	if material != nil {
		if len(material.Connections) > 0 {
			ids = append([]string(nil), material.Connections...)
		}
	}
	execution, err := newExecution(session, action, id, overlayRoot, artifactRoot, commandArgs, timeout, ids, material, plan, historyFiles)
	if err != nil {
		return nil, err
	}
	if err := execution.Validate(*session); err != nil {
		return nil, err
	}
	descriptorPath := filepath.Join(dir, "execution.json")
	if err := writeExclusive(descriptorPath, execution); err != nil {
		return nil, err
	}
	descriptorHash, err := digestFile(descriptorPath)
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
	run := &ActionRun{Session: session, Plan: plan, Execution: execution, Dir: dir, OverlayRoot: overlayRoot, ArtifactRoot: artifactRoot, Created: true, lock: lock}
	success = true
	return run, nil
}

func BeginRecord(sessionDir string, args []string, timeout time.Duration) (*ActionRun, error) {
	return createAction(sessionDir, "record", args, timeout, nil)
}

func BeginVerify(sessionDir string, args []string, timeout time.Duration, material *Artifact) (*ActionRun, error) {
	return createAction(sessionDir, "verify", args, timeout, material)
}

// BeginMock either creates the execution that declares all connections or
// joins its unstarted connection slot. The returned lock covers the peer life.
func BeginMock(sessionDir, connection string, commandArgs []string, timeout time.Duration, material *Artifact) (*ActionRun, error) {
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
			run, joinErr := joinMock(sessionDir, session, &existing, connection, lock)
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
	plan, err := LoadPlan(session)
	if err != nil {
		return nil, err
	}
	ids := connectionIDs(session.Source, plan)
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
	execution, err := newExecution(session, "mock", id, overlayRoot, artifactRoot, commandArgs, timeout, ids, material, plan, historyFiles)
	if err != nil {
		return nil, err
	}
	if err := execution.Validate(*session); err != nil {
		return nil, err
	}
	descriptorPath := filepath.Join(dir, "execution.json")
	if err := writeExclusive(descriptorPath, execution); err != nil {
		return nil, err
	}
	descriptorHash, err := digestFile(descriptorPath)
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
	run := &ActionRun{Session: session, Plan: plan, Execution: execution, Connection: connection, Dir: dir, OverlayRoot: overlayRoot, ArtifactRoot: artifactRoot, Created: true, lock: lock}
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

func joinMock(sessionDir string, session *Session, indexed *IndexExecution, connection string, lock *Lock) (*ActionRun, error) {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("mock connection %s already started: %w", id, err)
	}
	return f.Close()
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
	if run.Execution.Command == "record" || run.Execution.Command == "verify" {
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

func PublishCapture(sessionDir string, run *ActionRun, artifact Artifact) error {
	return updateIndex(sessionDir, func(index *Index) error {
		for _, existing := range index.Artifacts {
			if existing.ID == artifact.ID {
				return errors.New("artifact identity already exists")
			}
		}
		index.Artifacts = append(index.Artifacts, artifact)
		index.LatestCapture = artifact.ID
		for i := range index.Executions {
			if index.Executions[i].ID == run.Execution.ExecutionID {
				index.Executions[i].ArtifactID = artifact.ID
			}
		}
		return nil
	})
}
