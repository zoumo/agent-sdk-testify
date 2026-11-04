package record

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/official"
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
	plan["_fixtureProject"] = plan["project"]
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
		o := official.Options{CLI: command, RuntimeEnv: run.Execution.RuntimeEnv, InitialModel: tool.Str(plan["initialModel"]), ProbeProcess: func(state string, pid int) error { return lifecycle.TrackNativeProbe(run, state, pid) }}
		if err := official.PrepareNative(ctx, &o, plan, run.OverlayRoot); err != nil {
			return err
		}
	}
	if plan["driverProfile"] == "existing-children" {
		if run.Session.HistorySeed == "" {
			return errors.New("existing-child recording requires saved history")
		}
		if err := official.PrepareHistorySeed(run.Session.HistorySeed, run.OverlayRoot, plan); err != nil {
			return err
		}
	}
	if err := tool.WriteJSON(path, plan); err != nil {
		return err
	}
	if remote == nil {
		digest, err := tool.FileDigest(path)
		if err != nil {
			return err
		}
		// This is an observed preparation receipt, separate from the sealed material.
		// It is emitted only after native isolation preparation has actually passed.
		env := tool.Object{}
		for _, name := range []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
			if value, ok := run.Execution.RuntimeEnv[name]; ok {
				env[name] = value
			}
		}
		configs := tool.Object{}
		for _, name := range []string{"config.toml", "settings.json"} {
			hash, err := tool.FileDigest(filepath.Join(run.Execution.Config, name))
			if err == nil {
				configs[name] = hash
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		receipt := tool.Object{"schemaVersion": 1, "method": "native-preparation", "policy": plan["recordingIsolation"], "sessionId": run.Session.SessionID, "executionId": run.Execution.ExecutionID, "toolSHA256": run.Session.ToolSHA256, "preparedPlanSHA256": digest, "nativeSHA256": plan["cliSHA256"], "args": plan["args"], "cwd": run.Execution.Workspace, "environment": env, "configurationSHA256": configs}
		if err := tool.WriteJSON(filepath.Join(run.Dir, "receipts", "isolation.json"), receipt); err != nil {
			return err
		}
	}
	run.Plan = plan
	return nil
}
