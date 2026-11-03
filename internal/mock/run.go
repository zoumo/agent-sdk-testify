package mock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/zoumo/agent-sdk-testify/internal/exitcode"
	"github.com/zoumo/agent-sdk-testify/internal/version"
	"github.com/zoumo/agent-sdk-testify/protocol"
)

// The descriptor is the installed-process contract, not a workflow runner.
// Consumers apply Env before starting their SDK host and own business policy.
type mockRun struct {
	Builtin               *builtinSelection `json:"builtin,omitempty"`
	SchemaVersion         int               `json:"schemaVersion"`
	RunID                 string            `json:"runId"`
	Tool                  command           `json:"tool"`
	ToolVersion           string            `json:"toolVersion"`
	ToolSHA256            string            `json:"toolSHA256"`
	Recording             string            `json:"recording"`
	Provider              string            `json:"provider"`
	CaseID                string            `json:"caseId,omitempty"`
	ResourceRoot          string            `json:"resourceRoot"`
	Workspace             string            `json:"workspace"`
	ProtocolCWD           string            `json:"protocolCwd"`
	Env                   map[string]string `json:"env"`
	Contract              string            `json:"contract"`
	ContractSHA256        string            `json:"contractSHA256"`
	Materialization       string            `json:"materialization"`
	MaterializationSHA256 string            `json:"materializationSHA256"`
	Connections           []mockConnection  `json:"connections"`
	Finish                command           `json:"finish"`
}
type mockConnection struct {
	ID string `json:"id"`
	command
	Launcher        string `json:"launcher"`
	Recording       string `json:"recording"`
	PlanSHA256      string `json:"planSHA256"`
	RecordingSHA256 string `json:"recordingSHA256"`
	ManifestSHA256  string `json:"manifestSHA256"`
	Receipt         string `json:"receipt"`
	ProcessReceipt  string `json:"processReceipt"`
}

func mockPreparedMode(ctx context.Context, o Options, selection *builtinSelection) error {
	if o.Prepare {
		if o.Run != "" || o.Finish != "" || len(o.NativeArgs) > 0 {
			return bad(errors.New("mock modes are mutually exclusive"))
		}
		return prepareMockSelected(ctx, o.Recording, o.Output, o.Provider, selection)
	}
	if o.Recording != "" || o.Output != "" || o.Provider != "" {
		return bad(errors.New("recording/output/provider belong to prepare"))
	}
	if o.Finish != "" {
		path := o.Finish
		if path == "true" {
			path = o.Run
		} else if o.Run != "" {
			return bad(errors.New("specify one run descriptor"))
		}
		if len(o.NativeArgs) > 0 {
			return bad(errors.New("finish takes no native arguments"))
		}
		return finishMock(path)
	}
	if o.Connection == "" {
		o.Connection = "main"
	}
	return runPreparedMock(ctx, o.Run, o.Connection, o.NativeArgs)
}

