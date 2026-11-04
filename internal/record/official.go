package record

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
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
	"github.com/zoumo/agent-sdk-testify/internal/official"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// RunOfficial owns only the driver and orchestration facts. Each SDK peer starts
// the public record command, which owns its normal execution and publication.
func RunOfficial(ctx context.Context, o Options) (runErr error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	session, err := lifecycle.OpenActionSession(o.SessionDir)
	if err != nil {
		return err
	}
	plan, err := lifecycle.LoadPlan(session)
	if err != nil {
		return err
	}
	if session.Source.Kind == "plan" || session.Source.Provenance == "consumer" {
		return errors.New("record source has no official driver")
	}
	bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: session.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
	if err != nil {
		return err
	}
	if err := catalog.CheckVerifierPlan(plan, bound); err != nil {
		return err
	}
	if o.URL != "" || o.TokenFile != "" {
		if o.Command != "" || len(o.NativeArgs) > 0 {
			return errors.New("remote target excludes local executable and arguments")
		}
		if err := (&capture.ConnectOptions{URL: o.URL, TokenFile: o.TokenFile}).Validate(); err != nil {
			return err
		}
		path, err := filepath.EvalSymlinks(o.TokenFile)
		if err != nil {
			return err
		}
		if rel, err := filepath.Rel(o.SessionDir, path); err == nil && filepath.IsLocal(rel) {
			return errors.New("token file must be outside the persistent session")
		}
	} else {
		o.Command, err = validateCommand(o.Command)
		if err != nil {
			return err
		}
		if err := protocol.ValidateNativeArgs(session.AgentType, o.NativeArgs); err != nil {
			return err
		}
	}
	driver, err := official.ResolveDriver(session.AgentType)
	if err != nil {
		return err
	}
	driverHash, err := tool.FileDigest(driver)
	if err != nil {
		return err
	}
	activity, err := lifecycle.BeginOfficial(o.SessionDir, "record")
	if err != nil {
		return err
	}
	result := tool.Object{"schemaVersion": 1, "id": activity.ID, "sessionId": session.SessionID, "mode": "record", "status": "failed", "cleanupComplete": true, "case": plan["caseId"], "agentType": session.AgentType, "source": session.Source, "driver": tool.Object{"path": driver, "sha256": driverHash}, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	defer func() {
		result["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		if runErr != nil {
			result["error"] = runErr.Error()
		} else {
			result["status"] = "passed"
		}
		runErr = errors.Join(runErr, activity.Complete(result))
	}()
	root := filepath.Join(activity.Dir, "driver")
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o700); err != nil {
		return err
	}
	env, err := lifecycle.RecordEnvironment(session)
	if err != nil {
		return err
	}
	route := session.Routes["record"]
	// These are the normal stable route slots, rebound by agent record. There
	// is no second protocol workspace or SDK-specific transport implementation.
	plan["root"], plan["project"], plan["home"], plan["config"] = root, route.Workspace, env["HOME"], env[map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[session.AgentType]]
	plan["_fixtureProject"] = route.Workspace
	plan["args"] = o.NativeArgs
	plan["stdinEOFAction"] = "close"
	if session.AgentType == "claude" && plan["driverProfile"] == "interrupt" {
		plan["stdinEOFAction"] = "interrupt"
	}
	plan["raw"] = filepath.Join(root, "raw")
	var service interface {
		URL() string
		TokenFile() string
		Start(context.Context, official.Options, tool.Object) error
		Close() error
		Stopped() bool
	}
	if plan["driverProfile"] == "remote-close" && o.URL == "" {
		privateRoot := filepath.Join(session.ResourceRoot, "official", activity.ID)
		if err := os.MkdirAll(privateRoot, 0o700); err != nil {
			return err
		}
		service, err = official.ReserveRecordService(ctx, privateRoot)
		if err != nil {
			return err
		}
		o.URL, o.TokenFile = service.URL(), service.TokenFile()
		defer func() {
			cleanupErr := service.Close()
			if !service.Stopped() {
				result["cleanupComplete"] = false
			}
			result["remoteService"] = tool.Object{"owned": true, "cleanupComplete": cleanupErr == nil}
			runErr = errors.Join(runErr, cleanupErr)
		}()
	} else if o.URL != "" {
		result["remoteService"] = tool.Object{"owned": false}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	ids := lifecycle.ConnectionIDs(session.Source, plan)
	launchEnv, err := activity.AuthorizeRecord(driverHash, ids)
	if err != nil {
		return err
	}
	peer := official.DriverPeer{}
	results := tool.Object{}
	for _, id := range ids {
		results[id] = filepath.Join(root, "peer-"+id+".results.jsonl")
		args := []string{"agent", "record", "--session-dir", o.SessionDir, "--connection", id}
		if o.Timeout > 0 {
			args = append(args, "--timeout", o.Timeout.String())
		}
		if o.URL != "" {
			args = append(args, "--url", o.URL, "--token-file", o.TokenFile)
			if service != nil {
				args = append(args, "--connect-timeout", "15s")
			} else if o.ConnectTimeout > 0 {
				args = append(args, "--connect-timeout", o.ConnectTimeout.String())
			}
		} else {
			args = append(args, "--cmd", o.Command, "--")
			args = append(args, o.NativeArgs...)
		}
		// Target/authentication locations are private invocation inputs, never
		// part of request.json or the persisted driver plan.
		private := filepath.Join(session.ResourceRoot, "official", activity.ID)
		if err := os.MkdirAll(private, 0o700); err != nil {
			return err
		}
		request := filepath.Join(private, "peer-"+id+".json")
		if err := tool.WriteJSON(request, tool.Command{Command: self, Args: args, Env: launchEnv}); err != nil {
			return err
		}
		launcher, err := official.WritePeerLauncher(private, id, tool.Command{Command: self, Args: []string{"--internal-peer", "--request", request, "--lifecycle", filepath.Join(root, "peer-"+id+".process.json"), "--results", filepath.Join(root, "peer-"+id+".results.jsonl")}})
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
			return fmt.Errorf("unsupported driver connection %s", id)
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
	requestPath := filepath.Join(root, "request.json")
	if err := tool.WriteJSON(requestPath, official.Request{Case: tool.Str(plan["caseId"]), Materials: materials, Workspace: route.Workspace, Peer: peer, Output: root}); err != nil {
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
	var serviceDone chan error
	var cancelService context.CancelFunc
	if service != nil {
		var serviceContext context.Context
		serviceContext, cancelService = context.WithCancel(ctx)
		defer cancelService()
		serviceDone = make(chan error, 1)
		go func() {
			execution, err := waitRecordStart(serviceContext, o.SessionDir, session, filepath.Join(root, "peer-main.results.jsonl"))
			if err != nil {
				serviceDone <- err
				return
			}
			runtimePlanPath := filepath.Join(filepath.Dir(execution.Home), "record-plan.json")
			runtimePlan, err := waitRecordPlan(serviceContext, runtimePlanPath)
			if err != nil {
				serviceDone <- err
				return
			}
			runtimeEnv, err := lifecycle.RecordEnvironment(session)
			if err != nil {
				serviceDone <- err
				return
			}
			maps.Copy(runtimeEnv, execution.Env)
			runtimePlan["cli"], runtimePlan["args"] = o.Command, o.NativeArgs
			nativeOptions := official.Options{CLI: o.Command, RuntimeEnv: runtimeEnv}
			if err := official.PrepareNative(serviceContext, &nativeOptions, runtimePlan, filepath.Dir(execution.Home)); err != nil {
				serviceDone <- err
				return
			}
			if err := tool.WriteJSON(runtimePlanPath, runtimePlan); err != nil {
				serviceDone <- err
				return
			}
			runtimePlan["project"] = execution.Workspace
			serviceDone <- service.Start(serviceContext, nativeOptions, runtimePlan)
		}()
	}
	cmd := exec.CommandContext(context.WithoutCancel(ctx), driver, "--request", requestPath)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = environment(env), route.Workspace, stdout, stderr
	runErr = tool.WaitDriver(ctx, cmd)
	if serviceDone != nil {
		cancelService()
		runErr = errors.Join(runErr, <-serviceDone)
	}
	process := tool.Object{"started": cmd.Process != nil, "reaped": cmd.ProcessState != nil, "cleanupComplete": cmd.ProcessState != nil}
	if cmd.ProcessState != nil {
		process["exitCode"] = cmd.ProcessState.ExitCode()
	}
	if cmd.Process != nil {
		if err := tool.WaitProcessGroup(cmd.Process.Pid); err != nil {
			process["cleanupComplete"] = false
			result["cleanupComplete"] = false
			runErr = errors.Join(runErr, err)
		}
	}
	result["driverProcess"] = process
	var outcome, report tool.Object
	outcomeErr := tool.ReadJSON(filepath.Join(root, "result.json"), &outcome)
	reportErr := tool.ReadJSON(filepath.Join(root, "report.json"), &report)
	result["business"], result["report"] = outcome, report
	runErr = errors.Join(runErr, outcomeErr, reportErr)
	sdkPackage := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[session.AgentType]
	if tool.Obj(report["officialSDK"])["package"] != sdkPackage || report["storyId"] != plan["storyId"] || outcome["status"] != "passed" || report["status"] != "passed" || report["branchStatus"] != "passed" || len(tool.Array(report["gaps"])) != 0 || report["caseId"] != plan["caseId"] || report["provider"] != session.AgentType || tool.Str(tool.Obj(report["officialSDK"])["version"]) == "" {
		runErr = errors.Join(runErr, errors.New("official SDK business evidence is absent, failed or untriggered"))
	}
	refs, refErr := officialRecordReferences(o.SessionDir, session, root, ids)
	result["connections"] = refs
	runErr = errors.Join(runErr, refErr)
	if refErr == nil && len(refs) > 0 && o.URL == "" {
		executionID := tool.Str(refs[0]["executionId"])
		var execution lifecycle.Execution
		preparationErr := tool.ReadJSON(filepath.Join(o.SessionDir, "executions", executionID, "execution.json"), &execution)
		if preparationErr == nil {
			isolationHash, isolationErr := tool.FileDigest(filepath.Join(o.SessionDir, "executions", executionID, "receipts", "isolation.json"))
			planHash, planErr := tool.FileDigest(filepath.Join(filepath.Dir(execution.Home), "record-plan.json"))
			preparationErr = errors.Join(isolationErr, planErr)
			if preparationErr == nil {
				result["recordPreparation"] = tool.Object{"executionId": executionID, "isolationSHA256": isolationHash, "preparedPlanSHA256": planHash}
			}
		}
		runErr = errors.Join(runErr, preparationErr)
	}

	// The parent report is produced after material closure. Apply the existing
	// capture credential check at its new evidence location as well.
	scan, scanErr := corpus.ScanCaptureCredentialsWithEnvironment(root, env)
	result["credentialScan"] = scan
	runErr = errors.Join(runErr, scanErr)
	files, hashErr := history.HashTree(root)
	result["driverFiles"] = files
	runErr = errors.Join(runErr, hashErr)
	if hashErr == nil {
		runErr = errors.Join(runErr, tool.WriteJSON(filepath.Join(activity.Dir, "driver-files.json"), files))
	}
	return runErr
}

// References come exclusively from each launcher's actual notification pipe.
// No directory inventory, latest execution or caller-supplied success is used.
func officialRecordReferences(sessionDir string, session *lifecycle.Session, root string, ids []string) ([]tool.Object, error) {
	var refs []tool.Object
	var failures []error
	executionID := ""
	for _, id := range ids {
		ref, err := recordReference(sessionDir, session, root, id)
		if ref != nil {
			refs = append(refs, ref)
		}
		if err == nil {
			if executionID == "" {
				executionID = tool.Str(ref["executionId"])
			} else if executionID != tool.Str(ref["executionId"]) {
				err = errors.New("official connections used different executions")
			}
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return refs, errors.Join(failures...)
}

func recordReference(sessionDir string, session *lifecycle.Session, root, id string) (tool.Object, error) {
	var process tool.Object
	if err := tool.ReadJSON(filepath.Join(root, "peer-"+id+".process.json"), &process); err != nil {
		return nil, fmt.Errorf("protocol process not started or settled: %w", err)
	}
	f, err := os.Open(filepath.Join(root, "peer-"+id+".results.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	var started, finished tool.Object
	if err := decoder.Decode(&started); err != nil {
		return nil, err
	}
	ref := tool.Object{"started": started, "process": process}
	if err := decoder.Decode(&finished); err != nil {
		return ref, err
	}
	ref["finished"] = finished
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ref, errors.New("unexpected execution notifications")
	}
	executionID := tool.Str(started["executionId"])
	if executionID == "" || filepath.Base(executionID) != executionID || !filepath.IsLocal(executionID) {
		return ref, errors.New("invalid execution reference")
	}
	ref["executionId"], ref["connectionId"] = executionID, id
	dir := filepath.Join(sessionDir, "executions", executionID)
	for _, value := range []tool.Object{started, finished} {
		if value["processId"] != process["pid"] || value["sessionId"] != session.SessionID || value["connectionId"] != id || value["executionId"] != executionID || value["command"] != "record" || value["execution"] != filepath.Join(dir, "execution.json") || value["result"] != filepath.Join(dir, "receipts", id+".result.json") {
			return ref, errors.New("execution notification reference mismatch")
		}
	}
	if started["status"] != "running" || finished["status"] != "passed" || process["exitCode"] != float64(0) || process["reaped"] != true || process["cleanupComplete"] != true {
		return ref, errors.New("protocol process failed or was not reaped")
	}
	var durable tool.Object
	if err := tool.ReadJSON(filepath.Join(dir, "receipts", id+".result.json"), &durable); err != nil {
		return ref, err
	}
	if !reflect.DeepEqual(finished, durable) {
		return ref, errors.New("notification differs from durable result")
	}
	var execution lifecycle.Execution
	if err := tool.ReadJSON(filepath.Join(dir, "execution.json"), &execution); err != nil {
		return ref, err
	}
	if err := execution.Validate(*session); err != nil {
		return ref, err
	}
	material := execution.Artifact
	if material.ID == "" || material.Path != filepath.Join(sessionDir, "materials", material.ID) {
		return ref, errors.New("record did not publish a material")
	}
	var descriptor lifecycle.Artifact
	if err := tool.ReadJSON(filepath.Join(material.Path, "material.json"), &descriptor); err != nil {
		return ref, err
	}
	if !reflect.DeepEqual(material, descriptor) {
		return ref, errors.New("material differs from execution")
	}
	actualFiles, err := history.HashTree(material.Path)
	if err != nil {
		return ref, err
	}
	delete(actualFiles, "material.json")
	if !reflect.DeepEqual(actualFiles, material.Files) {
		return ref, errors.New("material inventory changed")
	}
	for name, expected := range material.Files {
		if !filepath.IsLocal(name) {
			return ref, errors.New("invalid material file")
		}
		actual, err := tool.FileDigest(filepath.Join(material.Path, name))
		if err != nil {
			return ref, err
		}
		if actual != expected {
			return ref, errors.New("material hash mismatch")
		}
	}
	ref["material"] = material
	ref["executionSHA256"], err = tool.FileDigest(filepath.Join(dir, "execution.json"))
	if err != nil {
		return ref, err
	}
	if notifiedMaterial := tool.Obj(finished["material"]); len(notifiedMaterial) > 0 {
		if notifiedMaterial["id"] != material.ID || notifiedMaterial["planSHA256"] != material.PlanSHA256 || notifiedMaterial["manifestSHA256"] != material.ManifestSHA256 || notifiedMaterial["recordingSHA256"] != material.RecordingSHA256 || finished["executionSHA256"] != ref["executionSHA256"] {
			return ref, errors.New("final material notification differs from published capture")
		}
	} else if finished["executionSHA256"] != started["executionSHA256"] {
		return ref, errors.New("partial connection result changed its execution binding")
	}
	for _, connection := range execution.Connections {
		if connection.ID != id {
			continue
		}
		if started["receipt"] != connection.Receipt || finished["receipt"] != connection.Receipt {
			return ref, errors.New("connection receipt reference mismatch")
		}
		var receipt tool.Object
		if err := tool.ReadJSON(connection.Receipt, &receipt); err != nil {
			return ref, err
		}
		if receipt["captureStatus"] != "complete" || receipt["cleanupComplete"] != true {
			return ref, errors.New("capture is incomplete")
		}
		ref["connection"] = connection
		return ref, nil
	}
	return ref, errors.New("connection absent from execution")
}
