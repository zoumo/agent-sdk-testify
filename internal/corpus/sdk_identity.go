package corpus

import "github.com/zoumo/agent-sdk-testify/internal/tool"

// OfficialSDKIdentity validates the SDK package used by the official case driver.
func OfficialSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if provider == "claude" && caseID == "SDK-CASE-042" {
		return sdk["package"] == "@anthropic-ai/claude-agent-sdk" && sdk["version"] == "0.3.293"
	}
	if provider == "pi" {
		return caseID == "SDK-CASE-001" && sdk["package"] == "@earendil-works/pi-coding-agent" && sdk["version"] == "1.6.0"
	}
	name := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider]
	return name != "" && sdk["package"] == name && tool.Str(sdk["version"]) != ""
}
