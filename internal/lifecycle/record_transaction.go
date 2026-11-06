package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// RecordRequest contains the immutable inputs shared by every connection of one
// record execution.
type RecordRequest struct {
	SessionDir string
	Connection string
	Args       []string
	Timeout    time.Duration
	Target     string
	ResultFD   int
}

// RecordProbeTracker records native process state during official preparation.
// The transaction supplies it separately so RecordContext remains read-only.
type RecordProbeTracker func(state string, pid int) error

// RecordPolicy supplies action-specific behavior. The transaction owns every
// lock, index transition, route, receipt, notification, and publication step.
type RecordPolicy interface {
	Prepare(context.Context, RecordContext, RecordProbeTracker) (tool.Object, error)
	Capture(context.Context, RecordContext) error
	Package(context.Context, RecordContext) (*corpus.Material, error)
}

// RecordContext is a read-only snapshot of transaction-owned state.
type RecordContext struct {
	session      *Session
	plan         tool.Object
	execution    Execution
	connection   string
	sessionDir   string
	dir          string
	overlayRoot  string
	artifactRoot string
}

func (c RecordContext) Session() Session {
	session := *c.session
	session.Tool.Args = slices.Clone(c.session.Tool.Args)
	session.HistorySeedFiles = maps.Clone(c.session.HistorySeedFiles)
	session.Env = maps.Clone(c.session.Env)
	session.Routes = make(map[string]Route, len(c.session.Routes))
	for name, route := range c.session.Routes {
		route.Env = maps.Clone(route.Env)
		route.RuntimeEnv = slices.Clone(route.RuntimeEnv)
		connections := route.Connections
		route.Connections = make([]ConnectionTemplate, len(connections))
		for i, connection := range connections {
			connection.Args = slices.Clone(connection.Args)
			route.Connections[i] = connection
		}
		session.Routes[name] = route
	}
	return session
}

func (c RecordContext) Plan() tool.Object {
	return cloneRecordObject(c.plan)
}

func (c RecordContext) Execution() Execution {
	return cloneRecordExecution(c.execution)
}

func (c RecordContext) IsOfficialCapture() bool {
	return c.execution.Artifact.OfficialOrigin != nil
}

func (c RecordContext) Connection() string {
	return c.connection
}

func (c RecordContext) SessionDir() string {
	return c.sessionDir
}

func (c RecordContext) ExecutionDir() string {
	return c.dir
}

func (c RecordContext) OverlayRoot() string {
	return c.overlayRoot
}

func (c RecordContext) ArtifactRoot() string {
	return c.artifactRoot
}

func (c RecordContext) ConnectionReceipt() string {
	for _, connection := range c.execution.Connections {
		if connection.ID == c.connection {
			return connection.Receipt
		}
	}
	return ""
}

func (c RecordContext) ProcessReceipt() string {
	return filepath.Join(c.dir, "receipts", c.connection+".native-process.json")
}

type recordTransaction struct {
	session      *Session
	plan         tool.Object
	execution    Execution
	connection   string
	sessionDir   string
	dir          string
	overlayRoot  string
	artifactRoot string
	lock         *Lock
}

func (r *recordTransaction) context() RecordContext {
	return RecordContext{
		session:      r.session,
		plan:         r.plan,
		execution:    r.execution,
		connection:   r.connection,
		sessionDir:   r.sessionDir,
		dir:          r.dir,
		overlayRoot:  r.overlayRoot,
		artifactRoot: r.artifactRoot,
	}
}

func (r *recordTransaction) close() error {
	if r == nil {
		return nil
	}
	return r.lock.Close()
}

func (r *recordTransaction) prepare(ctx context.Context, policy RecordPolicy) (plan tool.Object, err error) {
	lock, err := lockFile(filepath.Join(r.dir, "record-finalize.lock"), true, true)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	prepared, err := policy.Prepare(ctx, r.context(), r.trackNativeProbe)
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, errors.New("record policy prepared no plan")
	}
	return prepared, nil
}

