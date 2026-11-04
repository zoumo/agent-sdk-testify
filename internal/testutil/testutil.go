// Package testutil locates the repository root and prepares the installed tool
// used by process-boundary tests.
package testutil

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// PrepareBinary verifies the shared CLI and guards subprocess race reports.
// Builds belong to the layer runner, never individual test packages.
func PrepareBinary() (func(int) int, error) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	if binary == "" {
		return nil, errors.New("AGENT_SDK_TESTIFY_BIN is required; run make test-e2e or scripts/with-agent-sdk-testify.sh")
	}
	if err := ValidateBinary(binary, RaceEnabled); err != nil {
		return nil, err
	}
	return PrepareRaceGuard()
}

// PrepareRaceGuard catches races in controlled helpers even when a case expects failure.
func PrepareRaceGuard() (func(int) int, error) {
	dir, err := os.MkdirTemp("", "testify-race-")
	if err != nil {
		return nil, err
	}
	// Runtime options are read at process startup; controlled child launchers
	// inherit this value even when the product sanitizes their environment.
	options := os.Getenv("GORACE") + " atexit_sleep_ms=0 log_path=" + filepath.Join(dir, "race")
	if err := os.Setenv("GORACE", options); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return func(code int) int {
		if err := CheckRaceReports(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
		_ = os.RemoveAll(dir)
		return code
	}, nil
}

// ValidateBinary checks the actual installed artifact, not its filename.
func ValidateBinary(binary string, wantRace bool) error {
	if !filepath.IsAbs(binary) {
		return errors.New("AGENT_SDK_TESTIFY_BIN must be absolute")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("CLI is not an executable file: %s", binary)
	}
	build, err := buildinfo.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("read CLI build identity: %w", err)
	}
	if build.Path != "github.com/zoumo/agent-sdk-testify" {
		return fmt.Errorf("unexpected CLI module: %s", build.Path)
	}
	race := false
	for _, setting := range build.Settings {
		if setting.Key == "-race" {
			race = setting.Value == "true"
		}
	}
	if race != wantRace {
		return fmt.Errorf("CLI race mode mismatch: artifact race=%t, test race=%t", race, wantRace)
	}
	return nil
}

// CheckRaceReports is independent of business exit status and captured stderr.
func CheckRaceReports(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "WARNING: DATA RACE") {
			return fmt.Errorf("subprocess data race (%s):\n%s", entry.Name(), data)
		}
	}
	return nil
}

// RaceShellExport preserves guard configuration across isolated launchers.
func RaceShellExport() string {
	return "export GORACE='" + strings.ReplaceAll(os.Getenv("GORACE"), "'", "'\"'\"'") + "'\n"
}