func prepareMockSelected(ctx context.Context, source, output, provider string, selection *builtinSelection) (err error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(output) {
		return exitcode.Fail(exitcode.Configuration, errors.New("prepare requires --recording ABS and --output ABS"))
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(output))
	if e != nil {
		return e
	}
	output = filepath.Join(parent, filepath.Base(output))
	if rel, relErr := filepath.Rel(source, output); relErr != nil || filepath.IsLocal(rel) {
		return errors.New("output must be outside recording")
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return err
	}
	var r mockRun
	defer func() {
		if ctx.Err() != nil {
			err = errors.Join(err, exitcode.Fail(exitcode.Canceled, ctx.Err()))
		}
		if err != nil {
			cleanup := error(nil)
			if r.ResourceRoot != "" {
				cleanup = os.RemoveAll(r.ResourceRoot)
			}
			diagnostic := object{"schemaVersion": 1, "status": "failed", "error": err.Error(), "cleanupComplete": cleanup == nil, "resourceRoot": r.ResourceRoot}
			if cleanup != nil {
				diagnostic["cleanupError"] = cleanup.Error()
			}
			err = errors.Join(err, cleanup, atomicMockJSON(filepath.Join(output, "prepare.json"), diagnostic))
		}
	}()
	manifest, plan, _, e := loadMockSource(source, provider)
	if e != nil {
		return e
	}
	r.Provider = planAgentType(plan)
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	r.SchemaVersion = 1
	r.RunID = hex.EncodeToString(id[:])
	r.Recording = source
	r.CaseID = str(plan["caseId"])
	self, e := os.Executable()
	if e != nil {
		return e
	}
	r.Tool = command{Command: self}
	r.ToolVersion = version.Version
	r.ToolSHA256, err = fileDigest(self)
	if err != nil {
		return err
	}
	// TMPDIR is caller-controlled; never allocate mutable resources in evidence.
	tempBase, e := filepath.EvalSymlinks(os.TempDir())
	if e != nil {
		return e
	}
	if rel, e := filepath.Rel(source, tempBase); e != nil || filepath.IsLocal(rel) {
		return errors.New("temporary directory must be outside recording")
	}
	r.ResourceRoot, err = os.MkdirTemp(tempBase, "agent-sdk-testify-mock-")
	if err != nil {
		return err
	}
	r.ResourceRoot, err = filepath.EvalSymlinks(r.ResourceRoot)
	if err != nil {
		return err
	}
	if selection != nil {
		target := filepath.Join(r.ResourceRoot, "builtin")
		if err := os.Rename(source, target); err != nil {
			return err
		}
		source, r.Recording, r.Builtin = target, target, selection
		if err := checkBuiltinMaterial(source, selection.Material); err != nil {
			return err
		}
	}
	r.Workspace = filepath.Join(r.ResourceRoot, "project")
	for _, dir := range []string{"project", "home", "config", "tmp"} {
		if err := os.Mkdir(filepath.Join(r.ResourceRoot, dir), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(r.ResourceRoot, ".owner"), []byte(r.RunID), 0o600); err != nil {
		return err
	}
	if err := writeFiles(r.Workspace, obj(obj(plan["scenario"])["files"])); err != nil {
		return err
	}
	if err := copySaved(source, r.ResourceRoot); err != nil {
		return err
	}
	// Offline mock does not copy auth, user settings, hooks or discovery inputs.
	configName, configBytes := "config.toml", []byte{}
	if model := str(plan["model"]); model != "" {
		configBytes = []byte("model = " + strconv.Quote(model) + "\n")
	}
	if r.Provider == "claude" {
		configName = "settings.json"
		configBytes, err = json.Marshal(object{"model": plan["model"]})
		if err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(r.ResourceRoot, "config", configName), configBytes, 0o600); err != nil {
		return err
	}
	r.ProtocolCWD = str(plan["project"])
	if r.Provider == "claude" {
		if err := materializeClaude(source, r.ResourceRoot, r.Workspace, plan); err != nil {
			return err
		}
		r.ProtocolCWD = r.Workspace
	} else {
		if err := writeJSON(filepath.Join(r.ResourceRoot, "materialization.json"), object{"version": 1, "transformations": []object{}}); err != nil {
			return err
		}
	}
	var materialization object
	if err := readJSON(filepath.Join(r.ResourceRoot, "materialization.json"), &materialization); err != nil {
		return err
	}
	var adoptedHashes object
	if _, e := os.Stat(filepath.Join(source, "adoption.json")); e == nil {
		review, reviewErr := verifyAdoptedFiles(source)
		if reviewErr != nil {
			return reviewErr
		}
		adoptedHashes = obj(review["captureFiles"])
	} else if !os.IsNotExist(e) {
		return e
	}
	var copies []object
	for _, directory := range []string{"config/projects", "config/sessions", "history-seed"} {
		base := filepath.Join(source, directory)
		if _, e := os.Stat(base); os.IsNotExist(e) {
			continue
		}
		if err := filepath.WalkDir(base, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			name, e := filepath.Rel(source, p)
			if e != nil {
				return e
			}
			hash, e := fileDigest(p)
			if e != nil {
				return e
			}
			expected := str(obj(manifest["savedFiles"])[name])
			if expected == "" {
				expected = str(obj(obj(obj(plan["historySeed"])["files"])[name])["sha256"])
			}
			if expected == "" {
				expected = str(adoptedHashes[name])
			}
			if hash != expected {
				return fmt.Errorf("unbound or corrupt history: %s", name)
			}
			copies = append(copies, object{"source": name, "sourceSHA256": hash, "derived": name, "derivedSHA256": hash})
			return nil
		}); err != nil {
			return err
		}
	}
	materialization["copies"] = copies
	if err := writeJSON(filepath.Join(r.ResourceRoot, "materialization.json"), materialization); err != nil {
		return err
	}
	r.Materialization = filepath.Join(output, "materialization.json")
	if err := copyFile(filepath.Join(r.ResourceRoot, "materialization.json"), r.Materialization); err != nil {
		return err
	}
	r.MaterializationSHA256, err = fileDigest(r.Materialization)
	if err != nil {
		return err
	}
	r.Env = map[string]string{"HOME": filepath.Join(r.ResourceRoot, "home"), "TMPDIR": filepath.Join(r.ResourceRoot, "tmp"), "TMP": filepath.Join(r.ResourceRoot, "tmp"), "TEMP": filepath.Join(r.ResourceRoot, "tmp"), "XDG_CONFIG_HOME": filepath.Join(r.ResourceRoot, "home", ".config"), "XDG_CACHE_HOME": filepath.Join(r.ResourceRoot, "home", ".cache"), "XDG_DATA_HOME": filepath.Join(r.ResourceRoot, "home", ".local", "share")}
	if r.Provider == "codex" {
		r.Env["CODEX_HOME"] = filepath.Join(r.ResourceRoot, "config")
	} else {
		r.Env["CLAUDE_CONFIG_DIR"] = filepath.Join(r.ResourceRoot, "config")
		r.Env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
		r.Env["DISABLE_AUTOUPDATER"] = "1"
	}
	r.Contract = filepath.Join(output, "case.json")
	// Preserve native identities in contract; only explicit descriptor paths are mutable.
	if err := copyFile(filepath.Join(source, "plan.json"), r.Contract); err != nil {
		return err
	}
	r.ContractSHA256, err = fileDigest(r.Contract)
	if err != nil {
		return err
	}
	path := filepath.Join(output, "run.json")
	phases := []string{"main"}
	if plan["driverProfile"] == "remote-close" {
		phases = append(phases, "probe")
	}
	for _, phase := range phases {
		selected := source
		_, _, selectedDoc, e := loadMockPhase(selected, r.Provider, phase)
		if e != nil {
			return fmt.Errorf("%s: %w", phase, e)
		}
		planHash, e := fileDigest(filepath.Join(selected, "plan.json"))
		if e != nil {
			return e
		}
		manifestHash, e := fileDigest(filepath.Join(selected, "manifest.json"))
		if e != nil {
			return e
		}
		c := mockConnection{ID: phase, Recording: selected, PlanSHA256: planHash, RecordingSHA256: selectedDoc.SHA256, ManifestSHA256: manifestHash, Receipt: filepath.Join(output, phase+".receipt.json"), ProcessReceipt: filepath.Join(output, phase+".process.json")}
		c.command = command{Command: self, Args: []string{"mock", "--run", path, "--connection", phase}}
		c.Launcher, err = writeMockLauncher(r.ResourceRoot, phase, c.command)
		if err != nil {
			return err
		}
		r.Connections = append(r.Connections, c)
	}
	r.Finish = command{Command: self, Args: []string{"mock", "--finish", path}}
	if ctx.Err() != nil {
		return exitcode.Fail(exitcode.Canceled, ctx.Err())
	}
	if err := atomicMockJSON(path, r); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(r)
}