func (r *recordTransaction) trackNativeProbe(state string, pid int) error {
	return writeAtomic(filepath.Join(r.dir, "receipts", r.connection+".probe.native-process.json"), tool.Object{"state": state, "pid": pid})
}

// finalize serializes completion across declared connections and owns every
// state transition from connection outcome through result delivery.
func (r *recordTransaction) finalize(ctx context.Context, policy RecordPolicy, stream *os.File, runErr error) (err error) {
	lock, err := lockFile(filepath.Join(r.dir, "record-finalize.lock"), true, true)
	if err != nil {
		err = errors.Join(runErr, err)
		return errors.Join(err, writeRecordResult(ctx, r, stream, err))
	}
	defer func() { err = errors.Join(err, lock.Close()) }()

	status := recordStatus(runErr)
	ready, allPassed, settleErr := r.settleConnection(status)
	if settleErr != nil {
		err = errors.Join(runErr, settleErr)
		return errors.Join(err, writeRecordResult(ctx, r, stream, err))
	}
	if !ready {
		return writeRecordResult(ctx, r, stream, runErr)
	}
	if !allPassed {
		err = errors.Join(runErr, errors.New("record execution has failed connections"), r.fail())
		return errors.Join(err, writeRecordResult(ctx, r, stream, err))
	}

	artifact, err := policy.Package(ctx, r.context())
	if err != nil {
		err = errors.Join(err, r.fail())
		return errors.Join(err, writeRecordResult(ctx, r, stream, err))
	}
	if artifact == nil {
		err = errors.New("record policy produced no material")
		err = errors.Join(err, r.fail())
		return errors.Join(err, writeRecordResult(ctx, r, stream, err))
	}
	if err := r.publish(*artifact); err != nil {
		err = errors.Join(err, r.fail())
		return errors.Join(err, writeRecordResult(ctx, r, stream, err))
	}
	return writeRecordResult(ctx, r, stream, nil)
}

func (r *recordTransaction) settleConnection(status string) (ready, passed bool, err error) {
	passed = true
	err = updateIndex(r.sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != r.execution.ExecutionID {
				continue
			}
			setConnectionStarted(execution, r.connection, status)
			ready = true
			for _, connection := range execution.Connections {
				if connection.Status == "pending" || connection.Status == "running" {
					ready = false
				}
				if connection.Status != "passed" {
					passed = false
				}
			}
			execution.Status = "open"
			execution.UpdatedAt = now()
			return nil
		}
		return errors.New("record execution absent from index")
	})
	return ready, passed, err
}

func (r *recordTransaction) fail() error {
	errs := []error{r.restoreRoutes(), scrubExecutionAuthentication(r.session, r.overlayRoot)}
	errs = append(errs, updateIndex(r.sessionDir, func(index *Index) error {
		for i := range index.Executions {
			execution := &index.Executions[i]
			if execution.ID != r.execution.ExecutionID {
				continue
			}
			execution.Status = "failed"
			execution.ArtifactID = ""
			execution.UpdatedAt = now()
			for j := range execution.Connections {
				if execution.Connections[j].Status == "pending" || execution.Connections[j].Status == "running" {
					execution.Connections[j].Status = "failed"
				}
			}
			return nil
		}
		return errors.New("record execution absent from index")
	}))
	return errors.Join(errs...)
}

func (r *recordTransaction) restoreRoutes() error {
	var errs []error
	// Official SDK history reads may continue after protocol EOF. Its
	// orchestrator restores the record route after the driver has finished.
	if r.execution.Artifact.OfficialOrigin == nil {
		errs = append(errs, restoreRouteSlots(r.session, "record"))
	}
	if route, ok := r.session.Routes["mock"]; ok && route.Enabled {
		errs = append(errs, restoreRouteSlots(r.session, "mock"))
	}
	return errors.Join(errs...)
}

