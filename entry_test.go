package main

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryEntryOutsideCheckout(t *testing.T) {
	cmd, _, output := verifyFixture(t, "passed")
	script, err := filepath.Abs("scripts/simulation.sh")
	require.NoError(t, err)
	args := append([]string{script}, cmd.Args[1:]...)
	cmd.Path = "/bin/bash"
	cmd.Args = append([]string{"bash"}, args...)
	cmd.Env = append(cmd.Env, "AGENT_SDK_TESTIFY_BUILD_DIR="+filepath.Dir(os.Getenv("AGENT_SDK_TESTIFY_BIN")))
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", data)
	var evidence object
	require.NoError(t, readJSON(filepath.Join(output, "verification.json"), &evidence))
	require.Equal(t, "passed", evidence["status"])
}
