package recording

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/rawjson"
)

// NativeIdentity deliberately excludes environment values. Version is a caller
// assertion; capture hashes the resolved executable without invoking --version.
type NativeIdentity struct {
	Executable string   `json:"executable"`
	SHA256     string   `json:"sha256"`
	Args       []string `json:"args"`
	CWD        string   `json:"cwd"`
	Version    string   `json:"version,omitempty"`
}

type TransportReport struct {
	Version         int    `json:"version"`
	CaptureStatus   string `json:"captureStatus"`
	Reason          string `json:"reason"`
	Frames          int64  `json:"frames"`
	ProtocolBytes   int64  `json:"protocolBytes"`
	StderrBytes     int64  `json:"stderrBytes"`
	Milliseconds    int64  `json:"milliseconds"`
	NativeLaunched  bool   `json:"nativeLaunched"`
	NativeExitCode  *int   `json:"nativeExitCode"`
	NativeSignal    string `json:"nativeSignal,omitempty"`
	ClientEOF       bool   `json:"clientEOF"`                // Complete input reached EOF without a forwarding error.
	StdinEOFAction  string `json:"stdinEOFAction,omitempty"` // Actual successful close or interrupt, not configured policy.
	StdinEOFSignal  string `json:"stdinEOFSignal,omitempty"` // Signal successfully sent to the direct child.
	Reaped          bool   `json:"reaped"`
	CleanupComplete bool   `json:"cleanupComplete"`
	Error           string `json:"error,omitempty"`
}

type Manifest struct {
	Version         int               `json:"version"`
	RecordingFormat Format            `json:"recordingFormat"`
	ProxyVersion    string            `json:"proxyVersion"`
	CaptureStatus   string            `json:"captureStatus"`
	Native          NativeIdentity    `json:"native"`
	Files           map[string]string `json:"files"`
}

type Receipt struct {
	Version         int    `json:"version"`
	FullConsumption bool   `json:"fullConsumption"`
	NativeLaunched  bool   `json:"nativeLaunched"`
	Peer            string `json:"peer"`
	Provider        string `json:"provider"`
	Frames          int    `json:"frames"`
	RecordingSHA256 string `json:"recordingSHA256"`
	ClientEOF       bool   `json:"clientEOF"`
}

// ValidateManifest validates an explicitly supplied v1 format descriptor and the
// hash for one caller-selected artifact. Unknown outer metadata is preserved,
// including historical source paths. It never follows paths from a manifest.
// Legacy manifests without recordingFormat remain the external caller's concern.
func ValidateManifest(data []byte, artifact string, content []byte) (rawjson.Value, error) {
	v, err := rawjson.Parse(data)
	if err != nil {
		return v, err
	}
	f, ok := v.Member("recordingFormat")
	if !ok {
		return v, errors.New("recordingFormat required")
	}
	if err = allowed(f, "name", "version", "transport"); err != nil {
		return v, err
	}
	name, e1 := stringField(f, "name")
	version, e2 := integer(f, "version", false)
	transport, e3 := stringField(f, "transport")
	if e1 != nil || e2 != nil || e3 != nil || name != FormatName || version != FormatVersion || transport != Transport {
		return v, errors.New("unsupported recording format")
	}
	files, _ := v.Member("files")
	digest, e := stringField(files, artifact)
	if e != nil || digest != Hash(content) {
		return v, fmt.Errorf("manifest hash mismatch for %q", artifact)
	}
	return v, nil
}

// EncodeReport is for tool-owned metadata only; native payloads use EncodeFrame.
func EncodeReport(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