func (r *recordTransaction) publish(artifact corpus.Material) error {
	if _, err := inspectMaterial(r.session, artifact); err != nil {
		return err
	}
	if err := validateRecordReport(artifact.Path); err != nil {
		return err
	}
	for _, connection := range r.execution.Connections {
		if err := validateCaptureTransport(connection.Receipt, 0); err != nil {
			return fmt.Errorf("connection %s: %w", connection.ID, err)
		}
	}
	if err := r.restoreRoutes(); err != nil {
		return err
	}
	if err := scrubExecutionAuthentication(r.session, r.overlayRoot); err != nil {
		return err
	}
	materialRoot := filepath.Join(r.sessionDir, "materials", artifact.ID)
	stage, err := os.MkdirTemp(filepath.Join(r.sessionDir, "materials"), ".record-")
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
	if _, err := inspectMaterial(r.session, artifact); err != nil {
		return err
	}
	execution, descriptorSHA256, err := bindRecordCapture(r.session, r.execution, r.artifactRoot, artifact)
	if err != nil {
		return err
	}
	// All fallible finalization precedes the atomic selection change.
	if err := updateIndex(r.sessionDir, func(index *Index) error {
		for _, existing := range index.Artifacts {
			if existing.ID == artifact.ID {
				return errors.New("artifact identity already exists")
			}
		}
		index.Artifacts = append(index.Artifacts, artifact)
		for i := range index.Executions {
			indexed := &index.Executions[i]
			if indexed.ID != execution.ExecutionID {
				continue
			}
			indexed.ArtifactID = artifact.ID
			indexed.DescriptorSHA256 = descriptorSHA256
			indexed.Status = "passed"
			indexed.UpdatedAt = now()
			for j := range indexed.Connections {
				indexed.Connections[j].Status = "passed"
			}
			return nil
		}
		return errors.New("record execution absent from index")
	}); err != nil {
		return err
	}
	if err := writeCurrent(r.sessionDir, r.session, artifact.ID); err != nil {
		return err
	}
	r.execution = execution
	return nil
}

func bindRecordCapture(session *Session, execution Execution, artifactRoot string, artifact corpus.Material) (Execution, string, error) {
	ids := make([]string, 0, len(execution.Connections))
	for _, connection := range execution.Connections {
		ids = append(ids, connection.ID)
	}
	connections, err := executionConnections(artifactRoot, "record", ids, &artifact, session.Routes["record"])
	if err != nil {
		return Execution{}, "", err
	}
	execution.Connections = connections
	execution.Artifact = artifact
	if err := execution.Validate(*session); err != nil {
		return Execution{}, "", err
	}
	path := filepath.Join(filepath.Dir(artifactRoot), "execution.json")
	if err := writeAtomic(path, execution); err != nil {
		return Execution{}, "", err
	}
	digest, err := tool.FileDigest(path)
	if err != nil {
		return Execution{}, "", err
	}
	return execution, digest, nil
}

func writeRecordResult(ctx context.Context, run *recordTransaction, stream *os.File, runErr error) error {
	dir := run.dir
	if runErr != nil {
		if err := tool.WriteJSON(filepath.Join(dir, "receipts", run.connection+".failure.json"), tool.Object{"status": "failed", "error": runErr.Error()}); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return writeActionResult(ctx, resultStateFromRecord(run), stream, runErr)
}

func resultStateFromRecord(run *recordTransaction) actionResultState {
	return actionResultState{
		session:    run.session,
		execution:  run.execution,
		connection: run.connection,
		dir:        run.dir,
	}
}

// RunRecord owns one record connection from allocation or join through result
// delivery and lock release.
func RunRecord(ctx context.Context, request RecordRequest, policy RecordPolicy) (runErr error) {
	if policy == nil {
		return errors.New("record policy is required")
	}
	if request.Connection == "" {
		request.Connection = "main"
	}
	result, err := ResultStream(request.ResultFD)
	if err != nil {
		return err
	}
	if result != nil {
		defer result.Close()
	}
	run, err := beginRecordTransaction(request)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, run.close()) }()
	completed := false
	defer func() {
		finalErr := runErr
		if !completed && finalErr == nil {
			finalErr = errors.New("record transaction aborted")
		}
		runErr = errors.Join(runErr, run.finalize(ctx, policy, result, finalErr))
	}()

	if err := notifyActionStarted(ctx, resultStateFromRecord(run), result); err != nil {
		return err
	}
	plan, err := run.prepare(ctx, policy)
	if err != nil {
		return err
	}
	run.plan = plan
	if err := policy.Capture(ctx, run.context()); err != nil {
		return err
	}
	completed = true
	return nil
}

func recordStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "passed"
}

func beginRecordTransaction(request RecordRequest) (transaction *recordTransaction, err error) {
	sessionDir, err := canonicalSessionDir(request.SessionDir)
	if err != nil {
		return nil, err
	}
	allocation, err := lockFile(filepath.Join(sessionDir, "locks", "record-start.lock"), true, true)
	if err != nil {
		return nil, err
	}
	defer allocation.Close()

	lock, exclusive, err := beginActionLock(sessionDir)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = lock.Close()
		}
	}()

	session, err := OpenSession(sessionDir)
	if err != nil {
		return nil, err
	}
	origin, err := recordOrigin(sessionDir, session, request.Connection)
	if err != nil {
		return nil, err
	}
	if origin == nil {
		if err := checkOfficialIdle(sessionDir); err != nil {
			return nil, err
		}
	}
	plan, err := LoadPlan(session)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(ConnectionIDs(session.Source, plan), request.Connection) {
		return nil, fmt.Errorf("undeclared record connection %s", request.Connection)
	}
	index, err := readIndex(IndexPath(sessionDir))
	if err != nil {
		return nil, err
	}

	var action *ActionRun
	allocated := false
	defer func() {
		if ok || !allocated || action == nil {
			return
		}
		aborted := &recordTransaction{
			session:      action.Session,
			plan:         action.Plan,
			execution:    action.Execution,
			connection:   request.Connection,
			sessionDir:   sessionDir,
			dir:          action.Dir,
			overlayRoot:  action.OverlayRoot,
			artifactRoot: action.ArtifactRoot,
		}
		err = errors.Join(err, aborted.fail())
	}()
	if pending := pendingExecution(index); pending != nil {
		if pending.Command != "record" || connectionStarted(pending, request.Connection) {
			return nil, ErrBusy
		}
		dir := ExecutionDir(sessionDir, pending.ID)
		var execution Execution
		if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
			return nil, err
		}
		if err := execution.Validate(*session); err != nil {
			return nil, err
		}
		if hash, err := tool.FileDigest(filepath.Join(dir, "execution.json")); err != nil || hash != pending.DescriptorSHA256 {
			return nil, errors.New("record descriptor changed")
		}
		if !reflect.DeepEqual(execution.Artifact.OfficialOrigin, origin) || !slices.Equal(execution.Args, request.Args) || execution.Limits.TimeoutNS != int64(request.Timeout) {
			return nil, errors.New("record connection conflicts with execution target or timeout")
		}
		execution.RuntimeEnv, err = executionRuntimeEnvironment(session, "record", filepath.Dir(execution.Home))
		if err != nil {
			return nil, err
		}
		action = &ActionRun{Session: session, Plan: plan, Execution: execution, Dir: dir, OverlayRoot: filepath.Dir(execution.Home), ArtifactRoot: execution.ArtifactRoot, lock: lock}
	} else {
		if !exclusive {
			return nil, ErrBusy
		}
		action, err = createActionLocked(sessionDir, "record", request.Args, request.Timeout, nil, nil, lock, origin)
		if err != nil {
			return nil, err
		}
		allocated = true
	}

	targetPath := filepath.Join(action.OverlayRoot, "record-target.private")
	if existing, err := os.ReadFile(targetPath); err == nil {
		if string(existing) != request.Target {
			return nil, errors.New("record connection targets differ")
		}
	} else if os.IsNotExist(err) {
		if err := os.WriteFile(targetPath, []byte(request.Target), 0o600); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	action.Connection = request.Connection
	var claimPath, originReceiptPath string
	claimed := false
	defer func() {
		if ok || allocated {
			return
		}
		if claimed {
			err = errors.Join(err, removeFileIfExists(claimPath))
		}
		if originReceiptPath != "" {
			err = errors.Join(err, removeFileIfExists(originReceiptPath))
		}
	}()
	if origin != nil {
		path := filepath.Join(action.Dir, "receipts", request.Connection+".origin.json")
		proof := tool.Object{"schemaVersion": 1, "origin": origin, "connectionId": request.Connection, "executionId": action.Execution.ExecutionID, "protocolPid": os.Getpid(), "supervisorPid": os.Getppid(), "toolSHA256": session.ToolSHA256}
		if err := writeExclusive(path, proof); err != nil {
			return nil, err
		}
		originReceiptPath = path
	}
	claimPath, err = connectionClaimPath(action.Execution, request.Connection)
	if err != nil {
		return nil, err
	}
	if err := createConnectionClaim(claimPath); err != nil {
		return nil, fmt.Errorf("%s connection %s already started: %w", action.Execution.Command, request.Connection, err)
	}
	claimed = true
	if err := markConnection(sessionDir, action.Execution.ExecutionID, request.Connection, "running", "open"); err != nil {
		return nil, err
	}
	if exclusive {
		if err := lock.Downgrade(); err != nil {
			return nil, err
		}
	}
	ok = true
	return &recordTransaction{
		session:      session,
		plan:         plan,
		execution:    action.Execution,
		connection:   request.Connection,
		sessionDir:   sessionDir,
		dir:          action.Dir,
		overlayRoot:  action.OverlayRoot,
		artifactRoot: action.ArtifactRoot,
		lock:         lock,
	}, nil
}

