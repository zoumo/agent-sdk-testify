package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestRemoteMissingProbeCancelsAndReapsOwnedPeer(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "peer.pid")
	peer := command{Command: "/bin/sh", Args: []string{"-c", "echo $$ > '" + pidPath + "'; cat; sleep 60"}}
	service, err := startMockRemote(context.Background(), peer, peer, root)
	require.NoError(t, err)
	defer service.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, service.endpoint.URL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + service.endpoint.BearerToken}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	require.Eventually(t, func() bool { _, e := os.Stat(pidPath); return e == nil }, time.Second, 5*time.Millisecond)
	data, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	cleanupErr := service.close()
	require.ErrorContains(t, cleanupErr, "1/2 required connections")
	require.NotContains(t, cleanupErr.Error(), "cleanup did not complete")
	require.NotContains(t, cleanupErr.Error(), "completion unavailable")
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "owned process was not reaped")
}

func TestRemoteDriverCrashCannotPassWithoutConnections(t *testing.T) {
	o := sample(t, "crash")
	o.Mode = "simulate"
	var plan object
	require.NoError(t, readJSON(filepath.Join(o.Materials, "plan.json"), &plan))
	plan["driverProfile"] = "remote-close"
	require.NoError(t, writeJSON(filepath.Join(o.Materials, "plan.json"), plan))
	require.NoError(t, os.MkdirAll(filepath.Join(o.Materials, "remote-probe/raw"), 0700))
	raw, err := os.ReadFile(filepath.Join(o.Materials, "raw/raw.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(o.Materials, "remote-probe/raw/raw.jsonl"), raw, 0600))
	_, err = run(context.Background(), o)
	require.ErrorContains(t, err, "0/2 required connections")
	require.ErrorContains(t, err, "exit status 9")
}
