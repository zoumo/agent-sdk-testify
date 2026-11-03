package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

var claudeAgentProfile = agentProfile{
	binding:   "bindings/claude.json",
	configKey: "CLAUDE_CONFIG_DIR",
	authFiles: []string{".credentials.json"},
	environment: func(root string) map[string]string {
		return map[string]string{
			"CLAUDE_CONFIG_DIR":                     filepath.Join(root, "config"),
			"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS": "1",
			"DISABLE_AUTOUPDATER":                   "1",
		}
	},
	writeConfig: func(root string, plan tool.Object) error {
		config, err := resetMockConfig(root)
		if err != nil {
			return err
		}
		data, err := json.Marshal(map[string]any{"model": planModel(plan)})
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(config, "settings.json"), data, 0o600)
	},
	protocolCWD: func(_ tool.Object, workspace string) string {
		return workspace
	},
	matchesNative: func(native tool.Object) bool {
		return nativeMatchesName(native, "claude")
	},
}
