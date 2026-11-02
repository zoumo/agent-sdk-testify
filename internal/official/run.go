package official

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func Run(ctx context.Context, o Options) (summary object, runFailure error) {
	var err error
	if o.Mode != "record" && o.Mode != "verify" {
		return nil, errors.New("expected official record or verify")
	}
	if o.Materials != "" {
		o.Materials, err = filepath.Abs(o.Materials)
		if err != nil {
			return nil, err
		}
	}
	plan, err := loadMaterials(o)
	if err != nil {
		return nil, err
	}
	// Bind offline receipts to the source before replacing runtime paths. A raw
	// hash alone cannot prove that official replay used this plan and history.
	source := object{}
	if o.Mode != "record" {
		for _, name := range []string{"plan.json", "manifest.json", "report.json", "result.json", "summary.json"} {
			digest, e := fileDigest(filepath.Join(o.Materials, name))
			if e != nil && !os.IsNotExist(e) {
				return nil, e
			}
			if e == nil {
				source[name] = digest
			}
		}
	}
	// Legacy plans remain source evidence; stale runtime entries are not driver inputs.
	delete(plan, "_mock")
	delete(plan, "_peers")
	if o.Mode == "record" {
		// Loaded plans are inputs, not proof of this attempt's startup policy.
		delete(plan, "recordingIsolation")
		delete(plan, "historySeed")
		delete(plan, "historyProject")
	}
	provider := str(plan["provider"])
	if provider != "codex" && provider != "claude" {
		return nil, errors.New("materials must identify codex or claude")
	}
	if !o.OutputPrepared {
		o.Output, err = createOutput(o.Output, o.Mode)
		if err != nil {
			return nil, err
		}
	}
	o.Output, err = filepath.EvalSymlinks(o.Output)
	if err != nil {
		return nil, err
	}
	root := o.Output
	workspace := filepath.Join(o.Output, "project")
	defer func() {
		cleanupErr := scrubNative(root)
		if o.Mode == "record" {
			runFailure = errors.Join(runFailure, cleanupErr)
			// Retain preparation failures too, before driver/transport receipts exist.
			if runFailure != nil && summary == nil {
				summary = object{"mode": o.Mode, "provider": provider, "case": str(plan["caseId"]), "output": root, "error": runFailure.Error()}
				runFailure = errors.Join(runFailure, writeJSON(filepath.Join(root, "summary.json"), summary))
			}
		}
	}()
	for _, dir := range []string{root, workspace, filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "tmp"), filepath.Join(o.Output, "materials")} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	if err = writeFiles(workspace, obj(obj(plan["scenario"])["files"])); err != nil {
		return nil, err
	}
	// Saved material reuse belongs to offline runs. New captures may only
	// import history through the explicit, provenance-checked seed path.
	if o.Mode != "record" {
		if err = copySaved(o.Materials, root); err != nil {
			return nil, err
		}
	}
	if o.Mode != "record" && provider == "claude" {
		if err = materializeClaude(o.Materials, root, workspace, plan); err != nil {
			return nil, err
		}
	}
	if o.Mode == "record" && plan["driverProfile"] == "existing-children" && o.HistorySeed != "" {
		if err = prepareHistorySeed(o.HistorySeed, root, plan); err != nil {
			return nil, err
		}
	}
	if o.Mode == "record" && plan["driverProfile"] == "existing-children" && len(obj(plan["historySeed"])) == 0 {
		return nil, errors.New("existing-child recording needs supplied saved-history materials")
	}
	if o.Mode != "record" {
		for _, name := range []string{"remote-service.json"} {
			if _, e := os.Stat(filepath.Join(o.Materials, name)); e == nil {
				if err = copyFile(filepath.Join(o.Materials, name), filepath.Join(root, name)); err != nil {
					return nil, err
				}
			}
		}
	} else {
		plan["project"] = workspace
		if err = prepareNative(ctx, &o, plan, root); err != nil {
			return nil, err
		}
		plan["recordingIsolation"] = isolatedRecording
	}
	plan["root"] = root
	plan["home"] = filepath.Join(root, "home")
	plan["config"] = filepath.Join(root, "config")
	plan["raw"] = filepath.Join(root, "raw")
	plan["_fixtureProject"] = workspace
	if str(plan["project"]) == "" {
		plan["project"] = workspace
	}
	var service *remoteService
	if o.Mode == "record" && plan["driverProfile"] == "remote-close" {
		service, err = startRemote(ctx, o, plan)
		if err != nil {
			return nil, err
		}
		defer func() {
			if service != nil {
				_ = service.close()
			}
		}()
	}
	peers, err := preparePeerRuns(o, plan, service)
	if err != nil {
		return nil, err
	}
	// Keep original source data for provider-owned assertions; mutable runtime
	// paths belong to this attempt. No exact SDK or executable version gate.
	if err = writeJSON(filepath.Join(o.Output, "materials/case.json"), plan); err != nil {
		return nil, err
	}
	req := request{Case: str(plan["caseId"]), Materials: filepath.Join(o.Output, "materials"), Workspace: workspace, Peer: peers.driver, Output: root}
	requestPath := filepath.Join(o.Output, "request.json")
	if err = writeJSON(requestPath, req); err != nil {
		return nil, err
	}
	if err = writeJSON(filepath.Join(root, "plan.json"), plan); err != nil {
		return nil, err
	}
	if _, e := os.Stat(filepath.Join(o.Materials, "report.json")); o.Mode != "record" && e == nil {
		if err = copyFile(filepath.Join(o.Materials, "report.json"), filepath.Join(root, "source-report.json")); err != nil {
			return nil, err
		}
	}
	if _, e := os.Stat(filepath.Join(root, "source-report.json")); e == nil {
		if err = copyFile(filepath.Join(root, "source-report.json"), filepath.Join(root, "report.json")); err != nil {
			return nil, err
		}
	}
	driver, err := absoluteExecutable(o.Driver)
	if err != nil {
		return nil, err
	}
	env, err := driverEnvironment(o, plan)
	if err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(o.Output, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	stdout, err := os.OpenFile(filepath.Join(o.Output, "stdout.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	cmd := exec.Command(driver, append(append([]string{}, o.DriverArgs...), "--request", requestPath)...)
	cmd.Dir = workspace
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = log
	runErr := waitDriver(ctx, cmd)
	if service != nil {
		runErr = errors.Join(runErr, service.close())
		service = nil
	}
	var outcome result
	resultErr := readJSON(filepath.Join(root, "result.json"), &outcome)
	if resultErr == nil {
		switch outcome.Status {
		case "passed", "failed", "untriggered", "unsupported":
		default:
			resultErr = fmt.Errorf("invalid driver status %q", outcome.Status)
		}
	}
	transportErr := peers.finish(outcome.Status)
	if o.Mode == "record" {
		transportErr = errors.Join(transportErr, writeSourceManifest(root, outcome))
	}
	runErr = errors.Join(runErr, resultErr, transportErr, scrubNative(root))
	summary = object{"case": req.Case, "provider": provider, "mode": o.Mode, "result": outcome, "output": o.Output}
	if o.Mode == "verify" {
		process := object{"started": cmd.Process != nil, "reaped": cmd.ProcessState != nil}
		if cmd.ProcessState != nil {
			process["exitCode"] = cmd.ProcessState.ExitCode()
		}
		summary["driverProcess"] = process
	}
	if o.Mode != "record" {
		summary["source"] = source
		if digest, e := fileDigest(filepath.Join(root, "materialization.json")); e == nil {
			summary["materializationSHA256"] = digest
		}
	}
	if runErr != nil {
		summary["error"] = runErr.Error()
	}
	// Versions and hashes aid diagnosis; they never select or reject a runtime.
	summary["driver"] = driver
	summary["driverArgs"] = o.DriverArgs
	if b, e := os.ReadFile(filepath.Join(root, "raw/raw.jsonl")); e == nil {
		h := sha256.Sum256(b)
		summary["recordingSHA256"] = hex.EncodeToString(h[:])
	}
	if err = writeJSON(filepath.Join(o.Output, "summary.json"), summary); err != nil {
		return summary, errors.Join(runErr, err)
	}
	if runErr != nil {
		return summary, runErr
	}
	if outcome.Status != "passed" {
		return summary, fmt.Errorf("driver reported %s: %s", outcome.Status, outcome.Message)
	}
	return summary, nil
}

func driverEnvironment(o options, plan object) ([]string, error) {
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": str(plan["home"]), "TMPDIR": filepath.Join(str(plan["root"]), "tmp"), "LANG": "C.UTF-8"}
	if str(plan["provider"]) == "codex" {
		env["CODEX_HOME"] = str(plan["config"])
	} else {
		env["CLAUDE_CONFIG_DIR"] = str(plan["config"])
		env["CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"] = "1"
		env["DISABLE_AUTOUPDATER"] = "1"
	}
	if o.Mode == "record" {
		var selected map[string]string
		if err := readJSON(filepath.Join(str(plan["root"]), "native.env.private.json"), &selected); err != nil {
			return nil, err
		}
		for k, v := range selected {
			env[k] = v
		}
	}
	var out []string
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out, nil
}
