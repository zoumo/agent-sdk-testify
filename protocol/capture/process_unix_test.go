//go:build darwin || linux

package capture

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureWaitHelper(t *testing.T) {
	if os.Getenv("AGENT_MOCK_CAPTURE_WAIT_TEST") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}
func TestNativeWaitCanJoinAtCleanupDeadline(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestCaptureWaitHelper$")
	cmd.Env = append(os.Environ(), "AGENT_MOCK_CAPTURE_WAIT_TEST=1")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
	})
	abandon := make(chan struct{})
	result := make(chan nativeExit, 1)
	var processMu sync.Mutex
	go func() { result <- waitNative(cmd, &processMu, abandon) }()
	close(abandon)
	select {
	case exit := <-result:
		assert.False(t, exit.reaped)
		assert.Nil(t, exit.code)
		require.ErrorContains(t, exit.err, "cleanup deadline")
	case <-time.After(time.Second):
		t.Fatal("native waiter did not join after cleanup deadline")
	}
}
