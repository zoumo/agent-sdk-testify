package lifecycle

import (
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// SaveNativePreparation binds observed native preparation to its ordinary execution.
func SaveNativePreparation(fixtureDir string, fixture *Fixture, execution Execution, runtimePlan tool.Object, planPath, materialPlanPath string) error {
	dir := ExecutionDir(fixtureDir, execution.ExecutionID)
	prepared := tool.Object{"schemaVersion": 3, "fixtureId": fixture.FixtureID, "executionId": execution.ExecutionID, "sourcePlanSHA256": fixture.Source.SHA256}
	for _, name := range []string{"project", "_fixtureProject", "root", "home", "config", "tmp", "provider", "caseId", "storyId", "driverProfile", "model", "reasoningEffort", "cliSHA256", "args", "recordingIsolation", "stdinEOFAction", "limits"} {
		if value, ok := runtimePlan[name]; ok {
			prepared[name] = value
		}
	}
	prepared["tmp"] = execution.Tmp
	preparedPath := filepath.Join(dir, "prepared-plan.json")
	if err := tool.WriteJSON(preparedPath, prepared); err != nil {
		return err
	}
	runtimeDigest, err := tool.FileDigest(preparedPath)
	if err != nil {
		return err
	}
	preparedReference, err := PackageReference(fixtureDir, execution.Artifact.ID, preparedPath)
	if err != nil {
		return err
	}
	executionReference, err := PackageReference(fixtureDir, execution.Artifact.ID, filepath.Join(dir, "execution.json"))
	if err != nil {
		return err
	}
	executionDigest, err := tool.FileDigest(filepath.Join(dir, "execution.json"))
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
		"schemaVersion":    3,
		"method":           "native-preparation",
		"policy":           runtimePlan["recordingIsolation"],
		"fixtureId":        fixture.FixtureID,
		"executionId":      execution.ExecutionID,
		"execution":        tool.Object{"path": executionReference, "sha256": executionDigest},
		"preparedPlan":     tool.Object{"path": preparedReference, "sha256": runtimeDigest},
		"sourcePlanSHA256": fixture.Source.SHA256,
		"native":           tool.Object{"sha256": runtimePlan["cliSHA256"], "args": runtimePlan["args"]},
		"cwd":              execution.Workspace,
		"environment":      env,
		"configuration":    configs,
	}
	fixture.Tool.bindSHA256(receipt)
	if err := tool.WriteJSON(filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "isolation.json"), receipt); err != nil {
		return err
	}
	return nil
}

// RebindNativePreparationExecution adds final core hashes before the ordinary
// execution inventory seals. The declaration itself remains immutable.
func RebindNativePreparationExecution(fixtureDir string, execution Execution) error {
	path := filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "isolation.json")
	var receipt tool.Object
	if err := tool.ReadJSON(path, &receipt); err != nil {
		return err
	}
	if receipt["schemaVersion"] != float64(3) {
		return nil
	}
	receipt["materialId"] = execution.Artifact.ID
	if execution.Artifact.ManifestSHA256 != "" {
		receipt["sourcePlanSHA256"], receipt["planSHA256"], receipt["manifestSHA256"] = execution.Artifact.SourcePlanSHA256, execution.Artifact.PlanSHA256, execution.Artifact.ManifestSHA256
	}
	return tool.WriteJSON(path, receipt)
}
