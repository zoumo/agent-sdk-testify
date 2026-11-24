package corpus

import "github.com/zoumo/agent-sdk-testify/internal/tool"

// RecordedSDKIdentity validates SDK identities in persisted capture, replay,
// and publication evidence.
func RecordedSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if provider == "claude" && caseID == "SDK-CASE-042" {
		return sdk["package"] == "@anthropic-ai/claude-agent-sdk" && sdk["version"] == "0.3.293"
	}
	name := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider]
	return name != "" && sdk["package"] == name && tool.Str(sdk["version"]) != ""
}

// DriverSDKIdentity validates a new official invocation's business report.
// During side-by-side qualification, both entry points use the same SDK for
// case 042. The allowed identities still match the recorded identities.
func DriverSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	return RecordedSDKIdentity(provider, caseID, sdk)
}
