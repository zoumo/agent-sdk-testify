// Package official owns official SDK driver orchestration and its evidence.
package official

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/zoumo/agent-sdk-testify/data"
	"github.com/zoumo/agent-sdk-testify/internal/catalog"
	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/protocol"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type RecordOptions struct {
	FixtureDir     string
	URL            string
	TokenFile      string
	Command        string
	NativeArgs     []string
	Timeout        time.Duration
	ConnectTimeout time.Duration
}

// Record owns only the driver and orchestration facts. Each SDK peer starts
// the public record command, which owns its normal execution and publication.
func Record(ctx context.Context, o RecordOptions) (runErr error) {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	fixture, err := lifecycle.OpenFixture(o.FixtureDir)
	if err != nil {
		return err
	}
	plan, err := lifecycle.LoadPlan(fixture)
	if err != nil {
		return err
	}
	if fixture.Source.Kind == "plan" || fixture.Source.Provenance == "consumer" {
		return errors.New("record source has no official driver")
	}
	bound, err := catalog.Load(data.Scenarios(), catalog.Selection{Provider: fixture.AgentType, Case: tool.Str(plan["caseId"]), Model: tool.Str(plan["model"])})
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
		if rel, err := filepath.Rel(filepath.Dir(fixture.ResourceRoot), path); err == nil && filepath.IsLocal(rel) {
			runtimeRelative, runtimeErr := filepath.Rel(fixture.ResourceRoot, path)
			if runtimeErr != nil || !filepath.IsLocal(runtimeRelative) {
				return errors.New("token file must be outside fixture evidence (use runtime or an external file)")
			}
		}
	} else {
		o.Command, err = tool.ValidateCommand(o.Command)
		if err != nil {
			return err
		}
		if err := protocol.ValidateNativeArgs(fixture.AgentType, o.NativeArgs); err != nil {
			return err
		}
	}
	driver, err := ResolveDriver(fixture.AgentType)
	if err != nil {
		return err
	}
	driverHash, err := tool.FileDigest(driver)
	if err != nil {
		return err
	}
	activity, err := lifecycle.BeginOfficial(o.FixtureDir, "record")
	if err != nil {
		return err
	}
	o.FixtureDir, fixture = activity.FixtureDir, activity.Fixture
	result := tool.Object{"schemaVersion": 2, "id": activity.ID, "fixtureId": fixture.FixtureID, "mode": "record", "status": "failed", "cleanupComplete": true, "case": plan["caseId"], "agentType": fixture.AgentType, "source": fixture.Source, "driver": tool.Object{"sha256": driverHash}, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
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
	env, err := lifecycle.RecordEnvironment(fixture)
	if err != nil {
		return err
	}
	route := fixture.Routes["record"]
	// These are the normal stable route slots, rebound by agent record. There
	// is no second protocol workspace or SDK-specific transport implementation.
	plan["project"] = route.Workspace
	plan["args"] = o.NativeArgs
	plan["stdinEOFAction"] = "close"
	if fixture.AgentType == "claude" && plan["driverProfile"] == "interrupt" {
		plan["stdinEOFAction"] = "interrupt"
	}
	seedRoot := ""
	if plan["driverProfile"] == "existing-children" {
		seedRoot = filepath.Dir(route.Workspace)
		if fixture.AgentType == "claude" {
			plan["args"] = append(tool.Strings(plan["args"]), "--resume", tool.Str(tool.Obj(plan["historySeed"])["sessionId"]))
		}
	}
	runtime := newDriverRuntime(root, route.Workspace, env["HOME"], env[map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}[fixture.AgentType]], filepath.Join(root, "raw"), filepath.Join(root, "tmp"), seedRoot, false)
	private := filepath.Join(fixture.ResourceRoot, "official", activity.ID)
	var service interface {
		URL() string
		TokenFile() string
		Start(context.Context, native.Options, tool.Object) error
		Close() error
		Stopped() bool
		ProcessID() int
		Evidence() tool.Object
	}
	if plan["driverProfile"] == "remote-close" && o.URL == "" {
		if err := os.MkdirAll(private, 0o700); err != nil {
			return err
		}
		service, err = native.ReserveRecordService(ctx, private)
		if err != nil {
			return err
		}
		o.URL, o.TokenFile = service.URL(), service.TokenFile()
		defer func() {
			cleanupErr := service.Close()
			if cleanupErr == nil {
				if pid := service.ProcessID(); pid > 0 {
					runErr = errors.Join(runErr, activity.TrackProcess("service", "stopped", pid))
				}
			} else {
				result["cleanupComplete"] = false
			}
			cleanup := tool.Object{}
			readErr := tool.ReadJSON(filepath.Join(private, "remote-service.json"), &cleanup)
			result["remoteService"] = tool.Object{"owned": true, "cleanupComplete": cleanupErr == nil && readErr == nil, "native": service.Evidence(), "cleanup": cleanup}
			runErr = errors.Join(runErr, readErr)
			runErr = errors.Join(runErr, cleanupErr)
		}()
	} else if o.URL != "" {
		result["remoteService"] = tool.Object{"owned": false}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	ids := lifecycle.ConnectionIDs(fixture.Source, plan)
	launchEnv, err := activity.AuthorizeRecord(driverHash, ids)
	if err != nil {
		return err
	}
	peers := make(map[string]tool.Command, len(ids))
	for _, id := range ids {
		args := []string{"agent", "record", "--fixture-dir", o.FixtureDir, "--connection", id}
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
		peers[id] = tool.Command{Command: self, Args: args, Env: launchEnv}
	}
	var serviceDone chan error
	var cancelService context.CancelFunc
	if service != nil {
		var serviceContext context.Context
		serviceContext, cancelService = context.WithCancel(ctx)
		serviceDone = make(chan error, 1)
		go func() {
			execution, err := waitRecordStart(serviceContext, o.FixtureDir, fixture, filepath.Join(root, "peer-main.results.jsonl"))
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
			runtimeEnv, err := lifecycle.RecordEnvironment(fixture)
			if err != nil {
				serviceDone <- err
				return
			}
			maps.Copy(runtimeEnv, execution.Env)
			runtimePlan["cli"], runtimePlan["args"] = o.Command, o.NativeArgs
			nativeOptions := native.Options{CLI: o.Command, RuntimeEnv: runtimeEnv, ProbeProcess: func(state string, pid int) error { return activity.TrackProcess("service-probe", state, pid) }}
			if err := native.PrepareNative(serviceContext, &nativeOptions, runtimePlan, filepath.Dir(execution.Home)); err != nil {
				serviceDone <- err
				return
			}
			if err := tool.WriteJSON(runtimePlanPath, runtimePlan); err != nil {
				serviceDone <- err
				return
			}
			execution.RuntimeEnv = runtimeEnv
			materialPlanPath := filepath.Join(filepath.Dir(runtimePlanPath), "material-plan.json")
			if err := tool.WriteJSON(materialPlanPath, corpus.MaterialPlanFromRuntime(runtimePlan)); err != nil {
				serviceDone <- err
				return
			}
			if err := lifecycle.SaveNativePreparation(o.FixtureDir, fixture, *execution, runtimePlan, runtimePlanPath, materialPlanPath); err != nil {
				serviceDone <- err
				return
			}
			runtimePlan["project"] = execution.Workspace
			if err := activity.TrackProcess("service", "launching", 0); err != nil {
				serviceDone <- err
				return
			}
			nativeOptions.ProcessStarted = func(pid int) error { return activity.TrackProcess("service", "started", pid) }
			serviceErr := service.Start(serviceContext, nativeOptions, runtimePlan)
			if service.ProcessID() == 0 {
				serviceErr = errors.Join(serviceErr, activity.TrackProcess("service", "not-started", 0))
			}
			serviceDone <- serviceErr
		}()
	}
	stopService := func() error {
		if serviceDone != nil {
			cancelService()
			err := <-serviceDone
			serviceDone = nil
			return err
		}
		return nil
	}
	run, driverErr := runDriver(ctx, driverRunOptions{
		Activity:   activity,
		Driver:     driver,
		Env:        env,
		Workspace:  route.Workspace,
		Root:       root,
		PrivateDir: private,
		Material:   corpus.MaterialPlanFromRuntime(plan),
		Runtime:    runtime,
		Peers:      peers,
		ReportFile: "report.json",
		Inspect: func(outcome, report tool.Object, _ error) error {
			inspectErr := validateBusinessEvidence(fixture.AgentType, plan, bound, outcome, report)
			refs, refErr := officialRecordReferences(o.FixtureDir, fixture, root, ids)
			result["connections"] = refs
			inspectErr = errors.Join(inspectErr, refErr)
			if refErr == nil && len(refs) > 0 && (o.URL == "" || service != nil) {
				executionID := tool.Str(refs[0]["executionId"])
				execution, preparationErr := lifecycle.ReadExecution(o.FixtureDir, fixture, filepath.Join(o.FixtureDir, "executions", executionID, "execution.json"))
				if preparationErr == nil {
					preparationErr = lifecycle.RebindNativePreparationExecution(o.FixtureDir, *execution)
				}
				if preparationErr == nil {
					paths := map[string]string{
						"execution":    filepath.Join(o.FixtureDir, "executions", executionID, "execution.json"),
						"isolation":    filepath.Join(o.FixtureDir, "executions", executionID, "receipts", "isolation.json"),
						"preparedPlan": filepath.Join(filepath.Dir(execution.Home), "record-plan.json"),
						"materialPlan": filepath.Join(filepath.Dir(execution.Home), "material-plan.json"),
					}
					references := tool.Object{}
					for name, path := range paths {
						reference, referenceErr := lifecycle.FixtureReference(o.FixtureDir, path)
						hash, hashErr := tool.FileDigest(path)
						preparationErr = errors.Join(preparationErr, referenceErr, hashErr)
						references[name] = tool.Object{"path": reference, "sha256": hash}
					}
					if preparationErr == nil {
						preparation := tool.Object{"schemaVersion": 2, "executionId": executionID, "material": execution.Artifact}
						for name, reference := range references {
							preparation[name] = reference
						}
						result["recordPreparation"] = preparation
					}
				}
				inspectErr = errors.Join(inspectErr, preparationErr)
			}

			// The parent report is produced after material closure. Apply the
			// capture credential check at its driver-run evidence location.
			scan, scanErr := corpus.ScanCaptureCredentialsWithEnvironment(root, env)
			result["credentialScan"] = scan
			return errors.Join(inspectErr, scanErr)
		},
	})
	runErr = driverErr
	runErr = errors.Join(runErr, stopService())
	if run == nil {
		return runErr
	}
	process := maps.Clone(run.Process)
	delete(process, "pid")
	result["driverProcess"] = process
	if run.ProcessGroupFailed {
		result["cleanupComplete"] = false
	}
	result["business"], result["report"] = run.Outcome, run.Report
	result["driverFiles"] = run.Files
	if run.FileHashesComplete {
		runErr = errors.Join(runErr, tool.WriteJSON(filepath.Join(activity.Dir, "driver-files.json"), run.Files))
	}
	return runErr
}

