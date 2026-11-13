package record

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/native"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func prepareOfficialCapture(ctx context.Context, run lifecycle.RecordContext, command string, args []string, remote *capture.ConnectOptions, trackProbe lifecycle.RecordProbeTracker) (tool.Object, error) {
	path := filepath.Join(run.OverlayRoot(), "record-plan.json")
	var plan tool.Object
	if err := tool.ReadJSON(path, &plan); err == nil {
		if corpus.SelfSeededChildren(plan) && run.Connection() == "probe" {
			if err := prepareChildObservation(run, plan); err != nil {
				return nil, err
			}
		}
		materialPlanPath := filepath.Join(run.OverlayRoot(), "material-plan.json")
		if _, statErr := os.Stat(materialPlanPath); os.IsNotExist(statErr) {
			if err := tool.WriteJSON(materialPlanPath, corpus.MaterialPlanFromRuntime(plan)); err != nil {
				return nil, err
			}
		} else if statErr != nil {
			return nil, statErr
		}
		return plan, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	fixture := run.Fixture()
	plan, err := lifecycle.LoadPlan(&fixture)
	if err != nil {
		return nil, err
	}
	execution := run.Execution()
	sourcePlanDigest, err := recordSourcePlanDigest(execution)
	if err != nil {
		return nil, err
	}
	// Keep native request cwd stable across SDK and protocol subprocesses. Mock
	// will replay this precise value while placing writable fixtures elsewhere.
	delete(plan, "recordingIsolation")
	if tool.Str(plan["_fixtureProject"]) == "" {
		if project := tool.Str(plan["project"]); project != "" {
			plan["_fixtureProject"] = project
		}
	}
	plan["project"] = fixture.Routes["record"].Workspace
	plan["root"], plan["home"], plan["config"], plan["raw"] = run.OverlayRoot(), execution.Home, execution.Config, filepath.Join(run.ArtifactRoot(), "raw")
	plan["stdinEOFAction"] = "close"
	if fixture.AgentType == "claude" && plan["driverProfile"] == "interrupt" {
		plan["stdinEOFAction"] = "interrupt"
	}
	if remote != nil {
		for _, key := range []string{"cli", "cliVersion", "cliSHA256", "args"} {
			delete(plan, key)
		}
	}
	if remote == nil {
		plan["cli"], plan["args"] = command, args
		o := native.Options{CLI: command, RuntimeEnv: execution.RuntimeEnv, InitialModel: tool.Str(plan["initialModel"]), ProbeProcess: trackProbe}
		if err := native.PrepareNative(ctx, &o, plan, run.OverlayRoot()); err != nil {
			return nil, err
		}
	}
	if plan["driverProfile"] == "existing-children" && plan["historySeed"] == nil && !corpus.SelfSeededChildren(plan) {
		if fixture.HistorySeed == "" {
			return nil, errors.New("existing-child recording requires saved history")
		}
		if err := native.PrepareHistorySeed(fixture.HistorySeed, run.OverlayRoot(), plan); err != nil {
			return nil, err
		}
	}
	plan["sourcePlanSHA256"] = sourcePlanDigest
	if err := tool.WriteJSON(path, plan); err != nil {
		return nil, err
	}
	materialPlanPath := filepath.Join(run.OverlayRoot(), "material-plan.json")
	if err := tool.WriteJSON(materialPlanPath, corpus.MaterialPlanFromRuntime(plan)); err != nil {
		return nil, err
	}
	if remote == nil {
		if err := lifecycle.SaveNativePreparation(run.FixtureDir(), &fixture, execution, plan, path, materialPlanPath); err != nil {
			return nil, err
		}
	}

	return plan, nil
}

func recordSourcePlanDigest(execution lifecycle.Execution) (string, error) {
	path := execution.Source.Path

	return tool.FileDigest(path)
}
