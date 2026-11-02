package main

import "fmt"

// Resolve only the console scripts shipped by the official-driver wheel.
// Recorded paths and caller-selected SDK drivers are never executable inputs.
// Shared by the record and official verification entry points.
func resolveOfficialDriver(provider string) (string, error) {
	if provider != "codex" && provider != "claude" {
		return "", fmt.Errorf("unsupported provider %q: expected codex or claude", provider)
	}
	name := "agent-sdk-testify-" + provider
	path, err := absoluteExecutable(name)
	if err != nil {
		return "", fmt.Errorf("official driver %s is not available; install the driver wheel and %s SDK extra in the recording/verification environment: %w", name, provider, err)
	}
	return path, nil
}
