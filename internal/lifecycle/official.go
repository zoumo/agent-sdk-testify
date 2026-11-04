package lifecycle

import (
	"context"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// OfficialRun owns orchestration activity, never a protocol execution or overlay.
// The action lock is held only while registering, so SDK children can start.
type OfficialRun struct {
	ID, Dir, SessionDir string
	Session             *Session
	lock                *Lock
	mu                  sync.Mutex
	processes           map[string]tool.Object
}

func BeginOfficial(sessionDir, mode string) (*OfficialRun, error) {
	sessionDir, err := canonicalSessionDir(sessionDir)
	if err != nil {
		return nil, err
	}
	if err := checkOpen(sessionDir); err != nil {
		return nil, err
	}
	activity, err := lockFile(filepath.Join(sessionDir, "locks", "official.lock"), true, false)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = activity.Close()
		}
	}()
	lock, err := actionLock(sessionDir, true)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := checkOfficialIdle(sessionDir); err != nil {
		return nil, err
	}
	session, err := OpenActionSession(sessionDir)
	if err != nil {
		return nil, err
	}
	index, err := readIndex(IndexPath(sessionDir))
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
	parent := "official-records"
	if mode == "verify" {
		parent = "verifications"
	} else if mode != "record" {
		return nil, errors.New("invalid official mode")
	}
	dir := filepath.Join(sessionDir, parent, id)
	if err := os.MkdirAll(filepath.Join(dir, "driver"), 0o700); err != nil {
		return nil, err
	}
	var records []string
	registry := filepath.Join(sessionDir, parent, "index.json")
	if err := tool.ReadJSON(registry, &records); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	records = append(records, id)
	if err := writeAtomic(registry, records); err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(sessionDir, "official-active.json"), tool.Object{
		"schemaVersion": 1, "sessionId": session.SessionID, "id": id, "mode": mode, "pid": os.Getpid(), "status": "running", "dir": dir, "tracked": true, "processes": map[string]tool.Object{},
	}); err != nil {
		return nil, err
	}
	ok = true
	return &OfficialRun{ID: id, Dir: dir, SessionDir: sessionDir, Session: session, lock: activity, processes: map[string]tool.Object{}}, nil
}

func checkOfficialIdle(sessionDir string) error {
	var active tool.Object
	err := tool.ReadJSON(filepath.Join(sessionDir, "official-active.json"), &active)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// A crashed owner leaves a durable reservation. Never infer that its
	// descendants have exited merely because the advisory lock was released.
	return errors.New("session has active or interrupted official orchestration")
}

// Complete is called only after owned processes and final evidence are settled.
func (r *OfficialRun) Complete(result tool.Object) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	result["processes"] = r.processes
	var processErr error
	for _, process := range r.processes {
		processErr = errors.Join(processErr, stoppedLaunch(process))
	}
	if processErr != nil {
		result["status"], result["cleanupComplete"] = "failed", false
		result["processError"] = processErr.Error()
	}
	if err := writeAtomic(filepath.Join(r.Dir, "result.json"), result); err != nil {
		return errors.Join(err, r.lock.Close())
	}
	if result["cleanupComplete"] != true {
		return errors.Join(errors.New("official processes are not confirmed stopped; activity retained"), processErr, r.lock.Close())
	}
	err := os.Remove(filepath.Join(r.SessionDir, "official-active.json"))
	return errors.Join(err, r.lock.Close())
}

// RecordEnvironment uses the stable normal record route slots. The first agent
// connection binds these slots to its overlay before protocol I/O starts.
func RecordEnvironment(session *Session) (map[string]string, error) {
	route := session.Routes["record"]
	env := make(map[string]string, len(route.Env))
	maps.Copy(env, route.Env)
	private, err := readPrivateEnvironment(session.ResourceRoot)
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
	r.processes[name] = tool.Object{"state": state, "pid": pid}
	var active tool.Object
	path := filepath.Join(r.SessionDir, "official-active.json")
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

// TrackNativeProbe records preparation's separate native version-probe group.
func TrackNativeProbe(run *ActionRun, state string, pid int) error {
	return writeAtomic(filepath.Join(run.Dir, "receipts", run.Connection+".probe.native-process.json"), tool.Object{"state": state, "pid": pid})
}

// RestoreMockRoute settles the ordinary shared route only after all protocol
// owners release it. Official verification owns no separate execution overlay.
func (r *OfficialRun) RestoreMockRoute() error {
	lock, err := actionLock(r.SessionDir, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	index, err := readIndex(IndexPath(r.SessionDir))
	if err != nil {
		return err
	}
	if pendingExecution(index) != nil {
		return ErrBusy
	}
	return restoreRouteSlots(r.Session, "mock")
}
