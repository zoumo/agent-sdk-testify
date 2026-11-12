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

// CanonicalFixtureDir validates and resolves one prepared-fixture directory.
func CanonicalFixtureDir(fixtureDir string) (string, error) {
	if !filepath.IsAbs(fixtureDir) {
		return "", errors.New("--fixture-dir must be absolute")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(fixtureDir))
	if err != nil {
		return "", err
	}
	dir := filepath.Join(parent, filepath.Base(fixtureDir))
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("fixture directory is not an unlinked directory")
	}
	return dir, nil
}

func readFixtureUnlocked(fixtureDir string) (*Fixture, error) {
	dir, err := CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, err
	}
	fixture, err := readFixture(dir)
	if err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(dir))
	if err != nil {
		return nil, err
	}
	hash, err := tool.FileDigest(FixturePath(dir))
	if err != nil {
		return nil, err
	}
	if hash != index.FixtureSHA256 || index.FixtureID != fixture.FixtureID {
		return nil, errors.New("fixture descriptor changed after prepare")
	}
	if fixture.ResourceRoot != filepath.Join(dir, "runtime") {
		return nil, errors.New("fixture runtime must be inside fixture/runtime")
	}
	if _, err := readCurrent(dir, fixture.FixtureID); err != nil {
		return nil, err
	}
	return fixture, nil
}

func OpenFixture(fixtureDir string) (*Fixture, error) {
	return readFixtureUnlocked(fixtureDir)
}

