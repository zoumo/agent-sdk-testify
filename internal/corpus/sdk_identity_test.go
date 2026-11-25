package corpus

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestSDKIdentityAfterClaudeCutover(t *testing.T) {
	for _, testcase := range []struct {
		name, provider, caseID, pkg, version string
		recorded, driver                     bool
	}{
		{"legacy Claude", "claude", "SDK-CASE-037", "claude-agent-sdk", "0.2.174", true, false},
		{"Claude interaction", "claude", "SDK-CASE-037", "@anthropic-ai/claude-agent-sdk", "0.3.293", true, true},
		{"Claude history", "claude", "SDK-CASE-035", "@anthropic-ai/claude-agent-sdk", "0.3.293", true, true},
		{"wrong interaction TS pin", "claude", "SDK-CASE-037", "@anthropic-ai/claude-agent-sdk", "0.3.322", false, false},
		{"Claude elicitation", "claude", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "0.3.293", true, true},
		{"wrong TS pin", "claude", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "0.3.322", false, false},
		{"wrong Claude package for elicitation", "claude", "SDK-CASE-042", "claude-agent-sdk", "0.2.174", false, false},
		{"missing TS version", "claude", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "", false, false},
		{"unknown package", "claude", "SDK-CASE-042", "another-sdk", "0.3.293", false, false},
		{"Pi stock history", "pi", "SDK-CASE-033", "@earendil-works/pi-coding-agent", "1.6.0", true, true},
		{"Pi wrong SDK version", "pi", "SDK-CASE-033", "@earendil-works/pi-coding-agent", "1.5.0", false, false},
		{"Pi UI reply unavailable in RpcClient", "pi", "SDK-CASE-008", "@earendil-works/pi-coding-agent", "1.6.0", false, false},
		{"Codex", "codex", "SDK-CASE-001", "openai-codex", "0.0.0.dev0", false, false},
		{"Claude package submitted by Codex", "codex", "SDK-CASE-042", "@anthropic-ai/claude-agent-sdk", "0.3.293", false, false},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			sdk := tool.Object{"package": testcase.pkg, "version": testcase.version}
			require.Equal(t, testcase.recorded, RecordedSDKIdentity(testcase.provider, testcase.caseID, sdk))
			require.Equal(t, testcase.driver, DriverSDKIdentity(testcase.provider, testcase.caseID, sdk))
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

func TestCodexRustIdentity(t *testing.T) {
	sdk := tool.Object{"package": "codex-app-server-remote-client", "version": "0.0.0", "revision": "108730843ae4303ae79e9b6b936343f0b7c483bb"}
	wrongRevision := tool.Object{"package": codexRustSDK, "version": "0.0.0", "revision": "different"}
	python := tool.Object{"package": "openai-codex", "version": "0.0.0.dev0"}
	for n := 1; n <= 42; n++ {
		caseID := fmt.Sprintf("SDK-CASE-%03d", n)
		qualified := n != 3 && n != 25 && n != 26
		require.Equal(t, qualified, RecordedSDKIdentity("codex", caseID, sdk), caseID)
		require.Equal(t, qualified, DriverSDKIdentity("codex", caseID, sdk), caseID)
	}
	require.False(t, RecordedSDKIdentity("codex", "SDK-CASE-043", sdk))
	require.False(t, RecordedSDKIdentity("claude", "SDK-CASE-041", sdk))
	for _, invalid := range []tool.Object{
		wrongRevision,
		{"package": codexRustSDK, "version": "0.0.0"},
		{"package": codexRustSDK, "version": "1.0.0", "revision": codexRustSDKRevision},
		{"package": codexRustSDK, "version": "0.0.0", "revision": codexRustSDKRevision, "extra": true},
	} {
		require.False(t, RecordedSDKIdentity("codex", "SDK-CASE-001", invalid))
		require.False(t, DriverSDKIdentity("codex", "SDK-CASE-001", invalid))
	}
	require.True(t, SDKOutcomeMatches(sdk, tool.Object{"sdk": sdk}))
	require.False(t, SDKOutcomeMatches(sdk, tool.Object{}))
	require.False(t, SDKOutcomeMatches(sdk, tool.Object{"sdk": wrongRevision}))
	require.False(t, SDKOutcomeMatches(python, tool.Object{"sdk": sdk}))
	require.False(t, SDKOutcomeMatches(python, tool.Object{}))
}
