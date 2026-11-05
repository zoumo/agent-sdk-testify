package official

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

type VerifyOptions struct {
	SessionDir string
	Timeout    time.Duration
	Index      string
	Scenarios  string
	Archive    string
}

// Verify owns the SDK driver, not an execution or a protocol environment.
func Verify(ctx context.Context, o VerifyOptions) (runErr error) {
	if !filepath.IsAbs(o.SessionDir) {
		return errors.New("official verify requires absolute --session-dir")
	}
	if o.Timeout < 0 {
		return errors.New("timeout must be nonnegative")
	}
	if o.Index == "" && (o.Scenarios != "" || o.Archive != "") {
		return errors.New("scenarios and archive require index")
	}
	c := corpus.Corpus{Index: o.Index, Scenarios: o.Scenarios, Archive: o.Archive}
	if c.Index != "" {
		if c.Archive == "" {
			var err error
			c.Archive, err = corpus.DefaultArchive()
			if err != nil {
				return err
			}
		}
		if err := c.Normalize(); err != nil {
			return err
		}
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	session, err := lifecycle.OpenSession(o.SessionDir)
	if err != nil {
		return err
	}
	var activity *lifecycle.OfficialRun
	verificationSaved := false
	evidence := tool.Object{"schemaVersion": 1, "sessionId": session.SessionID, "mode": "verify", "pipeline": "public-agent-mock", "status": "failed", "cleanupComplete": true, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	defer func() {
		if activity == nil {
			return
		}
		if !verificationSaved {
			evidence["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			if runErr == nil {
				evidence["status"] = "passed"
			} else {
				evidence["error"] = runErr.Error()
			}
			runErr = errors.Join(runErr, tool.WriteJSON(filepath.Join(activity.Dir, "verification.json"), evidence))
		}
		if verificationSaved && runErr != nil {
			evidence["status"] = "failed"
			evidence["error"] = runErr.Error()
		}
		runErr = errors.Join(runErr, activity.Complete(evidence))
	}()

	material, err := lifecycle.LatestArtifact(o.SessionDir, session)
	if err != nil {
		return err
	}
	if material.Provenance != "official" && material.Provenance != "builtin" {
		return errors.New("material has no applicable official verifier: provenance must be official or builtin")
	}
	evidence["recording"], evidence["material"] = material.Path, material
	before, err := corpus.Inventory(material.Path, false)
	if err != nil {
		return err
	}
	evidence["sourceFiles"] = before
	if err := corpus.ValidateVerificationSource(*material, before); err != nil {
		return err
	}
	if c.Index != "" {
		evidence["admission"] = &lifecycle.Admission{Index: c.Index, Scenarios: c.Scenarios, Archive: c.Archive}
	}

	var plan tool.Object
	if err := tool.ReadJSON(filepath.Join(material.Path, "plan.json"), &plan); err != nil {
		return err
	}
	bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: session.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
	if err != nil {
		return err
	}
	if err := catalog.CheckVerifierPlan(plan, bound); err != nil {
		return err
	}
	evidence["case"], evidence["provider"], evidence["binding"] = plan["caseId"], session.AgentType, bound
	contracts := tool.Object{}
	for _, name := range []string{tool.Str(bound["storyId"]) + ".json", "bindings/" + session.AgentType + ".json"} {
		content, err := fs.ReadFile(data.Scenarios(), name)
		if err != nil {
			return err
		}
		contracts[name] = tool.DigestBytes(content)
	}
	evidence["contracts"] = contracts
	driver, err := ResolveDriver(session.AgentType)
	if err != nil {
		return err
	}
	driverHash, err := tool.FileDigest(driver)
	if err != nil {
		return err
	}
	evidence["driver"] = tool.Object{"path": driver, "sha256": driverHash}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	toolHash, err := tool.FileDigest(self)
	if err != nil {
		return err
	}
	evidence["tool"] = tool.Object{"version": version.Version, "sha256": toolHash}
	// Preconditions do not create orchestration facts. Registration then pins the
	// exact preflight input against concurrent record before a driver can start.
	activity, err = lifecycle.BeginOfficial(o.SessionDir, "verify")
	if err != nil {
		return err
	}
	o.SessionDir, session = activity.SessionDir, activity.Session
	evidence["id"], evidence["output"] = activity.ID, activity.Dir
	pinned, err := lifecycle.LatestArtifact(o.SessionDir, session)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(material, pinned) {
		return errors.New("current recording changed before verification")
	}
	inventory, err := corpus.Inventory(material.Path, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, inventory) {
		return errors.New("material changed before verification")
	}
	var index lifecycle.Index
	if err := tool.ReadJSON(lifecycle.IndexPath(o.SessionDir), &index); err != nil {
		return err
	}
	previous := map[string]bool{}
	for _, execution := range index.Executions {
		previous[execution.ID] = true
	}
	evidence["priorExecutions"] = previous
	root := filepath.Join(activity.Dir, "driver")
	route := session.Routes["mock"]
	env := route.Env
	plan["root"], plan["home"], plan["config"], plan["raw"] = root, env["HOME"], env[map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[session.AgentType]], filepath.Join(material.Path, "raw")
	plan["_seedRoot"] = filepath.Dir(route.Workspace)
	plan["_recordedWorkspace"] = plan["_fixtureProject"]
	plan["_fixtureProject"], plan["_replay"] = route.Workspace, true
	// project/historyProject retain the original native protocol cwd. Only the
	// fixture and SDK host use the ordinary mock route slots.
	peer := DriverPeer{}
	results := tool.Object{}
	private := filepath.Join(session.ResourceRoot, "official", activity.ID)
	if err := os.MkdirAll(private, 0o700); err != nil {
		return err
	}
	for _, id := range material.Connections {
		results[id] = filepath.Join(root, "peer-"+id+".results.jsonl")
		args := []string{"agent", "mock", "--session-dir", o.SessionDir, "--connection", id}
		if o.Timeout > 0 {
			args = append(args, "--timeout", fmt.Sprint(o.Timeout.Seconds()))
		}
		request := filepath.Join(private, "peer-"+id+".json")
		if err := tool.WriteJSON(request, tool.Command{Command: self, Args: args}); err != nil {
			return err
		}
		launcher, err := WritePeerLauncher(private, id, tool.Command{Command: self, Args: []string{"--internal-peer", "--request", request, "--lifecycle", filepath.Join(root, "peer-"+id+".process.json"), "--results", tool.Str(results[id])}})
		if err != nil {
			return err
		}
		entry := tool.Command{Command: launcher}
		switch id {
		case "main":
			peer.Command = entry
		case "probe":
			peer.Probe = &entry
		default:
			return fmt.Errorf("unsupported official connection %s", id)
		}
	}
	plan["_agentResults"] = results
	materials := filepath.Join(root, "materials")
	if err := os.MkdirAll(materials, 0o700); err != nil {
		return err
	}
	if err := tool.WriteJSON(filepath.Join(materials, "case.json"), plan); err != nil {
		return err
	}
	request := filepath.Join(root, "request.json")
	if err := tool.WriteJSON(request, Request{Case: tool.Str(plan["caseId"]), Materials: materials, Workspace: route.Workspace, Peer: peer, Output: root}); err != nil {
		return err
	}
	stdout, err := os.OpenFile(filepath.Join(root, "stdout.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(root, "stderr.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer stderr.Close()
	cmd := exec.CommandContext(context.WithoutCancel(ctx), driver, "--request", request)
	cmd.Env = environment(env)
	cmd.Dir, cmd.Stdout, cmd.Stderr = route.Workspace, stdout, stderr
	runErr = activity.WaitDriver(ctx, cmd)
	process := tool.Object{"started": cmd.Process != nil, "reaped": cmd.ProcessState != nil, "cleanupComplete": cmd.ProcessState != nil}
	if cmd.ProcessState != nil {
		process["exitCode"] = cmd.ProcessState.ExitCode()
	}
	if cmd.Process != nil {
		process["pid"] = cmd.Process.Pid
		if err := tool.WaitProcessGroup(cmd.Process.Pid); err != nil {
			process["cleanupComplete"] = false
			evidence["cleanupComplete"] = false
			runErr = errors.Join(runErr, err)
		}
	}
	evidence["driverProcess"] = process
	var outcome, report tool.Object
	outcomeErr := tool.ReadJSON(filepath.Join(root, "result.json"), &outcome)
	reportErr := tool.ReadJSON(filepath.Join(root, "replay-report.json"), &report)
	evidence["business"], evidence["report"], evidence["officialSDK"] = outcome, report, report["officialSDK"]
	runErr = errors.Join(runErr, outcomeErr, reportErr)
	if err := validateBusinessEvidence(session.AgentType, plan, bound, outcome, report); err != nil {
		runErr = errors.Join(runErr, err)
	}
	refs, refErr := mockReferences(o.SessionDir, session, root, material.Connections, *material, previous)
	evidence["connections"] = refs
	runErr = errors.Join(runErr, refErr)
	after, err := corpus.Inventory(material.Path, false)
	runErr = errors.Join(runErr, err)
	if !reflect.DeepEqual(before, after) {
		runErr = errors.Join(runErr, errors.New("material changed during verification"))
	}
	runErr = errors.Join(runErr, snapshotMocks(activity.Dir, refs), activity.RestoreMockRoute())
	var recordEvidenceErr error
	if c.Index != "" && material.OfficialOrigin != nil && runErr == nil {
		target := filepath.Join(root, "record-origin")
		recordEvidenceErr = lifecycle.SnapshotOfficialRecord(o.SessionDir, session, *material, target)
		if recordEvidenceErr == nil {
			files, hashErr := corpus.Inventory(target, false)
			recordEvidenceErr = hashErr
			evidence["officialRecord"] = tool.Object{"root": "driver/record-origin", "files": files}
		}
	}
	hashes, err := history.HashTree(root)
	evidence["driverFiles"] = hashes
	evidence["files"] = tool.Object{}
	for name, hash := range hashes {
		tool.Obj(evidence["files"])["driver/"+name] = hash
	}
	runErr = errors.Join(runErr, err)
	// Preserve successful verification independently of an optional admission.
	if runErr == nil {
		evidence["status"] = "passed"
	}
	evidence["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	if runErr != nil {
		evidence["error"] = runErr.Error()
	}
	if err := tool.WriteJSON(filepath.Join(activity.Dir, "verification.json"), evidence); err != nil {
		return errors.Join(runErr, err)
	}
	verificationSaved = true
	if c.Index != "" {
		admissionErr := runErr
		if admissionErr == nil {
			admissionErr = recordEvidenceErr
		}
		admissionErr = admitVerified(material.Path, tool.Object{"output": activity.Dir}, admissionErr, c)
		if admissionErr != nil {
			evidence["admissionError"] = admissionErr.Error()
		}
		// Admission failure must not overwrite the protocol/business conclusion.
		if admissionErr != nil {
			return admissionErr
		}
	}
	return runErr
}

func validateBusinessEvidence(agent string, plan, bound, outcome, report tool.Object) error {
	sdk := tool.Obj(report["officialSDK"])
	if outcome["status"] != "passed" || report["status"] != "passed" || report["branchStatus"] != "passed" || report["captureStatus"] != "complete" || len(tool.Array(report["gaps"])) != 0 || report["caseId"] != plan["caseId"] || report["storyId"] != bound["storyId"] || report["provider"] != agent || sdk["package"] != map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[agent] || tool.Str(sdk["version"]) == "" {
		return errors.New("official SDK business evidence is absent, failed or untriggered")
	}
	return nil
}
