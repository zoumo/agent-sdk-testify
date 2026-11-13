package corpus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestChildObservationArgsRequireOwnSetupIdentity(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			parent := "12345678-1234-4234-8234-123456789012"
			plan := tool.Object{
				"provider": provider, "driverProfile": "existing-children",
				"scenario":              tool.Object{"parameters": tool.Object{"selfSeed": true}},
				"args":                  []string{"native-protocol"},
				"existingChildrenSetup": tool.Object{"schemaVersion": float64(1), "provider": provider, "sessionId": parent},
			}
			require.True(t, SelfSeededChildren(plan))
			args, err := ChildObservationArgs(plan)
			require.NoError(t, err)
			expected := []string{"native-protocol"}
			if provider == "claude" {
				expected = append(expected, "--resume", parent)
			}
			require.Equal(t, expected, args)
			require.Equal(t, []string{"native-protocol"}, plan["args"])
			for _, field := range []string{"schemaVersion", "provider", "sessionId"} {
				setup := tool.Obj(plan["existingChildrenSetup"])
				original := setup[field]
				setup[field] = "invalid"
				_, err := ChildObservationArgs(plan)
				require.Error(t, err)
				setup[field] = original
			}
			plan["historySeed"] = tool.Object{"sessionId": "legacy"}
			require.False(t, SelfSeededChildren(plan))
		})
	}
}
