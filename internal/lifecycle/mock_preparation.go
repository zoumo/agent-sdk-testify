package lifecycle

import (
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// saveMockPreparation records the actual overlay prepared by the ordinary mock
// allocation path. Both official and consumer mock use this same writer.
func saveMockPreparation(fixtureDir string, fixture *Fixture, execution Execution, plan tool.Object) error {
	material := execution.Artifact
	dir := PackageExecutionRoot(fixtureDir, material.ID, execution.ExecutionID)
	executionReference := "executions/" + execution.ExecutionID + "/execution.json"
	executionDigest, err := tool.FileDigest(filepath.Join(dir, "execution.json"))
	if err != nil {
		return err
	}

	env := tool.Object{}
	for _, name := range []string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		if value, ok := execution.RuntimeEnv[name]; ok {
			env[name] = value
		}
	}
	configuration := tool.Object{}
	for _, name := range []string{"config.toml", "settings.json"} {
		hash, err := tool.FileDigest(filepath.Join(execution.Config, name))
		if err == nil {
			configuration[name] = hash
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	connections := []any{}
	for _, connection := range execution.Connections {
		connections = append(connections, tool.Object{"connectionId": connection.ID, "executionId": execution.ExecutionID, "executionSHA256": executionDigest})
	}
	prepared := tool.Object{"schemaVersion": 3, "fixtureId": fixture.FixtureID, "executionId": execution.ExecutionID, "materialId": material.ID, "sourcePlanSHA256": material.SourcePlanSHA256, "planSHA256": material.PlanSHA256, "manifestSHA256": material.ManifestSHA256, "provider": fixture.AgentType, "command": "mock", "project": execution.ProtocolCwd, "root": filepath.Dir(execution.Workspace), "home": execution.Home, "config": execution.Config, "tmp": execution.Tmp, "args": execution.NativeArgs, "limits": execution.Limits}
	for _, name := range []string{"caseId", "storyId", "driverProfile", "model", "reasoningEffort"} {
		if value, ok := plan[name]; ok {
			prepared[name] = value
		}
	}
	preparedPath := filepath.Join(dir, "prepared-plan.json")
	if err := tool.WriteJSON(preparedPath, prepared); err != nil {
		return err
	}
	preparedHash, err := tool.FileDigest(preparedPath)
	if err != nil {
		return err
	}
	isolation := tool.Object{
		"schemaVersion": 3, "fixtureId": fixture.FixtureID, "executionId": execution.ExecutionID, "materialId": material.ID,
		"method": "mock-preparation", "command": "mock", "nativeArgs": execution.NativeArgs,
		"execution":    tool.Object{"path": executionReference, "sha256": executionDigest},
		"preparedPlan": tool.Object{"path": "executions/" + execution.ExecutionID + "/prepared-plan.json", "sha256": preparedHash},
		"workspace":    execution.Workspace, "protocolCwd": execution.ProtocolCwd, "home": execution.Home, "config": execution.Config, "tmp": execution.Tmp,
		"environment": env, "configuration": configuration, "connections": connections,
	}
	return tool.WriteJSON(filepath.Join(dir, "isolation.json"), isolation)
}
