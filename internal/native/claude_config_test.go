package native

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

func TestClaudeCasePromptIdentifiesNativeWorkspace(t *testing.T) {
	args, err := claudeNativeArgs("fixture-tools", tool.Object{})
	require.NoError(t, err)
	args, err = prepareClaudeArgs(tool.Object{"project": "/protocol/identity", "_fixtureProject": "/isolated/workspace"}, args, "fixture-tools")
	require.NoError(t, err)
	for i, arg := range args {
		if arg == "--system-prompt" {
			require.Contains(t, args[i+1], "/isolated/workspace")
			require.NotContains(t, args[i+1], "/protocol/identity")
			return
		}
	}
	t.Fatal("missing case system prompt")
}

func TestClaudeSelectedEndpointCredentials(t *testing.T) {
	root := t.TempDir()
	env := map[string]string{"ANTHROPIC_BASE_URL": "other-endpoint", "ANTHROPIC_AUTH_TOKEN": "other-token"}
	settings := filepath.Join(root, "settings.json")
	require.NoError(t, tool.WriteJSON(settings, tool.Object{"env": tool.Object{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}}))
	claudeAuthEnvironment(settings, env)
	require.Equal(t, map[string]string{"ANTHROPIC_BASE_URL": "selected-endpoint", "ANTHROPIC_API_KEY": "selected-key"}, env)
}

func TestClaudeEffortUsesConfiguredCreationValue(t *testing.T) {
	args, err := claudeNativeArgs("structured-input", tool.Object{
		"setting": "effort", "configurationTiming": "between-inputs", "effort": "low",
	})
	require.NoError(t, err)
	require.Contains(t, args, "--effort")
	require.Equal(t, "low", args[len(args)-1])
	require.NotContains(t, args, "--max-thinking-tokens")

	_, err = claudeNativeArgs("structured-input", tool.Object{
		"setting": "effort", "configurationTiming": "between-inputs",
	})
	require.ErrorContains(t, err, "initial effort")
}
