package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Attempts are evidence, including failures. Only explicitly selected test
// directories should use temporary storage; default runs survive OS temp cleanup.
func createOutput(path, mode string) (string, error) {
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return absolute, os.Mkdir(absolute, 0700)
	}
	base, err := outputBase(mode)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("create persistent attempt directory: %w", err)
	}
	return os.MkdirTemp(base, "attempt-")
}

func outputBase(mode string) (string, error) {
	base := os.Getenv("AGENT_SDK_TESTIFY_DATA_DIR")
	if base == "" {
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(base, "agent-sdk-testify")
	}
	return filepath.Abs(filepath.Join(base, "attempts", mode))
}
