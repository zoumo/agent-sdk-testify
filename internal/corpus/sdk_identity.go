package corpus

import (
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const (
	claudeTypeScriptSDK        = "@anthropic-ai/claude-agent-sdk"
	claudeTypeScriptSDKVersion = "0.3.293"
)

// RecordedSDKIdentity validates SDK identities in persisted capture, replay,
// and publication evidence.
func RecordedSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if provider == "claude" && sdk["package"] == claudeTypeScriptSDK {
		return sdk["version"] == claudeTypeScriptSDKVersion
	}
	if provider == "claude" && caseID == "SDK-CASE-042" {
		return false
	}
	name := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider]
	return name != "" && sdk["package"] == name && tool.Str(sdk["version"]) != ""
}

// DriverSDKIdentity validates a new official invocation's business report.
func DriverSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if provider == "claude" && sdk["package"] != claudeTypeScriptSDK {
		return false
	}
	return RecordedSDKIdentity(provider, caseID, sdk)
}

// SDKOutcomeMatches binds the TypeScript driver's result to its business report.
// Python and Codex evidence retain their original SDK report contract.
func SDKOutcomeMatches(sdk, outcome tool.Object) bool {
	return sdk["package"] != claudeTypeScriptSDK || reflect.DeepEqual(sdk, tool.Obj(outcome["sdk"]))
}
