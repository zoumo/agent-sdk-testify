package lifecycle

import (
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// SaveNativePreparation binds observed native preparation to its ordinary execution.
func SaveNativePreparation(fixtureDir string, fixture *Fixture, execution Execution, runtimePlan tool.Object, planPath, materialPlanPath string) error {
	runtimeDigest, err := tool.FileDigest(planPath)
	if err != nil {
		return err
	}
	materialDigest, err := tool.FileDigest(materialPlanPath)
	if err != nil {
		return err
	}
	executionReference, err := FixtureReference(fixtureDir, filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "execution.json"))
	if err != nil {
		return err
	}
	preparedReference, err := FixtureReference(fixtureDir, planPath)
	if err != nil {
		return err
	}
	materialReference, err := FixtureReference(fixtureDir, materialPlanPath)
	if err != nil {
		return err
	}
	executionDigest, err := tool.FileDigest(filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "execution.json"))
	if err != nil {
		return err
	}
	// This is an observed preparation receipt, separate from the sealed material.
	// It is emitted only after native isolation preparation has actually passed.
	env := tool.Object{}
	for _, name := range []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		if value, ok := execution.RuntimeEnv[name]; ok {
			env[name] = value
		}
	}
	configs := tool.Object{}
	for _, name := range []string{"config.toml", "settings.json"} {
		hash, err := tool.FileDigest(filepath.Join(execution.Config, name))
		if err == nil {
			configs[name] = hash
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	receipt := tool.Object{
		"schemaVersion": 2,
		"method":        "native-preparation",
		"policy":        runtimePlan["recordingIsolation"],
		"fixtureId":     fixture.FixtureID,
		"executionId":   execution.ExecutionID,
		"execution":     tool.Object{"path": executionReference, "sha256": executionDigest},
		"preparedPlan":  tool.Object{"path": preparedReference, "sha256": runtimeDigest},
		"materialPlan":  tool.Object{"path": materialReference, "sha256": materialDigest},
		"native":        tool.Object{"sha256": runtimePlan["cliSHA256"], "args": runtimePlan["args"]},
		"cwd":           execution.Workspace,
		"environment":   env,
		"configuration": configs,
	}
	fixture.Tool.bindSHA256(receipt)
	if err := tool.WriteJSON(filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "receipts", "isolation.json"), receipt); err != nil {
		return err
	}
	return nil
}

// RebindNativePreparationExecution points the observed preparation at the
// settled execution descriptor. Finalization adds artifact and connection
// bindings to that descriptor after native preparation has already passed.
func RebindNativePreparationExecution(fixtureDir string, execution Execution) error {
	path := filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "receipts", "isolation.json")
	var receipt tool.Object
	if err := tool.ReadJSON(path, &receipt); err != nil {
		return err
	}
	if receipt["schemaVersion"] != float64(2) {
		return nil
	}
	executionPath := filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "execution.json")
	reference, err := FixtureReference(fixtureDir, executionPath)
	if err != nil {
		return err
	}
	digest, err := tool.FileDigest(executionPath)
	if err != nil {
		return err
	}
	receipt["execution"] = tool.Object{"path": reference, "sha256": digest}
	return tool.WriteJSON(path, receipt)
}
