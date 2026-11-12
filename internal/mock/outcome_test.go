package mock

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

func TestReplayFailureClass(t *testing.T) {
	pathErr := &os.PathError{Op: "write", Path: "stdout", Err: errors.New("broken pipe")}
	for _, test := range []struct {
		name       string
		stopCause  error
		replayErr  error
		historyErr error
		cleanupErr error
		want       lifecycle.FailureClass
	}{
		{name: "success"},
		{name: "strict replay", replayErr: errors.New("early client EOF"), want: lifecycle.FailureClassStrictReplay},
		{name: "classified transport", replayErr: lifecycle.ClassifyFailure(lifecycle.FailureClassTransport, io.ErrClosedPipe), want: lifecycle.FailureClassTransport},
		{name: "path error", replayErr: pathErr, want: lifecycle.FailureClassTransport},
		{name: "short write", replayErr: io.ErrShortWrite, want: lifecycle.FailureClassTransport},
		{name: "canceled", replayErr: context.Canceled, want: lifecycle.FailureClassCanceled},
		{name: "timeout", replayErr: context.DeadlineExceeded, want: lifecycle.FailureClassTimeout},
		{
			name: "deadline precedes classified stream close",
			replayErr: errors.Join(
				lifecycle.ClassifyFailure(lifecycle.FailureClassTransport, io.ErrClosedPipe),
				context.DeadlineExceeded,
			),
			want: lifecycle.FailureClassTimeout,
		},
		{name: "abort stops replay", stopCause: lifecycle.ClassifyFailure(lifecycle.FailureClassAborted, errors.New("mock run aborted")), replayErr: errors.New("early client EOF"), want: lifecycle.FailureClassAborted},
		{
			name:      "abort precedes deadline and classified stream close",
			stopCause: lifecycle.ClassifyFailure(lifecycle.FailureClassAborted, errors.New("mock run aborted")),
			replayErr: errors.Join(
				context.DeadlineExceeded,
				lifecycle.ClassifyFailure(lifecycle.FailureClassTransport, io.ErrClosedPipe),
			),
			want: lifecycle.FailureClassAborted,
		},
		{name: "history drift", replayErr: nil, historyErr: errors.New("history hash mismatch"), want: lifecycle.FailureClassInternal},
		{name: "cleanup", cleanupErr: errors.New("close failure"), want: lifecycle.FailureClassCleanup},
		{name: "replay precedes cleanup", replayErr: errors.New("extra client byte after scenario"), cleanupErr: errors.New("close failure"), want: lifecycle.FailureClassStrictReplay},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, replayFailureClass(test.stopCause, test.replayErr, test.historyErr, test.cleanupErr))
		})
	}
}

func TestReplayOutcomeDeadlineAuthorityPreservesCauses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.json")
	outcome, err := openReplayOutcome(path, tool.Object{
		"status": "failed", "fullConsumption": false, "clientEOF": false, "cleanupComplete": true,
	})
	require.NoError(t, err)
	outcome.recordReplay(false, errors.Join(
		context.DeadlineExceeded,
		lifecycle.ClassifyFailure(lifecycle.FailureClassTransport, io.ErrClosedPipe),
	))
	err = outcome.finish(nil, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	require.Equal(t, lifecycle.FailureClassTimeout, lifecycle.FailureClassOf(err))
	require.True(t, strings.HasPrefix(err.Error(), "timeout:"), err.Error())

	receipt := tool.Object{}
	require.NoError(t, tool.ReadJSON(path, &receipt))
	require.Equal(t, "failed", receipt["status"])
	require.Equal(t, "timeout", receipt["failureClass"])
	require.NotContains(t, receipt, "exitCode")
}

func TestReplayOutcomeReceiptCloseFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		exitCode int
		hasExit  bool
		wantCode int
	}{
		{name: "process", exitCode: 0, hasExit: true, wantCode: 1},
		{name: "in-process"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "main.json")
			receipt := tool.Object{
				"status": "failed", "fullConsumption": true, "clientEOF": true, "cleanupComplete": true,
			}
			if test.hasExit {
				receipt["exitCode"] = test.exitCode
			}
			outcome, err := openReplayOutcome(path, receipt)
			require.NoError(t, err)
			outcome.recordReplay(true, nil)
			require.NoError(t, outcome.file.Close())

			err = outcome.finish(nil, nil)
			require.ErrorContains(t, err, "file already closed")
			require.Equal(t, lifecycle.FailureClassCleanup, lifecycle.FailureClassOf(err))
			require.True(t, strings.HasPrefix(err.Error(), "cleanup:"), err.Error())

			published := tool.Object{}
			require.NoError(t, tool.ReadJSON(path, &published))
			require.Equal(t, "failed", published["status"])
			require.Equal(t, false, published["cleanupComplete"])
			require.Equal(t, "cleanup", published["failureClass"])
			require.Contains(t, published["error"], "file already closed")
			if test.hasExit {
				require.EqualValues(t, test.wantCode, published["exitCode"])
			} else {
				require.NotContains(t, published, "exitCode")
			}
		})
	}
}

