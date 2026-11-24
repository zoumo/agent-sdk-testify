package corpus

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestSDKIdentityDuringClaudeSideBySideQualification(t *testing.T) {
	for _, testcase := range []struct {
		name, provider, caseID, pkg, version string
		valid                                bool
	}{
		{"legacy Claude", "claude", "SDK-CASE-037", "claude-agent-sdk", "0.2.174", true},
		{"Claude interaction", "claude", "SDK-CASE-037", "@anthropic-ai/claude-agent-sdk", "0.3.293", true},
		{"Claude history", "claude", "SDK-CASE-035", "@anthropic-ai/claude-agent-sdk", "0.3.293", true},
		{"wrong interaction TS pin", "claude", "SDK-CASE-037", "@anthropic-ai/claude-agent-sdk", "0.3.322", false},
		{"Claude elicitation", "claude", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "0.3.293", true},
		{"wrong TS pin", "claude", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "0.3.322", false},
		{"wrong Claude package for elicitation", "claude", "SDK-CASE-042", "claude-agent-sdk", "0.2.174", false},
		{"missing TS version", "claude", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "", false},
		{"unknown package", "claude", "SDK-CASE-042", "another-sdk", "0.3.293", false},
		{"Codex", "codex", "SDK-CASE-001", "openai-codex", "0.0.0.dev0", true},
		{"Claude package submitted by Codex", "codex", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "0.3.293", false},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			sdk := tool.Object{"package": testcase.pkg, "version": testcase.version}
			require.Equal(t, testcase.valid, RecordedSDKIdentity(testcase.provider, testcase.caseID, sdk))
			require.Equal(t, testcase.valid, DriverSDKIdentity(testcase.provider, testcase.caseID, sdk))
		})
	}
}

func TestSDKOutcomeMatches(t *testing.T) {
	sdk := tool.Object{"package": claudeTypeScriptSDK, "version": "0.3.293"}
	require.True(t, SDKOutcomeMatches(sdk, tool.Object{"sdk": sdk}))
	require.False(t, SDKOutcomeMatches(sdk, tool.Object{}))
	require.False(t, SDKOutcomeMatches(sdk, tool.Object{"sdk": tool.Object{"package": claudeTypeScriptSDK, "version": "different"}}))
	require.True(t, SDKOutcomeMatches(tool.Object{"package": "claude-agent-sdk"}, tool.Object{}))
}
