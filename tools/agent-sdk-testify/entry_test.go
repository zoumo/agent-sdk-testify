package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Check the repository entry's routing with synthetic drivers and native peers.
// TestSDKRequestUsesRunnerPeers separately exercises the actual Go SDK driver.
func TestSimulationEntryStagesOutsideCheckout(t *testing.T) {
	o := sample(t, "passed")
	build, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(o.Peer, filepath.Join(build, "agent-sdk-testify")))
	for _, name := range []string{"sdk-driver", "agent-sdk-testify-codex"} {
		wrapper := fmt.Sprintf("#!/bin/sh\nexec %q driver \"$@\"\n", o.Driver)
		require.NoError(t, os.WriteFile(filepath.Join(build, name), []byte(wrapper), 0700))
	}
	t.Setenv("AGENT_SDK_TESTIFY_BUILD_DIR", build)
	t.Setenv("PATH", build+string(os.PathListSeparator)+os.Getenv("PATH"))
	script, err := filepath.Abs("../../scripts/simulation.sh")
	require.NoError(t, err)
	capture := filepath.Join(outside, "record")
	index := filepath.Join(outside, "corpus", "index.json")
	indexBytes, err := os.ReadFile("../../tests/simulation/recordings/index.json")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(index), 0700))
	require.NoError(t, os.WriteFile(index, indexBytes, 0600))
	for _, mode := range []string{"record", "replay", "simulate"} {
		materials := capture
		args := []string{script, mode}
		if mode != "simulate" {
			args = append(args, "codex")
		}
		if mode == "record" {
			materials = o.Materials
			args = append(args, "--cli", o.Driver, "--index", index, "--archive", filepath.Join(outside, "archive"))
		}
		output := filepath.Join(outside, mode)
		args = append(args, "--materials", materials, "--output", output)
		cmd := exec.Command("bash", args...)
		cmd.Dir = outside
		log, err := cmd.CombinedOutput()
		if mode == "record" {
			// A synthetic transport pass lacks official SDK business evidence.
			// The repository entry must reject adoption, but retain the capture.
			require.Error(t, err, "%s", log)
			var admission struct {
				Passed int `json:"passed"`
			}
			require.NoError(t, readJSON(filepath.Join(output, "admission-summary.json"), &admission))
			require.Zero(t, admission.Passed)
			unchanged, readErr := os.ReadFile(index)
			require.NoError(t, readErr)
			require.Equal(t, indexBytes, unchanged)
		} else {
			require.NoError(t, err, "%s", log)
		}
		var summary struct {
			Mode   string `json:"mode"`
			Result result `json:"result"`
		}
		data, err := os.ReadFile(filepath.Join(output, "summary.json"))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &summary))
		require.Equal(t, mode, summary.Mode)
		require.Equal(t, "passed", summary.Result.Status)
		require.FileExists(t, filepath.Join(output, "summary.json"))
	}
	// Later stages must leave the original bytes and source manifest intact.
	require.NoError(t, verifySourcePhase(capture, true))
}
