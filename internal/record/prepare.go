package record

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func prepareOfficialCapture(ctx context.Context, run *lifecycle.ActionRun, command string, args []string, remote *capture.ConnectOptions) error {
	lock, err := lifecycle.RecordFinalizationLock(run)
	if err != nil {
		return err
	}
	defer lock.Close()
	path := filepath.Join(run.OverlayRoot, "record-plan.json")
	var plan tool.Object
	if err := tool.ReadJSON(path, &plan); err == nil {
		run.Plan = plan
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	plan, err = lifecycle.LoadPlan(run.Session)
	if err != nil {
		return err
	}
	// Keep native request cwd stable across SDK and protocol subprocesses. Mock
	// will replay this precise value while placing writable fixtures elsewhere.
	delete(plan, "recordingIsolation")
	plan["project"] = run.Session.Routes["record"].Workspace
	plan["_fixtureProject"] = run.Execution.Workspace
	plan["root"], plan["home"], plan["config"], plan["raw"] = run.OverlayRoot, run.Execution.Home, run.Execution.Config, filepath.Join(run.ArtifactRoot, "raw")
	plan["stdinEOFAction"] = "close"
	if run.Session.AgentType == "claude" && plan["driverProfile"] == "interrupt" {
		plan["stdinEOFAction"] = "interrupt"
	}
	if remote != nil {
		for _, key := range []string{"cli", "cliVersion", "cliSHA256", "args"} {
			delete(plan, key)
		}
	}
	if remote == nil {
		plan["cli"], plan["args"] = command, args
		o := native.Options{CLI: command, RuntimeEnv: run.Execution.RuntimeEnv, InitialModel: tool.Str(plan["initialModel"]), ProbeProcess: func(state string, pid int) error { return lifecycle.TrackNativeProbe(run, state, pid) }}
		if err := native.PrepareNative(ctx, &o, plan, run.OverlayRoot); err != nil {
			return err
		}
	}
	if plan["driverProfile"] == "existing-children" && plan["historySeed"] == nil {
		if run.Session.HistorySeed == "" {
			return errors.New("existing-child recording requires saved history")
		}
		if err := native.PrepareHistorySeed(run.Session.HistorySeed, run.OverlayRoot, plan); err != nil {
			return err
		}
	}
	if err := tool.WriteJSON(path, plan); err != nil {
		return err
	}
	if remote == nil {
		if err := lifecycle.SaveNativePreparation(filepath.Dir(filepath.Dir(run.Dir)), run.Session, run.Execution, plan, path); err != nil {
			return err
		}
	}

	run.Plan = plan
	return nil
}
