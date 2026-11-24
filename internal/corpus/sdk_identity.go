package corpus

import (
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// OfficialSDKIdentity validates the SDK package used by the official case driver.
func OfficialSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if provider == "claude" && caseID == "SDK-CASE-042" {
		return sdk["package"] == "@anthropic-ai/claude-agent-sdk" && sdk["version"] == "0.3.293"
	}
	name := map[string]string{"codex": "openai-codex", "claude": "claude-agent-sdk"}[provider]
	return name != "" && sdk["package"] == name && tool.Str(sdk["version"]) != ""
}

// OfficialExecutionSDKIdentity checks the SDK used for this invocation. Historical
// recording and index identities continue to use OfficialSDKIdentity.
func OfficialExecutionSDKIdentity(provider, caseID string, outcome, report tool.Object) bool {
	sdk := tool.Obj(report["officialSDK"])
	reported, present := outcome["sdk"]
	if present && !reflect.DeepEqual(reported, sdk) {
		return false
	}
	if sdk["package"] == "codex-app-server-remote-client" {
		return present && provider == "codex" && slices.Contains([]string{
			"SDK-CASE-001", "SDK-CASE-016", "SDK-CASE-017", "SDK-CASE-018", "SDK-CASE-019",
			"SDK-CASE-020", "SDK-CASE-029", "SDK-CASE-032", "SDK-CASE-033", "SDK-CASE-034",
			"SDK-CASE-035", "SDK-CASE-036", "SDK-CASE-041",
		}, caseID) &&
			len(sdk) == 3 && sdk["version"] == "0.0.0" &&
			sdk["revision"] == "108730843ae4303ae79e9b6b936343f0b7c483bb"
	}
	return OfficialSDKIdentity(provider, caseID, sdk)
}
