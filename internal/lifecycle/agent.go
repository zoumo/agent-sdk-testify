package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type agentProfile struct {
	binding       string
	configKey     string
	authFiles     []string
	environment   func(root string) map[string]string
	writeConfig   func(root string, plan tool.Object) error
	protocolCWD   func(plan tool.Object, workspace string) string
	matchesNative func(native tool.Object) bool
}

var agentProfiles = map[string]agentProfile{
	"codex":  codexAgentProfile,
	"claude": claudeAgentProfile,
}

func agentProfileFor(agentType string) (agentProfile, error) {
	profile, ok := agentProfiles[agentType]
	if !ok {
		return agentProfile{}, fmt.Errorf("unsupported agent type %q", agentType)
	}
	return profile, nil
}

func agentTypes() []string {
	types := make([]string, 0, len(agentProfiles))
	for name := range agentProfiles {
		types = append(types, name)
	}
	slices.Sort(types)
	return types
}

func nativeMatchesName(native tool.Object, names ...string) bool {
	executable := strings.ToLower(filepath.Base(tool.Str(native["executable"])))
	for _, name := range names {
		if strings.Contains(executable, name) {
			return true
		}
	}
	return false
}

func resetMockConfig(root string) (string, error) {
	config := filepath.Join(root, "config")
	if err := os.RemoveAll(config); err != nil {
		return "", err
	}
	if err := os.MkdirAll(config, 0o700); err != nil {
		return "", err
	}
	return config, nil
}
