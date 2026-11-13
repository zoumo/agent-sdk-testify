package corpus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestMaterialPlanFromRuntimeKeepsRecordedIdentities(t *testing.T) {
	runtime := tool.Object{
		"root": "/attempt/root", "home": "/attempt/home", "config": "/attempt/config",
		"raw": "/attempt/raw", "tmp": "/attempt/tmp", "output": "/attempt/root",
		"_seedRoot": "/attempt/seed", "_recordedWorkspace": "/attempt/recorded",
		"_fixtureProject": "/material/workspace", "_replay": true,
		"_agentResults":       tool.Object{"main": "/attempt/result.json"},
		"_executionWorkspace": "/attempt/workspace", "cli": "/attempt/cli",
		"caseId": "SDK-CASE-001", "provider": "codex", "project": "/material/workspace",
		"historyProject": "/material/history", "args": []any{"exec"}, "cliSHA256": "a",
		"historySeed": tool.Object{"sessionId": "parent"}, "sourcePlanSHA256": "b",
	}
	plan := MaterialPlanFromRuntime(runtime)
	for _, key := range []string{"root", "home", "config", "raw", "tmp", "output", "_seedRoot", "_recordedWorkspace", "_replay", "_agentResults", "_executionWorkspace", "cli"} {
		require.NotContains(t, plan, key)
	}
	require.Equal(t, runtime["caseId"], plan["caseId"])
	require.Equal(t, runtime["project"], plan["project"])
	require.Equal(t, runtime["historyProject"], plan["historyProject"])
	require.Equal(t, runtime["_fixtureProject"], plan["_fixtureProject"])
	require.Equal(t, runtime["historySeed"], plan["historySeed"])
	require.Equal(t, runtime["sourcePlanSHA256"], plan["sourcePlanSHA256"])
	require.Equal(t, runtime["cliSHA256"], plan["cliSHA256"])
	require.Equal(t, runtime["args"], plan["args"])
	// The projection is a copy; stripping keys must not mutate the runtime
	// configuration that the current attempt still needs.
	require.Equal(t, "/attempt/root", runtime["root"])
	require.Equal(t, "/attempt/cli", runtime["cli"])
}