func updateIndex(fixtureDir string, mutate func(*Index) error) error {
	lock, err := metadataLock(fixtureDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return err
	}
	oldMaterials := append([]corpus.Material(nil), index.Artifacts...)
	if err := mutate(index); err != nil {
		return err
	}
	if !reflect.DeepEqual(oldMaterials, index.Artifacts) {
		if err := writeMaterialIndex(fixtureDir, index); err != nil {
			return err
		}
	}
	index.LastUpdatedUTC = now()
	return writeAtomic(IndexPath(fixtureDir), index)
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

func findIndexExecution(index *Index, id string) (*IndexExecution, error) {
	seen := make(map[string]bool, len(index.Executions))
	var found *IndexExecution
	for i := range index.Executions {
		execution := &index.Executions[i]
		if execution.ID == "" {
			return nil, errors.New("execution index contains an empty identity")
		}
		if seen[execution.ID] {
			return nil, fmt.Errorf("duplicate execution identity %s in index", execution.ID)
		}
		seen[execution.ID] = true
		if execution.ID == id {
			found = execution
		}
	}
	return found, nil
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

func executionConnections(fixtureDir, executionID, materialID, action string, ids []string, material *corpus.Material, route Route) ([]ExecutionConnection, error) {
	var connections []ExecutionConnection
	for _, id := range ids {
		template, ok := connectionTemplate(route, id)
		if !ok {
			return nil, fmt.Errorf("route does not declare connection %s", id)
		}
		connection := ExecutionConnection{
			ID:         id,
			MaterialID: materialID,
			Transport:  template.Transport,
		}
		dir := PackageExecutionRoot(fixtureDir, materialID, executionID)
		switch action {
		case "record":
			base := filepath.Join(dir, "capture")
			if id == "probe" {
				base = filepath.Join(base, "remote-probe")
			}
			receipt := filepath.Join(base, "raw", "transport.json")
			reference, err := PackageReference(fixtureDir, materialID, receipt)
			if err != nil {
				return nil, err
			}
			connection.Receipt = reference
		case "mock":
			receipt := filepath.Join(dir, "receipts", id+".json")
			reference, err := PackageReference(fixtureDir, materialID, receipt)
			if err != nil {
				return nil, err
			}
			connection.Receipt = reference
			if template.Transport == ConnectionTransportProcess {
				process := filepath.Join(dir, "receipts", id+".process.json")
				processReference, err := PackageReference(fixtureDir, materialID, process)
				if err != nil {
					return nil, err
				}
				connection.ProcessReceipt = processReference
			}
		}
		if material != nil {
			connection.MaterialID = material.ID
			recording := filepath.Join(material.Path, "raw", "raw.jsonl")
			if id == "probe" {
				recording = filepath.Join(material.Path, "remote-probe", "raw", "raw.jsonl")
			}
			recordingHash, err := tool.FileDigest(recording)
			if err != nil {
				return nil, err
			}
			connection.PlanSHA256 = material.PlanSHA256
			connection.ManifestSHA256 = material.ManifestSHA256
			connection.RecordingSHA256 = recordingHash
		}
		connections = append(connections, connection)
	}
	return connections, nil
}

func createExecutionOverlay(fixture *Fixture, action, executionID string, plan tool.Object, material *corpus.Material) (string, map[string]string, error) {
	route, ok := fixture.Routes[action]
	if !ok || !route.Enabled {
		return "", nil, fmt.Errorf("%s is not available for this fixture", action)
	}
	fixtureDir := fixture.dir
	overlayRoot := ExecutionOverlayRoot(fixtureDir, executionID)
	success := false
	slotsTouched := false
	defer func() {
		if !success {
			if slotsTouched {
				_ = restoreRouteSlots(fixture, action)
			}
			_ = os.RemoveAll(overlayRoot)
		}
	}()
	if err := tool.CopyTree(BaselineRoot(fixtureDir), overlayRoot); err != nil {
		return "", nil, err
	}
	if err := prepareExecutionAuthentication(fixture, action, overlayRoot); err != nil {
		return "", nil, err
	}
	if action == "mock" {
		if err := cleanMockConfig(overlayRoot, plan, fixture.AgentType); err != nil {
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
	if action == "record" && fixture.Source.Kind == "recording" {
		if _, err := history.Materialize(fixture.Source.Path, overlayRoot); err != nil {
			return "", nil, err
		}
	}
	slotsTouched = true
	if err := bindRouteSlots(RouteRoot(fixtureDir, action), overlayRoot); err != nil {
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

func newExecution(fixture *Fixture, action, id, materialID, overlayRoot string, commandArgs, nativeArgs []string, timeout time.Duration, progressFD int, ids []string, material *corpus.Material, admission *Admission, plan tool.Object, historyFiles map[string]string) (Execution, error) {
	workspace := filepath.Join(overlayRoot, "workspace")
	limits := fixture.Limits
	if timeout > 0 {
		limits.TimeoutNS = int64(timeout)
	}
	route := fixture.Routes[action]
	provenance, err := executionProvenance(fixture, action, material)
	if err != nil {
		return Execution{}, err
	}
	connections, err := executionConnections(fixture.dir, id, materialID, action, ids, material, route)
	if err != nil {
		return Execution{}, err
	}
	source := fixture.Source
	sourceReference, err := FixtureReference(fixture.dir, source.Path)
	if err != nil {
		return Execution{}, err
	}
	source.Path = sourceReference
	execution := Execution{
		SchemaVersion: SchemaVersion,
		ExecutionID:   id,
		FixtureID:     fixture.FixtureID,
		Command:       action,
		Args:          append([]string(nil), commandArgs...),
		NativeArgs:    append([]string(nil), nativeArgs...),
		Source:        source,
		Executor:      route.Executor,
		Target:        route.Target,
		Provenance:    provenance,
		ProtocolCwd:   executionProtocolCwd(action, plan, workspace),
		HistoryFiles:  historyFiles,
		Env:           baseEnvironment(fixture.AgentType, overlayRoot),
		Connections:   connections,
		Admission:     admission,
		Limits:        limits,
		StartedAt:     now(),
	}
	if action == "record" {
		execution.Artifact = corpus.Material{
			ID:          materialID,
			Kind:        "capture",
			Action:      "record",
			Provenance:  provenance,
			Connections: append([]string(nil), ids...),
			CreatedAt:   now(),
		}
	} else if material != nil {
		execution.Artifact = *material
	}
	if action == "mock" {
		execution.ProgressFD = progressFD
	}
	execution.dir = fixture.dir
	if err := execution.Resolve(fixture, fixture.dir); err != nil {
		return Execution{}, err
	}
	return execution, nil
}

func createTransactionLocked(fixtureDir, action string, commandArgs, nativeArgs []string, timeout time.Duration, progressFD int, material *corpus.Material, admission *Admission, origin *corpus.OfficialOrigin) (*transactionContext, error) {
	fixtureDir, err := CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, err
	}
	fixture, err := readFixtureUnlocked(fixtureDir)
	if err != nil {
		return nil, err
	}
	if action == "record" && origin == nil {
		if err := checkOfficialIdle(fixtureDir); err != nil {
			return nil, err
		}
	}
	route, ok := fixture.Routes[action]
	if !ok || !route.Enabled {
		if action == "record" {
			return nil, errors.New("record source has no executable route: plan, driver binding or protocol cwd is unavailable")
		}
		return nil, fmt.Errorf("%s is not available for this fixture", action)
	}
	if action != "record" && material == nil {
		return nil, fmt.Errorf("%s requires an input material", action)
	}
	success := false
	routeBound := false
	defer func() {
		if !success && routeBound {
			_ = restoreRouteSlots(fixture, action)
		}
	}()
	if action == "record" && origin == nil {
		if err := checkOfficialIdle(fixtureDir); err != nil {
			return nil, err
		}
	}
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return nil, err
	}
	if pending := pendingExecution(index); pending != nil {
		return nil, fmt.Errorf("fixture has unfinished execution %s", pending.ID)
	}
	var plan tool.Object
	if action == "record" {
		plan, err = LoadPlan(fixture)
		if err != nil {
			return nil, err
		}
		if fixture.Source.Kind == "recording" {
			found := false
			for _, source := range index.Artifacts {
				if source.Path != fixture.Source.Path {
					continue
				}
				if _, err := inspectMaterial(fixture, source); err != nil {
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
		current, err := LatestArtifact(fixtureDir, fixture)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(current, material) {
			return nil, errors.New("current recording changed before execution")
		}
		inspection, err := inspectMaterial(fixture, *current)
		if err != nil {
			return nil, err
		}
		plan = inspection.Plan
		for _, id := range current.Connections {
			if _, ok := connectionTemplate(route, id); !ok {
				return nil, fmt.Errorf("route does not declare connection %s", id)
			}
		}
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	materialID := ""
	if material != nil {
		materialID = material.ID
	} else {
		materialID, err = randomID()
		if err != nil {
			return nil, err
		}
	}
	dir := PackageExecutionRoot(fixtureDir, materialID, id)
	if err := os.MkdirAll(filepath.Join(dir, "receipts"), 0o700); err != nil {
		return nil, err
	}
	artifactRoot := filepath.Join(dir, action)
	if action == "record" {
		artifactRoot = filepath.Join(dir, "capture")
	}
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		return nil, err
	}
	overlayRoot, historyFiles, err := createExecutionOverlay(fixture, action, id, plan, material)
	if err != nil {
		return nil, err
	}
	routeBound = true
	ids := ConnectionIDs(fixture.Source, plan)
	if material != nil {
		if len(material.Connections) > 0 {
			ids = append([]string(nil), material.Connections...)
		}
	}
	execution, err := newExecution(fixture, action, id, materialID, overlayRoot, commandArgs, nativeArgs, timeout, progressFD, ids, material, admission, plan, historyFiles)
	if err != nil {
		return nil, err
	}
	if origin != nil {
		execution.Provenance = "official"
		execution.Artifact.Provenance = "official"
		execution.Artifact.OfficialOrigin = origin
	}
	if err := execution.Validate(*fixture); err != nil {
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
	if err := updateIndex(fixtureDir, func(value *Index) error {
		for _, existing := range value.Executions {
			if existing.ID == id {
				return errors.New("duplicate execution identity")
			}
		}
		value.Executions = append(value.Executions, IndexExecution{
			ID:               id,
			MaterialID:       materialID,
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
	return newTransactionContext(fixture, plan, execution, fixtureDir), nil
}

func beginActionLock(fixtureDir string) (*Lock, bool, error) {
	lock, err := actionLock(fixtureDir, true)
	if err == nil {
		if err := recoverPublication(fixtureDir); err != nil {
			return nil, false, errors.Join(err, lock.Close())
		}
		return lock, true, nil
	}
	if !errors.Is(err, ErrBusy) {
		return nil, false, err
	}
	lock, err = actionLock(fixtureDir, false)
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
	path := filepath.Join(filepath.Dir(execution.Home), "claims", id+".started")
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

func markConnection(fixtureDir, executionID, connection, status, executionStatus string) error {
	return updateIndex(fixtureDir, func(index *Index) error {
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
