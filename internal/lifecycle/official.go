package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// OfficialRun owns orchestration activity, never a protocol execution or overlay.
// The action lock is held only while registering, so SDK children can start.
type OfficialRun struct {
	ID, Dir, FixtureDir string
	Fixture             *Fixture
	lock                *Lock
	mu                  sync.Mutex
	processes           map[string]tool.Object
}

func BeginOfficial(fixtureDir, mode string) (*OfficialRun, error) {
	fixtureDir, err := CanonicalFixtureDir(fixtureDir)
	if err != nil {
		return nil, err
	}
	activity, err := lockFile(filepath.Join(fixtureDir, "locks", "official.lock"), true, false)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = activity.Close()
		}
	}()
	lock, err := actionLock(fixtureDir, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if journal, err := readOfficialJournal(fixtureDir); err == nil {
		if err := recoverOfficialSettlement(fixtureDir, journal); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	fixture, err := OpenFixture(fixtureDir)
	if err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(fixtureDir))
	if err != nil {
		return nil, err
	}
	if pendingExecution(index) != nil {
		return nil, ErrBusy
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	if mode != "record" && mode != "verify" {
		return nil, errors.New("invalid official mode")
	}
	dir := filepath.Join(ResourceRoot(fixtureDir), "official", id, "staging")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	journal := tool.Object{"schemaVersion": SchemaVersion, "fixtureId": fixture.FixtureID, "id": id, "mode": mode, "pid": os.Getpid(), "status": "running", "staging": filepath.ToSlash(filepath.Join("runtime", "official", id, "staging")), "processes": map[string]tool.Object{}}
	if mode == "verify" {
		material, err := LatestArtifact(fixtureDir, fixture)
		if err != nil {
			return nil, err
		}
		hash := material.LegacyInventorySHA256
		if hash == "" {
			hash, err = tool.FileDigest(filepath.Join(material.Path, "material.json"))
			if err != nil {
				return nil, err
			}
		}
		journal["materialId"], journal["materialSHA256"] = material.ID, hash
		journal["target"] = filepath.ToSlash(filepath.Join("packages", material.ID, "verify", id))
	}
	if err := writeAtomic(filepath.Join(fixtureDir, "official.json"), journal); err != nil {
		return nil, err
	}
	ok = true
	return &OfficialRun{ID: id, Dir: dir, FixtureDir: fixtureDir, Fixture: fixture, lock: activity, processes: map[string]tool.Object{}}, nil
}

func checkOfficialIdle(fixtureDir string) error {
	var active tool.Object
	err := tool.ReadJSON(filepath.Join(fixtureDir, "official.json"), &active)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := persistedSchema(filepath.Join(fixtureDir, "official.json"), "official activity"); err != nil {
		return err
	}
	// A crashed owner leaves a durable reservation. Never infer that its
	// descendants have exited merely because the advisory lock was released.
	if active["status"] == "settling" {
		guard, err := lockFile(filepath.Join(fixtureDir, "locks", "official.lock"), true, false)
		if err != nil {
			return err
		}
		defer guard.Close()
		return recoverOfficialSettlement(fixtureDir, active)
	}
	return errors.New("fixture has active or interrupted official orchestration")
}

// Complete is called only after owned processes and final evidence are settled.
func (r *OfficialRun) Complete(result tool.Object) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.lock.Close()
	lock, err := actionLock(r.FixtureDir, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	journal, err := readOfficialJournal(r.FixtureDir)
	if err != nil {
		return err
	}
	if journal["id"] != r.ID {
		return errors.New("official journal identity changed")
	}
	if result["schemaVersion"] != 3 || result["id"] != r.ID || result["mode"] != journal["mode"] || result["fixtureId"] != r.Fixture.FixtureID {
		return errors.New("official result identity differs; journal retained")
	}
	for _, process := range r.processes {
		if process["state"] != "stopped" && process["state"] != "not-started" {
			return errors.New("official writers are not confirmed stopped; journal retained")
		}
	}
	if result["cleanupComplete"] != true {
		return errors.New("official cleanup incomplete; journal retained")
	}
	index, err := readIndex(IndexPath(r.FixtureDir))
	if err != nil {
		return err
	}
	if pendingExecution(index) != nil {
		return errors.New("official execution is not settled; journal retained")
	}
	materialID := tool.Str(journal["materialId"])
	executionID := tool.Str(journal["executionId"])
	if result["mode"] == "record" {
		if err := errors.Join(restoreRouteSlots(r.Fixture, "record"), RefreshRoutes(r.Fixture)); err != nil {
			return err
		}
		if materialID == "" {
			// Every authorized claim durably binds this journal before allocation.
			// Absence is meaningful only after all registered launchers have stopped.
			journal["target"] = filepath.ToSlash(filepath.Join("failures", "official-record", r.ID))
		}
	}
	if executionID != "" {
		result["executionId"] = executionID
		var aggregate tool.Object
		if err := tool.ReadJSON(filepath.Join(PackageExecutionRoot(r.FixtureDir, materialID, executionID), "result.json"), &aggregate); err == nil {
			result["publicationCommitted"] = aggregate["publicationCommitted"]
			for _, key := range []string{"material", "materialSHA256", "manifestSHA256", "sourcePlanSHA256", "planSHA256"} {
				if value, ok := aggregate[key]; ok {
					result[key] = value
				}
			}
		} else {
			return errors.Join(errors.New("official execution has not sealed; journal retained"), err)
		}
	}
	if materialID != "" {
		result["materialId"] = materialID
	}
	targetRef := tool.Str(journal["target"])
	if targetRef == "" {
		return errors.New("official association is unresolved; journal retained")
	}
	target, err := ResolveFixtureReference(r.FixtureDir, targetRef)
	if err != nil {
		return err
	}
	prefix := targetRef
	if materialID != "" {
		prefix, err = PackageReference(r.FixtureDir, materialID, target)
		if err != nil {
			return err
		}
	}
	envelope := "result.json"
	if result["mode"] == "verify" {
		envelope = "verification.json"
	}
	files, err := history.HashTree(r.Dir)
	if err != nil {
		return err
	}
	delete(files, envelope)
	inventory := tool.Object{}
	for name, hash := range files {
		inventory[filepath.ToSlash(filepath.Join(prefix, name))] = hash
	}
	result["files"] = inventory
	if err := writeExclusive(filepath.Join(r.Dir, envelope), result); err != nil {
		return err
	}
	hash, err := tool.FileDigest(filepath.Join(r.Dir, envelope))
	if err != nil {
		return err
	}
	journal["status"], journal["envelope"], journal["sha256"] = "settling", envelope, hash
	if err := writeAtomic(filepath.Join(r.FixtureDir, "official.json"), journal); err != nil {
		return err
	}
	if err := recoverOfficialSettlement(r.FixtureDir, journal); err != nil {
		return err
	}
	r.Dir = target
	return nil
}

func readOfficialJournal(root string) (tool.Object, error) {
	if err := persistedSchema(filepath.Join(root, "official.json"), "official journal"); err != nil {
		return nil, err
	}
	var journal tool.Object
	err := tool.ReadJSON(filepath.Join(root, "official.json"), &journal)
	return journal, err
}

// bindOfficialExecutionLocked records the reserved identity before any execution
// tree or process can exist. It runs under the ordinary fixture action lock.
func bindOfficialExecutionLocked(root, runID, materialID, executionID string) error {
	lock, err := metadataLock(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	journal, err := readOfficialJournal(root)
	if err != nil {
		return err
	}
	if journal["id"] != runID || journal["mode"] != "record" || journal["status"] != "running" {
		return errors.New("official claim journal differs")
	}
	if existing := tool.Str(journal["executionId"]); existing != "" && existing != executionID {
		return errors.New("official run already owns an execution")
	}
	journal["materialId"], journal["executionId"] = materialID, executionID
	journal["target"] = filepath.ToSlash(filepath.Join("packages", materialID, "record", runID))
	return writeAtomic(filepath.Join(root, "official.json"), journal)
}

// Recovery only finishes an already sealed rename. Running/unknown work is
// retained, even if the parent's PID has disappeared.
func recoverOfficialSettlement(root string, journal tool.Object) error {
	if journal["status"] != "settling" {
		return errors.New("official orchestration is interrupted before sealing")
	}
	for _, value := range tool.Obj(journal["processes"]) {
		state := tool.Obj(value)["state"]
		if state != "stopped" && state != "not-started" {
			return errors.New("official writer state is unresolved")
		}
	}
	staging, err := ResolveFixtureReference(root, tool.Str(journal["staging"]))
	if err != nil {
		return err
	}
	target, err := ResolveFixtureReference(root, tool.Str(journal["target"]))
	if err != nil {
		return err
	}
	id := tool.Str(journal["id"])
	mode := tool.Str(journal["mode"])
	if mode != "record" && mode != "verify" {
		return errors.New("invalid official settlement mode")
	}
	if validateExecutionID(id) != nil || staging != filepath.Join(ResourceRoot(root), "official", id, "staging") {
		return errors.New("invalid official staging identity")
	}
	materialID := tool.Str(journal["materialId"])
	expected := filepath.Join(root, "failures", "official-record", id)
	kind := "record"
	if mode == "verify" {
		if materialID == "" {
			return errors.New("verification omits pinned material")
		}
		kind = "verify"
	}
	if materialID != "" {
		if validateExecutionID(materialID) != nil {
			return errors.New("invalid official material identity")
		}
		expected = filepath.Join(PackageRoot(root, materialID), kind, id)
	}
	if target != expected {
		return errors.New("official settlement target differs")
	}
	_, se := os.Lstat(staging)
	_, te := os.Lstat(target)
	if se == nil && te == nil {
		return errors.New("official staging and target both exist")
	}
	source := staging
	if os.IsNotExist(se) && te == nil {
		source = target
	} else if se != nil || !os.IsNotExist(te) {
		return errors.Join(se, te)
	}
	envelope := tool.Str(journal["envelope"])
	expectedEnvelope := "result.json"
	if kind == "verify" {
		expectedEnvelope = "verification.json"
	}
	if envelope != expectedEnvelope {
		return errors.New("invalid official envelope")
	}
	hash, err := tool.FileDigest(filepath.Join(source, envelope))
	if err != nil || hash != journal["sha256"] {
		return errors.New("official settlement envelope hash differs")
	}
	var result tool.Object
	if err := tool.ReadJSON(filepath.Join(source, envelope), &result); err != nil {
		return err
	}
	if result["schemaVersion"] != float64(3) || result["id"] != id || result["mode"] != mode || result["fixtureId"] != journal["fixtureId"] || result["cleanupComplete"] != true || tool.Str(result["materialId"]) != materialID || tool.Str(result["executionId"]) != tool.Str(journal["executionId"]) {
		return errors.New("official sealed identity differs")
	}
	files, err := history.HashTree(source)
	if err != nil {
		return err
	}
	delete(files, envelope)
	inventory := tool.Obj(result["files"])
	prefix := tool.Str(journal["target"])
	if materialID != "" {
		prefix = filepath.ToSlash(filepath.Join(kind, id))
	}
	if len(files) != len(inventory) {
		return errors.New("official settlement inventory differs")
	}
	for name, digest := range files {
		if inventory[filepath.ToSlash(filepath.Join(prefix, name))] != digest {
			return fmt.Errorf("official settlement hash differs: %s", name)
		}
	}
	if source == staging {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Rename(staging, target); err != nil {
			return err
		}
	}
	return os.Remove(filepath.Join(root, "official.json"))
}

// RecordEnvironment uses the stable normal record route slots. The first agent
// connection binds these slots to its overlay before protocol I/O starts.
func RecordEnvironment(fixture *Fixture) (map[string]string, error) {
	route := fixture.Routes["record"]
	env := make(map[string]string, len(route.Env))
	maps.Copy(env, route.Env)
	private, err := readPrivateEnvironment(fixture.ResourceRoot)
	if err != nil {
		return nil, err
	}
	for _, name := range route.RuntimeEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		} else if value, ok := private[name]; ok {
			env[name] = value
		}
	}
	return env, nil
}

