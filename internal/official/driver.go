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
	if provider != "codex" && provider != "claude" {
		return "", fmt.Errorf("unsupported provider %q: expected codex or claude", provider)
	}
	name := "agent-sdk-testify-" + provider
	path, err := tool.AbsoluteExecutable(name)
	if err != nil {
		return "", fmt.Errorf("official driver %s is not available; add the installed %s driver and its SDK to the recording/verification environment: %w", name, provider, err)
	}
	// npm links the executable into PATH. Hash and execute the same target.
	return tool.ValidateCommand(path)
}
