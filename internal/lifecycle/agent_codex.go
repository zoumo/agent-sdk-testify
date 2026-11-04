package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

var codexAgentProfile = agentProfile{
	binding:   "bindings/codex.json",
	configKey: "CODEX_HOME",
	authFiles: []string{"auth.json"},
	environment: func(root string) map[string]string {
		return map[string]string{"CODEX_HOME": filepath.Join(root, "config")}
	},
	writeConfig: func(root string, plan tool.Object) error {
		config, err := resetMockConfig(root)
		if err != nil {
			return err
		}
		line := ""
		if model := planModel(plan); model != "" {
			line = "model = " + strconv.Quote(model) + "\n"
		}
		return os.WriteFile(filepath.Join(config, "config.toml"), []byte(line), 0o600)
	},
	protocolCWD: func(plan tool.Object, workspace string) string {
		if project := tool.Str(plan["project"]); project != "" {
			return project
		}
		return workspace
	},
}
