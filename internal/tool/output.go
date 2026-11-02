package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir is the single owner of persistent tool data. Attempts and private
// archives are siblings rather than copies of the same environment logic.
func DataDir() (string, error) {
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
	return filepath.Abs(base)
}

// CreateOutput returns a new attempt directory. Existing paths are never
// overwritten because failed attempts are evidence too.
func CreateOutput(path, mode string) (string, error) {
	if path != "" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return absolute, os.Mkdir(absolute, 0700)
	}
	base, err := OutputBase(mode)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return "", fmt.Errorf("create persistent attempt directory: %w", err)
	}
	return os.MkdirTemp(base, "attempt-")
}

func OutputBase(mode string) (string, error) {
	base, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "attempts", mode), nil
}
