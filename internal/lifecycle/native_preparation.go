package lifecycle

import (
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// SaveNativePreparation binds observed native preparation to its ordinary execution.
func SaveNativePreparation(fixtureDir string, fixture *Fixture, execution Execution, plan tool.Object, path string) error {
	digest, err := tool.FileDigest(path)
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
	receipt := tool.Object{"schemaVersion": 1, "method": "native-preparation", "policy": plan["recordingIsolation"], "fixtureId": fixture.FixtureID, "executionId": execution.ExecutionID, "preparedPlanSHA256": digest, "nativeSHA256": plan["cliSHA256"], "args": plan["args"], "cwd": execution.Workspace, "environment": env, "configurationSHA256": configs}
	fixture.Tool.bindSHA256(receipt)
	if err := tool.WriteJSON(filepath.Join(ExecutionDir(fixtureDir, execution.ExecutionID), "receipts", "isolation.json"), receipt); err != nil {
		return err
	}
	return nil
}
