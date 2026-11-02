package main

import (
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"testing"
)

func TestPublicCommands(t *testing.T) {
	binary := os.Getenv("AGENT_SDK_TESTIFY_BIN")
	out, err := exec.Command(binary, "--help").CombinedOutput()
	require.NoError(t, err)
	for _, name := range []string{"record", "verify", "mock"} {
		require.Contains(t, string(out), name)
	}
	for _, name := range []string{"simulate", "replay", "corpus", "protocol", "peer"} {
		require.NotContains(t, string(out), name)
		out, err := exec.Command(binary, name, "--help").CombinedOutput()
		require.Error(t, err, string(out))
		require.Contains(t, string(out), "expected record, verify or mock")
	}
	out, err = exec.Command(binary, "record", "--materials", "/missing", "--driver", "/missing").CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "flag provided but not defined")
}
