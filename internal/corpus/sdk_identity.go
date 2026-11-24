package corpus

import (
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const claudeTypeScriptSDK = "@anthropic-ai/claude-agent-sdk"

// RecordedSDKIdentity validates SDK identities in persisted capture, replay,
// and publication evidence.
func RecordedSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if provider == "claude" && sdk["package"] == claudeTypeScriptSDK {
		return sdk["version"] == "0.3.293"
	}
	if provider == "claude" && caseID == "SDK-CASE-042" {
		return false
	}
	name := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider]
	return name != "" && sdk["package"] == name && tool.Str(sdk["version"]) != ""
}

// DriverSDKIdentity validates a new official invocation's business report.
// The Python entry point remains usable during side-by-side qualification.
func DriverSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	return RecordedSDKIdentity(provider, caseID, sdk)
}

// SDKOutcomeMatches binds the TypeScript driver's result to its business report.
// Python and Codex evidence retain their original SDK report contract.
func SDKOutcomeMatches(sdk tool.Object, outcome tool.Object) bool {
	return sdk["package"] != claudeTypeScriptSDK || reflect.DeepEqual(sdk, tool.Obj(outcome["sdk"]))
}
