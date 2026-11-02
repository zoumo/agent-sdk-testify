package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The installed tool must start its own protocol processes without --peer,
// a checkout, a discoverable tool on PATH, or an inherited binary override.
func TestBinaryUsesItselfForCaptureReplayAndSimulation(t *testing.T) {
	o := sample(t, "passed")
	binary := filepath.Join(t.TempDir(), "agent-sdk-testify")
	data, err := os.ReadFile(o.Peer)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(binary, data, 0700))
	for _, mode := range []string{"record", "replay", "simulate"} {
		t.Run(mode, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			args := []string{mode, "--materials", o.Materials, "--driver", o.Driver,
				"--driver-arg", "driver", "--output", output}
			if mode == "record" {
				args = append(args, "--cli", o.Driver)
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=/missing", "GORACE=atexit_sleep_ms=0"}
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			var summary object
			require.NoError(t, json.Unmarshal(out, &summary))
			require.Equal(t, "passed", obj(summary["result"])["status"])
			var peer command
			require.NoError(t, readJSON(filepath.Join(output, "peer-main.json"), &peer))
			require.Equal(t, binary, peer.Command)
			require.Equal(t, "protocol", peer.Args[0])
		})
	}
}
