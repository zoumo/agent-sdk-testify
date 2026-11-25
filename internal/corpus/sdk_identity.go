package corpus

import (
	"reflect"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const (
	claudeTypeScriptSDK        = "@anthropic-ai/claude-agent-sdk"
	claudeTypeScriptSDKVersion = "0.3.293"
	codexRustSDK               = "codex-app-server-remote-client"
	codexRustSDKRevision       = "108730843ae4303ae79e9b6b936343f0b7c483bb"
)

// RecordedSDKIdentity validates SDK identities in persisted capture, replay,
// and publication evidence.
func RecordedSDKIdentity(provider, caseID string, sdk tool.Object) bool {
	if sdk["package"] == codexRustSDK {
		return provider == "codex" && codexRustCase(caseID) && len(sdk) == 3 &&
			sdk["version"] == "0.0.0" && sdk["revision"] == codexRustSDKRevision
	}
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

// SDKOutcomeMatches binds driver result identity to the business report.
// Historical Python results may omit their SDK identity.
func SDKOutcomeMatches(sdk, outcome tool.Object) bool {
	reported, present := outcome["sdk"]
	if !present {
		return sdk["package"] != claudeTypeScriptSDK && sdk["package"] != codexRustSDK
	}
	return reflect.DeepEqual(sdk, tool.Obj(reported))
}

func codexRustCase(caseID string) bool {
	switch caseID {
	case "SDK-CASE-001", "SDK-CASE-002", "SDK-CASE-004", "SDK-CASE-005",
		"SDK-CASE-006", "SDK-CASE-007", "SDK-CASE-008", "SDK-CASE-009",
		"SDK-CASE-010", "SDK-CASE-011", "SDK-CASE-012", "SDK-CASE-013", "SDK-CASE-014",
		"SDK-CASE-015", "SDK-CASE-016", "SDK-CASE-017", "SDK-CASE-018",
		"SDK-CASE-019", "SDK-CASE-020", "SDK-CASE-021", "SDK-CASE-022",
		"SDK-CASE-023", "SDK-CASE-024", "SDK-CASE-027", "SDK-CASE-028",
		"SDK-CASE-029", "SDK-CASE-030", "SDK-CASE-031", "SDK-CASE-032",
		"SDK-CASE-033", "SDK-CASE-034", "SDK-CASE-035", "SDK-CASE-036",
		"SDK-CASE-037", "SDK-CASE-038", "SDK-CASE-039", "SDK-CASE-040", "SDK-CASE-041", "SDK-CASE-042":
		return true
	default:
		return false
	}
}
