package replay

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

func runtimePipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	return r, w
}

func runtimeProgressFD(t *testing.T, source *os.File) int {
	t.Helper()
	fd, err := syscall.Dup(int(source.Fd()))
	require.NoError(t, err)
	// Ownership of this duplicate transfers to Run; no second os.File wrapper.
	return fd
}

func TestRunUnblocksEveryTransportWait(t *testing.T) {
	for _, mode := range []string{"read", "write", "delay", "eof", "progress"} {
		t.Run(mode, func(t *testing.T) {
			in, _ := runtimePipe(t)
			_, out := runtimePipe(t)
			d := synthetic(t, "codex", "<{}")
			switch mode {
			case "read":
				d = synthetic(t, "codex", ">{}")
			case "write":
				d = synthetic(t, "codex", `<{"text":"`+strings.Repeat("x", 1<<20)+`"}`)
			case "delay":
				d.Frames[0].Delay = time.Hour
			}
			progressFD := -1
			if mode == "progress" {
				_, w := runtimePipe(t)
				progressFD = runtimeProgressFD(t, w)
				require.NoError(t, syscall.SetNonblock(progressFD, true))
				// Fill the dedicated pipe so even its single marker must wait.
				for {
					_, err := syscall.Write(progressFD, make([]byte, 4096))
					if errors.Is(err, syscall.EAGAIN) {
						break
					}
					require.NoError(t, err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			err := Run(ctx, d, recording.Limits{}, "", in, out, progressFD)
			require.ErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestRunProgressClosesBeforeInputEOF(t *testing.T) {
	in, client := runtimePipe(t)
	server, out := runtimePipe(t)
	progress, w := runtimePipe(t)
	fd := runtimeProgressFD(t, w)
	require.NoError(t, w.Close())
	d := synthetic(t, "claude", "<{}")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Run(ctx, d, recording.Limits{}, "", in, out, fd) }()
	require.NoError(t, progress.SetReadDeadline(time.Now().Add(time.Second)))
	marker, err := io.ReadAll(progress)
	require.NoError(t, err)
	assert.Equal(t, "1", string(marker))
	select {
	case err := <-result:
		t.Fatalf("returned before EOF: %v", err)
	default:
	}
	require.NoError(t, client.Close())
	require.NoError(t, <-result)
	require.NoError(t, out.Close())
	data, err := io.ReadAll(server)
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(data))
}

func TestRunTransportFailureAndCancellation(t *testing.T) {
	in, client := runtimePipe(t)
	server, out := runtimePipe(t)
	require.NoError(t, client.Close())
	require.NoError(t, server.Close())
	d := synthetic(t, "codex", "<{}")
	err := Run(context.Background(), d, recording.Limits{}, "", in, out, -1)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Run(ctx, d, recording.Limits{}, "", in, out, -1)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRunRejectsMissingProgressBeforeAllocatingProtocolFDs(t *testing.T) {
	in, client := runtimePipe(t)
	server, out := runtimePipe(t)
	require.NoError(t, client.Close())
	first, err := syscall.Dup(int(out.Fd()))
	require.NoError(t, err)
	missing, err := syscall.Dup(int(out.Fd()))
	require.NoError(t, err)
	require.NoError(t, syscall.Close(first))
	require.NoError(t, syscall.Close(missing))
	// These free descriptor numbers would be reused by Run's input/output
	// duplicates. A missing inherited fd must fail before those allocations.
	err = Run(context.Background(), synthetic(t, "codex", "<{}"), recording.Limits{}, "", in, out, missing)
	require.Error(t, err)
	require.NoError(t, out.Close())
	data, err := io.ReadAll(server)
	require.NoError(t, err)
	assert.Empty(t, data, "invalid progress fd must not emit a marker onto protocol stdout")
}
