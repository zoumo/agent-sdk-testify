// Package testutil locates the repository root and prepares the installed tool
// used by process-boundary tests.
package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func RepositoryRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locate testutil source")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("repository root not found from %s", dir)
		}
		dir = parent
	}
}

// PrepareBinary reuses an explicit installed binary or builds the repository
// root once for a test package.
func PrepareBinary() (func(), error) {
	if binary := os.Getenv("AGENT_SDK_TESTIFY_BIN"); binary != "" {
		info, err := os.Stat(binary)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("AGENT_SDK_TESTIFY_BIN is not a regular file: %s", binary)
		}
		return func() {}, nil
	}
	root, err := RepositoryRoot()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "agent-sdk-testify-test-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	binary := filepath.Join(dir, "agent-sdk-testify")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", binary, ".")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("build test tool: %w\n%s", err, output)
	}
	if err := os.Setenv("AGENT_SDK_TESTIFY_BIN", binary); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}