func TestReplayOutcomeReceiptWriteFailurePreservesCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.json")
	outcome, err := openReplayOutcome(path, tool.Object{
		"status": "failed", "cleanupComplete": true, "unsupported": func() {},
	})
	require.NoError(t, err)
	outcome.recordReplay(false, errors.New("original replay failure"))

	err = outcome.finish(nil, nil)
	require.ErrorContains(t, err, "original replay failure")
	require.ErrorContains(t, err, "unsupported type")
	require.Equal(t, lifecycle.FailureClassCleanup, lifecycle.FailureClassOf(err))
	require.True(t, strings.HasPrefix(err.Error(), "cleanup:"), err.Error())

	receipt := tool.Object{}
	require.Error(t, tool.ReadJSON(path, &receipt))
}

func TestReplayOutcomePreservesFactsAndReceipt(t *testing.T) {
	t.Run("process failure", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "main.json")
		outcome, err := openReplayOutcome(path, tool.Object{
			"status": "failed", "exitCode": 0, "fullConsumption": false, "clientEOF": false, "cleanupComplete": true,
		})
		require.NoError(t, err)
		outcome.recordReplay(true, errors.New("extra client byte after scenario"))
		outcome.recordHistory(nil)
		err = outcome.finish(nil, nil)
		require.ErrorContains(t, err, "extra client byte after scenario")
		require.Equal(t, lifecycle.FailureClassStrictReplay, lifecycle.FailureClassOf(err))

		receipt := tool.Object{}
		require.NoError(t, tool.ReadJSON(path, &receipt))
		require.Equal(t, "failed", receipt["status"])
		require.EqualValues(t, 1, receipt["exitCode"])
		require.Equal(t, true, receipt["fullConsumption"])
		require.Equal(t, false, receipt["clientEOF"])
		require.Equal(t, true, receipt["cleanupComplete"])
		require.Equal(t, "strict-replay", receipt["failureClass"])
		require.Contains(t, receipt["error"], "extra client byte after scenario")
	})

	t.Run("in-process cleanup keeps EOF fact", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "main.json")
		outcome, err := openReplayOutcome(path, tool.Object{
			"status": "failed", "fullConsumption": false, "clientEOF": false, "cleanupComplete": true,
		})
		require.NoError(t, err)
		outcome.recordReplay(true, nil)
		err = outcome.finish(nil, func(error) error { return errors.New("close failure") })
		require.ErrorContains(t, err, "close failure")
		require.Equal(t, lifecycle.FailureClassCleanup, lifecycle.FailureClassOf(err))

		receipt := tool.Object{}
		require.NoError(t, tool.ReadJSON(path, &receipt))
		require.Equal(t, "failed", receipt["status"])
		require.NotContains(t, receipt, "exitCode")
		require.Equal(t, true, receipt["fullConsumption"])
		require.Equal(t, true, receipt["clientEOF"])
		require.Equal(t, false, receipt["cleanupComplete"])
		require.Equal(t, "cleanup", receipt["failureClass"])
	})

	t.Run("history drift and abort", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "main.json")
		outcome, err := openReplayOutcome(path, tool.Object{
			"status": "failed", "fullConsumption": false, "clientEOF": false, "cleanupComplete": true,
		})
		require.NoError(t, err)
		outcome.recordReplay(true, nil)
		outcome.recordHistory(errors.New("history hash mismatch"))
		err = outcome.finish(nil, nil)
		require.ErrorContains(t, err, "history hash mismatch")
		require.Equal(t, lifecycle.FailureClassInternal, lifecycle.FailureClassOf(err))

		receipt := tool.Object{}
		require.NoError(t, tool.ReadJSON(path, &receipt))
		require.Equal(t, true, receipt["fullConsumption"])
		require.Equal(t, true, receipt["clientEOF"])
		require.Equal(t, "internal", receipt["failureClass"])
	})
}
