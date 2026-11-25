package lifecycle

import (
	"path/filepath"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

var piAgentProfile = agentProfile{
	binding:       "bindings/pi.json",
	configKey:     "PI_CODING_AGENT_DIR",
	authFiles:     []string{"auth.json", "models.json"},
	copyAuthFiles: true,
	environment: func(root string) map[string]string {
		return map[string]string{"PI_CODING_AGENT_DIR": filepath.Join(root, "config"), "PI_OFFLINE": "1"}
	},
	writeConfig: func(root string, _ tool.Object) error {
		_, err := resetMockConfig(root)
		return err
	},
	protocolCWD: func(_ tool.Object, workspace string) string { return workspace },
}
