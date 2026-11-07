package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func buildCLI(t *testing.T, race bool) string {
	t.Helper()
	name := "AGENT_SDK_TESTIFY_NORMAL_BIN"
	if race {
		name = "AGENT_SDK_TESTIFY_RACE_BIN"
	}
	if binary := os.Getenv(name); binary != "" {
		require.NoError(t, testutil.ValidateBinary(binary, race))
		return binary
	}
	root, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "agent-sdk-testify")
	args := []string{"build", "-o", binary}
	if race {
		args = append(args, "-race")
	}
	cmd := exec.CommandContext(t.Context(), "go", append(args, "./cmd/agent-sdk-testify")...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return binary
}

func TestCLIArtifactModesAndReleaseSmoke(t *testing.T) {
	normal, race := buildCLI(t, false), buildCLI(t, true)
	require.NoError(t, testutil.ValidateBinary(normal, false))
	require.NoError(t, testutil.ValidateBinary(race, true))
	require.ErrorContains(t, testutil.ValidateBinary(normal, true), "race mode mismatch")
	require.ErrorContains(t, testutil.ValidateBinary(race, false), "race mode mismatch")
	root, err := testutil.RepositoryRoot()
	require.NoError(t, err)
	// The public wrapper rejects an ordinary override before running the caller.
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "with-agent-sdk-testify.sh"), "--race", "false")
	cmd.Env = append(os.Environ(), "AGENT_SDK_TESTIFY_BIN="+normal)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "race mode mismatch")
	for _, arg := range []string{"version", "--help"} {
		cmd = exec.CommandContext(t.Context(), normal, arg)
		cmd.Dir = t.TempDir()
		out, err = cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		require.NotEmpty(t, out)
	}
}

func TestExpectedBusinessFailureCannotHideSubprocessRace(t *testing.T) {
	root := t.TempDir()
	source, binary := filepath.Join(root, "race.go"), filepath.Join(root, "race-fixture")
	// Two writes without a happens-before edge intentionally exercise the runtime.
	require.NoError(t, os.WriteFile(source, []byte(`package main
import "fmt"
func main() {
 var value int
 start, done := make(chan struct{}), make(chan struct{})
 for range 2 { go func() { <-start; value++; done <- struct{}{} }() }
 close(start); <-done; <-done
 fmt.Println(value)
}
`), 0o600))
	cmd := exec.CommandContext(t.Context(), "go", "build", "-race", "-o", binary, source)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	t.Setenv("GORACE", os.Getenv("GORACE"))
	guard, err := testutil.PrepareRaceGuard()
	require.NoError(t, err)
	// Route through a controlled launcher whose input env deliberately lacks GORACE.
	launcher := filepath.Join(root, "launcher")
	require.NoError(t, os.WriteFile(launcher, []byte("#!/bin/sh\n"+testutil.RaceShellExport()+"exec '"+binary+"'\n"), 0o700))
	cmd = exec.CommandContext(t.Context(), launcher)
	cmd.Env = []string{"PATH=/missing"}
	out, err = cmd.CombinedOutput()
	require.Error(t, err, "%s", out) // An expected business failure alone would pass.
	require.Equal(t, 1, guard(0), "subprocess race must override a successful test result")
}