// References come exclusively from each launcher's actual notification pipe.
// No directory inventory, latest execution or caller-supplied success is used.
func officialRecordReferences(fixtureDir string, fixture *lifecycle.Fixture, root string, ids []string) ([]tool.Object, error) {
	refs, err := collectPeerReferences(ids, func(id string) (tool.Object, error) {
		return recordReference(fixtureDir, fixture, root, id)
	})
	if err != nil {
		return refs, err
	}
	final := 0
	for _, ref := range refs {
		if len(tool.Obj(tool.Obj(ref["finished"])["material"])) > 0 {
			final++
		}
	}
	if final != 1 {
		return refs, errors.New("official record requires exactly one final material notification")
	}
	return refs, nil
}

func recordReference(fixtureDir string, fixture *lifecycle.Fixture, root, id string) (tool.Object, error) {
	ref, execution, connection, err := readPeerReference(fixtureDir, fixture, root, id, "record", func(execution lifecycle.Execution) error {
		return execution.Validate(*fixture)
	})
	if err != nil {
		return ref, err
	}
	material := execution.Artifact
	if material.ID == "" || material.Path != filepath.Join(fixtureDir, "materials", material.ID) {
		return ref, errors.New("record did not publish a material")
	}
	var descriptor corpus.Material
	if err := tool.ReadJSON(filepath.Join(material.Path, "material.json"), &descriptor); err != nil {
		return ref, err
	}
	if !material.SameDescriptor(descriptor) {
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
	finished := tool.Obj(ref["finished"])
	if notifiedMaterial := tool.Obj(finished["material"]); len(notifiedMaterial) > 0 {
		if notifiedMaterial["id"] != material.ID || notifiedMaterial["planSHA256"] != material.PlanSHA256 || notifiedMaterial["manifestSHA256"] != material.ManifestSHA256 || notifiedMaterial["recordingSHA256"] != material.RecordingSHA256 {
			return ref, errors.New("final material notification differs from published capture")
		}
	} else if len(execution.Connections) == 1 {
		return ref, errors.New("final material notification is absent")
	}
	var receipt tool.Object
	if err := tool.ReadJSON(connection.Receipt, &receipt); err != nil {
		return ref, err
	}
	if receipt["captureStatus"] != "complete" || receipt["cleanupComplete"] != true {
		return ref, errors.New("capture is incomplete")
	}
	return ref, nil
}

func environment(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
