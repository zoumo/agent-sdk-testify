package official

import (
	"fmt"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

type DriverPeer struct {
	tool.Command
	Probe *tool.Command `json:"probe,omitempty"`
}

type Request struct {
	Case      string `json:"case"`
	Materials string `json:"materials"`
	Runtime   string `json:"runtime"`
}

// Resolve the installed entry point for the selected official driver.
// Recorded paths and caller-selected SDK drivers are never executable inputs.
// Shared by the record and official verification entry points.
func ResolveDriver(provider string) (string, error) {
	if provider != "codex" && provider != "claude" && provider != "pi" {
		return "", fmt.Errorf("unsupported provider %q: expected codex, claude or pi", provider)
	}
	name := "agent-sdk-testify-" + provider
	path, err := tool.AbsoluteExecutable(name)
	if err != nil {
		if provider == "claude" {
			return "", fmt.Errorf("official driver %s is not available; install the Claude driver npm tarball with Node.js 22+ and add its executable directory to PATH: %w", name, err)
		}
		if provider == "pi" {
			return "", fmt.Errorf("official driver %s is not available; install the Pi driver npm tarball and Pi SDK with Node.js 22+ and add its executable directory to PATH: %w", name, err)
		}
		return "", fmt.Errorf("official driver %s is not available; install the Codex Rust driver from the matching platform archive and add its directory to PATH: %w", name, err)
	}
	// npm links the executable into PATH. Hash and execute the same target.
	return tool.ValidateCommand(path)
}
