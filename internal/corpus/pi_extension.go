package corpus

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

const (
	PiVersion            = "1.6.0"
	PiExtensionDirectory = "pi-extension"
	PiExtensionFilename  = "session.ts"
	PiExtensionRevision  = "2dfc08f88c74d5ed3e8390ffabf710e0a55ff28d"
	PiExtensionSHA256    = "298c12cce0fe179295cfb1c01d4ae9fa9283c1525c593c6b4dd8be82ea57683b"
	piPackage            = "@earendil-works/pi-coding-agent"
)

func IsPiExtensionCase(provider, caseID string) bool {
	return provider == "pi" && (caseID == "SDK-CASE-012" || caseID == "SDK-CASE-038")
}

// PiExtensionIdentity describes the native stdio client and the exact extension
// used with the Pi executable. The RpcClient identity omits these fields.
func PiExtensionIdentity() tool.Object {
	return tool.Object{
		"package": piPackage, "version": PiVersion, "client": "native-stdio",
		"extensionRevision": PiExtensionRevision, "extensionSHA256": PiExtensionSHA256,
	}
}

func PiExtensionConfig(caseID string) tool.Object {
	switch caseID {
	case "SDK-CASE-012":
		return tool.Object{"toolApproval": true, "interactionTimeoutMs": float64(30000)}
	case "SDK-CASE-038":
		return tool.Object{"inject": true}
	default:
		return nil
	}
}

// ValidatePiExtensionPlan checks sealed launch facts without opening the recorded
// path. Execution-time bytes are checked in the private record directory.
func ValidatePiExtensionPlan(plan tool.Object) error {
	if !IsPiExtensionCase(tool.Str(plan["provider"]), tool.Str(plan["caseId"])) {
		if plan["piExtension"] != nil {
			return errors.New("recording has an unexpected Pi extension identity")
		}
		return nil
	}
	if plan["cliVersion"] != PiVersion || !reflect.DeepEqual(tool.Obj(plan["piExtension"]), PiExtensionIdentity()) {
		return errors.New("recording does not identify the pinned Pi runtime and extension")
	}
	args := tool.Strings(plan["args"])
	if len(args) < 3 || args[len(args)-2] != "-e" || !slices.Contains(args[:len(args)-2], "--no-extensions") {
		return errors.New("recording does not explicitly load the pinned Pi extension")
	}
	path := args[len(args)-1]
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != PiExtensionFilename ||
		filepath.Base(filepath.Dir(path)) != PiExtensionDirectory {
		return errors.New("recording Pi extension path differs from its private launch path")
	}
	return nil
}
