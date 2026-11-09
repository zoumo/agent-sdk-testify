package lifecycle

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestStoppedLaunchAcceptsConfirmedExitWithoutRecheckingPID(t *testing.T) {
	group := syscall.Getpgrp()
	process := tool.Object{"pid": group}

	process["state"] = "started"
	require.ErrorIs(t, stoppedLaunch(process), ErrBusy)

	process["state"] = "stopped"
	require.NoError(t, stoppedLaunch(process))
}
