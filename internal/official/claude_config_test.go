package official

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestClaudeInitialModelIsDistinctFromChangeTarget(t *testing.T) {
	plan := tool.Object{"model": "target", "initialModel": "initial"}
	args, err := prepareClaudeArgs(plan, nil, "structured-input")
	require.NoError(t, err)
	require.Equal(t, []string{"--model", "initial"}, args[len(args)-2:])
	require.Equal(t, "target", plan["model"])
	delete(plan, "initialModel")
	args, err = prepareClaudeArgs(plan, nil, "structured-input")
	require.NoError(t, err)
	require.Equal(t, []string{"--model", "target"}, args[len(args)-2:])
}

func TestClaudeSelectedEndpointCredentials(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"ANTHROPIC_BASE_URL": "other-endpoint", "ANTHROPIC_AUTH_TOKEN": "other-token"}
	settings := filepath.Join(root, "settings.json")
	require.NoError(t, tool.WriteJSON(settings, tool.Object{"env": tool.Object{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}}))
	claudeAuthEnvironment(settings, env)
	require.Equal(t, map[string]string{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}, env)
}
