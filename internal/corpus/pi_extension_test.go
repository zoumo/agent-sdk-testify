package corpus

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestPiExtensionIdentity(t *testing.T) {
	for _, caseID := range []string{"SDK-CASE-012", "SDK-CASE-038"} {
		t.Run(caseID, func(t *testing.T) {
			sdk := PiExtensionIdentity()
			require.True(t, RecordedSDKIdentity("pi", caseID, sdk))
			require.True(t, DriverSDKIdentity("pi", caseID, sdk))
			require.True(t, SDKOutcomeMatches(sdk, tool.Object{"sdk": PiExtensionIdentity()}))
			require.False(t, DriverSDKIdentity("pi", "SDK-CASE-001", sdk))
			require.False(t, DriverSDKIdentity("pi", caseID, tool.Object{"package": piPackage, "version": PiVersion}))
			for _, field := range []string{"version", "client", "extensionRevision", "extensionSHA256"} {
				invalid := PiExtensionIdentity()
				invalid[field] = "different"
				require.False(t, RecordedSDKIdentity("pi", caseID, invalid), field)
				require.False(t, SDKOutcomeMatches(sdk, tool.Object{"sdk": invalid}), field)
			}
		})
	}
}

func TestPiExtensionPlan(t *testing.T) {
	privatePath := filepath.Join(t.TempDir(), PiExtensionDirectory, PiExtensionFilename)
	newPlan := func() tool.Object {
		return tool.Object{
			"provider": "pi", "caseId": "SDK-CASE-012", "cliVersion": PiVersion,
			"piExtension": PiExtensionIdentity(), "args": []string{"--mode", "rpc", "--no-extensions", "-e", privatePath},
		}
	}
	require.NoError(t, ValidatePiExtensionPlan(newPlan()))
	for _, example := range []struct {
		name, field string
		value       any
	}{
		{"runtime mismatch", "cliVersion", "1.5.0"},
		{"missing extension identity", "piExtension", nil},
		{"missing launch arguments", "args", []string{"--mode", "rpc"}},
		{"unexpected case identity", "caseId", "SDK-CASE-001"},
		{"unexpected provider identity", "provider", "claude"},
		{"different launch path", "args", []string{"--no-extensions", "-e", filepath.Dir(privatePath)}},
	} {
		t.Run(example.name, func(t *testing.T) {
			plan := newPlan()
			plan[example.field] = example.value
			require.Error(t, ValidatePiExtensionPlan(plan))
		})
	}
}