// Prepared launchers accept the native metadata probe without forwarding SDK
// startup arguments. Legacy record/verify launchers retain their own behavior.
func writeMockLauncher(root, phase string, peer command) (string, error) {
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	words := []string{quote(peer.Command)}
	for _, arg := range peer.Args {
		words = append(words, quote(arg))
	}
	invocation := strings.Join(words, " ")
	script := "#!/bin/sh\nif [ \"$#\" -eq 1 ] && { [ \"$1\" = '--version' ] || [ \"$1\" = '-v' ]; }; then\n  exec " + invocation + " --version\nfi\nexec " + invocation + "\n"
	path := filepath.Join(root, "peer-"+phase)
	return path, os.WriteFile(path, []byte(script), 0o700)
}

// This reads immutable metadata only: no lock file, connection claim, protocol
// child or success receipt. The recorded native identity is not our tool version.
func mockNativeVersion(path, id string, r *mockRun) error {
	if err := mockOwner(r); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "closed")); !os.IsNotExist(err) {
		return errors.New("mock run is finished")
	}
	declared := false
	for _, c := range r.Connections {
		if c.ID == id {
			declared = true
			break
		}
	}
	if !declared {
		return errors.New("undeclared mock connection")
	}
	if err := checkMockMetadata(r); err != nil {
		return err
	}
	var plan object
	if err := readJSON(r.Contract, &plan); err != nil {
		return err
	}
	cliVersion := strings.TrimSpace(str(plan["cliVersion"]))
	if cliVersion == "" {
		cliVersion = "unknown (mock; recorded native CLI version unavailable)"
	}
	_, err := fmt.Fprintln(os.Stdout, cliVersion)
	return err
}