// Launch intent is durable before Start. A crash between Start and registration
// leaves an unknown process, so recovery must remain busy instead of guessing.
func (r *OfficialRun) TrackProcess(name, state string, pid int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, err := metadataLock(r.FixtureDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	r.processes[name] = tool.Object{"state": state, "pid": pid}
	var active tool.Object
	path := filepath.Join(r.FixtureDir, "official.json")
	if err := persistedSchema(path, "official activity"); err != nil {
		return err
	}
	if err := tool.ReadJSON(path, &active); err != nil {
		return err
	}
	if active["id"] != r.ID {
		return errors.New("official activity identity changed")
	}
	active["processes"] = r.processes
	return writeAtomic(path, active)
}

func (r *OfficialRun) WaitDriver(ctx context.Context, cmd *exec.Cmd) error {
	if err := r.TrackProcess("driver", "launching", 0); err != nil {
		return err
	}
	err := tool.WaitDriver(ctx, cmd, func(pid int) error { return r.TrackProcess("driver", "started", pid) })
	if cmd.Process == nil {
		err = errors.Join(err, r.TrackProcess("driver", "not-started", 0))
	}
	return err
}

// RestoreMockRoute settles the ordinary shared route only after all protocol
// owners release it. Official verification owns no separate execution overlay.
func (r *OfficialRun) RestoreMockRoute() error {
	return r.restoreRoute("mock")
}

func (r *OfficialRun) restoreRoute(action string) error {
	lock, err := actionLock(r.FixtureDir, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	index, err := readIndex(IndexPath(r.FixtureDir))
	if err != nil {
		return err
	}
	if pendingExecution(index) != nil {
		return ErrBusy
	}
	return errors.Join(restoreRouteSlots(r.Fixture, action), RefreshRoutes(r.Fixture))
}