func removeFileIfExists(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func cloneRecordObject(value tool.Object) tool.Object {
	if value == nil {
		return nil
	}
	clone := make(tool.Object, len(value))
	for key, item := range value {
		clone[key] = cloneRecordValue(item)
	}
	return clone
}

func cloneRecordValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneRecordObject(value)
	case []any:
		clone := make([]any, len(value))
		for i, item := range value {
			clone[i] = cloneRecordValue(item)
		}
		return clone
	case map[string]string:
		return maps.Clone(value)
	case []string:
		return slices.Clone(value)
	default:
		return value
	}
}

func cloneRecordExecution(source Execution) Execution {
	execution := source
	execution.Args = slices.Clone(source.Args)
	execution.NativeArgs = slices.Clone(source.NativeArgs)
	execution.HistoryFiles = maps.Clone(source.HistoryFiles)
	execution.Env = maps.Clone(source.Env)
	execution.RuntimeEnv = maps.Clone(source.RuntimeEnv)
	execution.Connections = make([]ExecutionConnection, len(source.Connections))
	for i, connection := range source.Connections {
		connection.Args = slices.Clone(connection.Args)
		execution.Connections[i] = connection
	}
	if source.Admission != nil {
		admission := *source.Admission
		execution.Admission = &admission
	}
	execution.Artifact = cloneRecordMaterial(source.Artifact)
	return execution
}

func cloneRecordMaterial(source corpus.Material) corpus.Material {
	material := source
	material.Files = maps.Clone(source.Files)
	material.Connections = slices.Clone(source.Connections)
	if source.OfficialOrigin != nil {
		origin := *source.OfficialOrigin
		origin.Connections = slices.Clone(source.OfficialOrigin.Connections)
		material.OfficialOrigin = &origin
	}
	return material
}