func readMockRun(path string) (*mockRun, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("run descriptor must be absolute")
	}
	var r mockRun
	if err := readJSON(path, &r); err != nil {
		return nil, err
	}
	if r.SchemaVersion != 1 || r.RunID == "" || len(r.Connections) == 0 || !filepath.IsAbs(r.ResourceRoot) || !strings.HasPrefix(filepath.Base(r.ResourceRoot), "agent-sdk-testify-mock-") {
		return nil, errors.New("invalid mock run descriptor or schemaVersion")
	}
	if r.Workspace != filepath.Join(r.ResourceRoot, "project") {
		return nil, errors.New("invalid owned workspace")
	}
	return &r, nil
}

// Shared locks cover the entire peer lifetime; exclusive finish never deletes
// directories while a declared peer is still using them. Locks survive SIGKILL.
func lockMockRun(path string, exclusive bool) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), "run.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		return nil, errors.New("mock run busy: close and wait for all peers before finish")
	}
	return f, nil
}
func unlockMockRun(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func mockOwner(r *mockRun) error {
	info, err := os.Lstat(r.ResourceRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("owned root is not a directory")
	}
	b, err := os.ReadFile(filepath.Join(r.ResourceRoot, ".owner"))
	if err != nil {
		return err
	}
	if string(b) != r.RunID {
		return errors.New("mock resource ownership mismatch")
	}
	return nil
}

func runPreparedMock(ctx context.Context, path, id string, native []string) (err error) {
	r, err := readMockRun(path)
	if err != nil {
		return err
	}
	if len(native) == 1 && (native[0] == "--version" || native[0] == "-v") {
		return mockNativeVersion(path, id, r)
	}
	lock, err := lockMockRun(path, false)
	if err != nil {
		return err
	}
	defer unlockMockRun(lock)
	if err := mockOwner(r); err != nil {
		return err
	}
	if _, e := os.Stat(filepath.Join(filepath.Dir(path), "closed")); !os.IsNotExist(e) {
		return errors.New("mock run is finished")
	}
	var c *mockConnection
	for i := range r.Connections {
		if r.Connections[i].ID == id {
			c = &r.Connections[i]
			break
		}
	}
	if c == nil {
		return errors.New("undeclared mock connection")
	}
	claim, e := os.OpenFile(c.ProcessReceipt+".started", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if e != nil {
		return e
	}
	if err := claim.Close(); err != nil {
		return err
	}
	process := object{"schemaVersion": 1, "runId": r.RunID, "connectionId": id, "status": "failed", "exitCode": -1, "toolVersion": version.Version}
	defer func() {
		if err != nil {
			process["error"] = err.Error()
		}
		err = errors.Join(err, atomicMockJSON(c.ProcessReceipt, process))
	}()
	if r.Builtin != nil {
		if err := checkBuiltinMaterial(r.Recording, r.Builtin.Material); err != nil {
			return err
		}
	}
	if err := errors.Join(checkMockIdentity(*c), checkMockHistory(r)); err != nil {
		return err
	}
	if err := protocol.ValidateNativeArgs(r.Provider, native); err != nil {
		return exitcode.Fail(exitcode.Configuration, err)
	}
	self, e := os.Executable()
	if e != nil {
		return e
	}
	args := []string{"mock", "--recording", c.Recording, "--provider", r.Provider, "--report", c.Receipt, "--phase", c.ID, "--"}
	args = append(args, native...)
	cmd := exec.CommandContext(context.WithoutCancel(ctx), self, args...)
	cmd.Dir = r.Workspace
	cmd.Env = []string{"PATH=/missing"}
	for k, v := range r.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.ExtraFiles = []*os.File{lock}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = waitDriver(ctx, cmd)
	if cmd.ProcessState != nil {
		process["exitCode"] = cmd.ProcessState.ExitCode()
	}
	// The supervisor stamps a completed protocol report with this allocation;
	// finish rejects a valid report borrowed from a different prepared execution.
	var receipt object
	if e := readJSON(c.Receipt, &receipt); e == nil {
		receipt["builtin"] = r.Builtin
		receipt["runId"] = r.RunID
		receipt["connectionId"] = c.ID
		err = errors.Join(err, atomicMockJSON(c.Receipt, receipt))
	} else {
		err = errors.Join(err, e)
	}
	process["tool"] = self
	if digest, e := fileDigest(self); e == nil {
		process["toolSHA256"] = digest
	} else {
		err = errors.Join(err, e)
	}
	if err == nil {
		process["status"] = "passed"
	} else if ctx.Err() != nil {
		err = exitcode.Fail(exitcode.Canceled, ctx.Err())
	} else if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		if code <= 0 {
			code = exitcode.Failure
		}
		err = exitcode.Fail(code, err)
	}
	return err
}

func checkMockIdentity(c mockConnection) error {
	raw := "raw/raw.jsonl"
	if c.ID == "probe" {
		raw = "remote-probe/raw/raw.jsonl"
	}
	for name, digest := range map[string]string{"plan.json": c.PlanSHA256, "manifest.json": c.ManifestSHA256, raw: c.RecordingSHA256} {
		if err := verifyFile(c.Recording, name, digest); err != nil {
			return err
		}
	}
	return verifySourcePhase(c.Recording, true)
}

func finishMock(path string) (err error) {
	r, err := readMockRun(path)
	if err != nil {
		return err
	}
	lock, err := lockMockRun(path, true)
	if err != nil {
		report := object{"schemaVersion": 1, "runId": r.RunID, "status": "failed", "cleanupComplete": false, "error": err.Error()}
		return errors.Join(err, atomicMockJSON(filepath.Join(filepath.Dir(path), "finish.json"), report), json.NewEncoder(os.Stdout).Encode(report))
	}
	defer unlockMockRun(lock)
	output := filepath.Dir(path)
	report := object{"schemaVersion": 1, "runId": r.RunID, "status": "failed", "cleanupComplete": false}
	defer func() {
		if err == nil {
			report["status"] = "passed"
		} else {
			report["error"] = err.Error()
		}
		err = errors.Join(err, atomicMockJSON(filepath.Join(output, "finish.json"), report))
		err = errors.Join(err, json.NewEncoder(os.Stdout).Encode(report))
	}()
	var previousCleanup object
	cleanedBuiltin := false
	if r.Builtin != nil {
		_, statErr := os.Stat(r.ResourceRoot)
		cleanedBuiltin = os.IsNotExist(statErr) && readJSON(filepath.Join(output, "cleanup.json"), &previousCleanup) == nil && previousCleanup["runId"] == r.RunID && previousCleanup["cleanupComplete"] == true
	}
	var builtinErr error
	if r.Builtin != nil {
		_, hash, e := readBuiltin()
		if e != nil {
			builtinErr = e
		} else if hash != r.Builtin.ManifestSHA256 {
			builtinErr = errors.New("builtin manifest drift")
		} else if cleanedBuiltin {
			if message := str(previousCleanup["builtinError"]); message != "" {
				builtinErr = errors.New(message)
			}
		} else {
			builtinErr = checkBuiltinMaterial(r.Recording, r.Builtin.Material)
		}
		err = errors.Join(err, builtinErr)
	}
	var peers []object
	for _, c := range r.Connections {
		var peerErr error
		if !cleanedBuiltin {
			peerErr = checkMockIdentity(c)
		}
		var receipt, process object
		peerErr = errors.Join(peerErr, readJSON(c.Receipt, &receipt), readJSON(c.ProcessReceipt, &process))
		if receipt["runId"] != r.RunID || receipt["connectionId"] != c.ID || receipt["schemaVersion"] != float64(1) || receipt["status"] != "passed" || receipt["provider"] != r.Provider || receipt["recordingSHA256"] != c.RecordingSHA256 || receipt["planSHA256"] != c.PlanSHA256 || receipt["fullConsumption"] != true || receipt["clientEOF"] != true || receipt["cleanupComplete"] != true || receipt["nativeLaunched"] != false || receipt["exitCode"] != float64(0) {
			peerErr = errors.Join(peerErr, errors.New("receipt does not prove strict full consumption, EOF and normal exit"))
		}
		if process["schemaVersion"] != float64(1) || process["runId"] != r.RunID || process["connectionId"] != c.ID || process["status"] != "passed" || process["exitCode"] != float64(0) {
			peerErr = errors.Join(peerErr, errors.New("missing or unsuccessful process exit receipt"))
		}
		p := object{"id": c.ID, "receipt": c.Receipt, "processReceipt": c.ProcessReceipt, "status": "passed"}
		if peerErr != nil {
			p["status"] = "failed"
			p["error"] = peerErr.Error()
			err = errors.Join(err, fmt.Errorf("%s: %w", c.ID, peerErr))
		}
		peers = append(peers, p)
	}
	report["connections"] = peers
	err = errors.Join(err, checkMockMetadata(r))
	// Durable closure prevents new peers even when cleanup must be retried.
	err = errors.Join(err, atomicMockJSON(filepath.Join(output, "closed"), object{"runId": r.RunID}))
	ownerErr := mockOwner(r)
	historyResult := object{"status": "passed"}
	if ownerErr == nil {
		if e := checkMockHistory(r); e != nil {
			historyResult["status"] = "failed"
			historyResult["error"] = e.Error()
			err = errors.Join(err, e)
		}
	} else if os.IsNotExist(ownerErr) {
		var previous object
		if readJSON(filepath.Join(output, "cleanup.json"), &previous) == nil && previous["runId"] == r.RunID {
			historyResult = obj(previous["history"])
			if historyResult["status"] != "passed" {
				err = errors.Join(err, fmt.Errorf("history: %s", str(historyResult["error"])))
			}
		}
	}
	report["history"] = historyResult
	if os.IsNotExist(ownerErr) {
		var previous object
		if readJSON(filepath.Join(output, "cleanup.json"), &previous) == nil && previous["runId"] == r.RunID && previous["cleanupComplete"] == true {
			report["cleanupComplete"] = true
		} else {
			err = errors.Join(err, ownerErr)
		}
	} else if ownerErr != nil {
		err = errors.Join(err, ownerErr)
	} else {
		cleanupErr := os.RemoveAll(r.ResourceRoot)
		report["cleanupComplete"] = cleanupErr == nil
		cleanup := object{"schemaVersion": 1, "runId": r.RunID, "resourceRoot": r.ResourceRoot, "cleanupComplete": cleanupErr == nil, "history": historyResult}
		if builtinErr != nil {
			cleanup["builtinError"] = builtinErr.Error()
		}
		err = errors.Join(err, cleanupErr, atomicMockJSON(filepath.Join(output, "cleanup.json"), cleanup))
	}
	return err
}

func atomicMockJSON(path string, v any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".mock-json-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(v)
	err = errors.Join(writeErr, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func checkMockMetadata(r *mockRun) error {
	if r.Builtin != nil {
		if err := checkBuiltinSelection(r.Builtin.Material); err != nil {
			return err
		}
		_, hash, err := readBuiltin()
		if err != nil {
			return err
		}
		if hash != r.Builtin.ManifestSHA256 {
			return errors.New("builtin manifest drift")
		}
		if r.Recording != filepath.Join(r.ResourceRoot, "builtin") {
			return errors.New("builtin ownership path drift")
		}
		for _, c := range r.Connections {
			raw := "raw/raw.jsonl"
			if c.ID == "probe" {
				raw = "remote-probe/raw/raw.jsonl"
			}
			files := r.Builtin.Material.Files
			if c.Recording != r.Recording || c.PlanSHA256 != files["plan.json"] || c.ManifestSHA256 != files["manifest.json"] || c.RecordingSHA256 != files[raw] {
				return errors.New("builtin connection identity drift")
			}
		}
	}
	if err := verifyFile(filepath.Dir(r.Contract), filepath.Base(r.Contract), r.ContractSHA256); err != nil {
		return err
	}
	if err := verifyFile(filepath.Dir(r.Materialization), filepath.Base(r.Materialization), r.MaterializationSHA256); err != nil {
		return fmt.Errorf("history materialization: %w", err)
	}
	return nil
}

func checkMockHistory(r *mockRun) error {
	if err := checkMockMetadata(r); err != nil {
		return err
	}
	var m object
	if err := readJSON(r.Materialization, &m); err != nil {
		return err
	}
	var errs []error
	for _, group := range []string{"copies", "transformations"} {
		for _, v := range array(m[group]) {
			entry := obj(v)
			errs = append(errs, verifyFile(r.Recording, str(entry["source"]), str(entry["sourceSHA256"])), verifyFile(r.ResourceRoot, str(entry["derived"]), str(entry["derivedSHA256"])))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("history materialization: %w", err)
	}
	return nil
}
