package mock

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/zoumo/agent-sdk-testify/internal/history"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/replay"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
	"github.com/zoumo/agent-sdk-testify/internal/version"
)

// InProcessStreams are the fixture-side endpoints transferred to one run.
type InProcessStreams struct {
	Reader io.ReadCloser
	Writer io.WriteCloser
	Stderr io.Writer
}

// InProcessCallbacks connect replay progress and caller-owned cleanup to the
// public run handle.
type InProcessCallbacks struct {
	Started         func(lifecycle.MockContext) error
	FullConsumption func()
	CloseStreams    func(error) error
	StopCause       func() error
}

type inProcessReplayPolicy struct {
	prepared  Prepared
	streams   InProcessStreams
	callbacks InProcessCallbacks
}

// RunInProcess reuses the normal mock execution transaction with a replay
// policy backed by caller-owned closeable streams.
func RunInProcess(ctx context.Context, prepared Prepared, streams InProcessStreams, callbacks InProcessCallbacks) (*Result, error) {
	if streams.Reader == nil || streams.Writer == nil || streams.Stderr == nil {
		return nil, errors.New("in-process mock requires reader, writer and stderr streams")
	}
	if callbacks.CloseStreams == nil {
		return nil, errors.New("in-process mock requires stream cleanup")
	}
	return lifecycle.RunMock(ctx, lifecycle.MockRequest{
		FixtureDir: prepared.FixtureDir,
		Connection: prepared.Connection,
		NativeArgs: prepared.NativeArgs,
		Timeout:    prepared.Timeout,
		ProgressFD: -1,
		ResultFD:   -1,
		Artifact:   prepared.Artifact,
		RequireNew: true,
	}, inProcessReplayPolicy{
		prepared:  prepared,
		streams:   streams,
		callbacks: callbacks,
	})
}

func (p inProcessReplayPolicy) Started(_ context.Context, run lifecycle.MockContext) error {
	if p.callbacks.Started == nil {
		return nil
	}
	return p.callbacks.Started(run)
}

func (p inProcessReplayPolicy) Replay(ctx context.Context, run lifecycle.MockContext) error {
	return replayInProcessConnection(ctx, p, run)
}

func replayInProcessConnection(ctx context.Context, policy inProcessReplayPolicy, run lifecycle.MockContext) (err error) {
	reportPath := run.ConnectionReceipt()
	receiptFile, openErr := os.OpenFile(reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if openErr != nil {
		closeErr := policy.callbacks.CloseStreams(openErr)
		return lifecycle.ClassifyFailure(lifecycle.FailureClassCleanup, errors.Join(openErr, closeErr))
	}
	receipt := tool.Object{
		"schemaVersion":   1,
		"version":         1,
		"status":          "failed",
		"peer":            "Go in-process mock",
		"transport":       lifecycle.MockTransportInProcess,
		"nativeLaunched":  false,
		"fullConsumption": false,
		"clientEOF":       false,
		"cleanupComplete": true,
		"recording":       run.Artifact().Path,
		"toolVersion":     version.Version,
	}
	var replayErr, historyErr, closeErr, reportErr error
	consumed, clientEOF := false, false
	defer func() {
		stopCause := policy.callbacks.StopCause()
		if stopCause == nil && ctx.Err() != nil {
			stopCause = lifecycle.ClassifyFailure(lifecycle.ContextFailureClass(ctx.Err()), ctx.Err())
		}
		closeCause := stopCause
		if closeCause == nil {
			closeCause = errors.Join(replayErr, historyErr)
		}
		closeErr = policy.callbacks.CloseStreams(closeCause)
		failureClass := inProcessFailureClass(stopCause, replayErr, historyErr, closeErr)
		finalErr := errors.Join(stopCause, replayErr, historyErr)
		if finalErr == nil {
			finalErr = closeErr
		} else {
			finalErr = errors.Join(finalErr, closeErr)
		}
		receipt["fullConsumption"] = consumed
		receipt["clientEOF"] = clientEOF && replayErr == nil
		receipt["cleanupComplete"] = closeErr == nil
		if finalErr == nil {
			receipt["status"] = "passed"
		} else {
			receipt["error"] = finalErr.Error()
		}
		if failureClass != "" {
			receipt["failureClass"] = string(failureClass)
		}
		reportErr = errors.Join(receiptFile.Close(), atomicMockJSON(reportPath, receipt))
		if reportErr != nil {
			finalErr = errors.Join(finalErr, reportErr)
			if failureClass == "" {
				failureClass = lifecycle.FailureClassCleanup
			}
		}
		err = errors.Join(finalErr, reportErr)
		if failureClass != "" {
			err = lifecycle.ClassifyFailure(failureClass, err)
		}
	}()

	manifest, document, loadErr := loadMockPhase(run.Artifact().Path, policy.prepared.Agent, run.Connection())
	if loadErr != nil {
		replayErr = lifecycle.ClassifyFailure(lifecycle.FailureClassInternal, loadErr)
		return err
	}
	receipt["provider"] = policy.prepared.Agent
	receipt["planSHA256"] = manifest["planSHA256"]
	receipt["provenance"] = manifest["provenance"]
	receipt["recordingSHA256"] = document.SHA256
	receipt["frames"] = len(document.Frames)
	replayCtx, cancel := context.WithTimeout(ctx, replayTimeout(policy.prepared.Agent, policy.prepared.Timeout, document.Timeout))
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(replayCtx, func() {
		_ = policy.callbacks.CloseStreams(replayCtx.Err())
		close(closed)
	})
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	replayErr = replay.RunStreams(
		replayCtx,
		document,
		recording.Limits{},
		"",
		&classifiedReader{ReadCloser: policy.streams.Reader},
		&classifiedWriter{WriteCloser: policy.streams.Writer},
		func() error {
			consumed = true
			if policy.callbacks.FullConsumption != nil {
				policy.callbacks.FullConsumption()
			}
			return nil
		},
	)
	clientEOF = replayErr == nil
	if replayErr == nil {
		historyErr = history.Check(run.OverlayRoot(), run.Execution().HistoryFiles)
		if historyErr != nil {
			historyErr = lifecycle.ClassifyFailure(lifecycle.FailureClassInternal, historyErr)
		}
	}
	return err
}

func inProcessFailureClass(stopCause, replayErr, historyErr, closeErr error) lifecycle.FailureClass {
	if stopCause != nil {
		if class := lifecycle.FailureClassOf(stopCause); class != "" {
			return class
		}
	}
	if replayErr != nil {
		switch {
		case errors.Is(replayErr, context.DeadlineExceeded):
			return lifecycle.FailureClassTimeout
		case errors.Is(replayErr, context.Canceled):
			return lifecycle.FailureClassCanceled
		}
		if class := lifecycle.FailureClassOf(replayErr); class != "" {
			return class
		}
		return lifecycle.FailureClassStrictReplay
	}
	if historyErr != nil {
		if class := lifecycle.FailureClassOf(historyErr); class != "" {
			return class
		}
		return lifecycle.FailureClassInternal
	}
	if closeErr != nil {
		return lifecycle.FailureClassCleanup
	}
	return ""
}

type classifiedReader struct {
	io.ReadCloser
}

func (r *classifiedReader) Read(value []byte) (int, error) {
	n, err := r.ReadCloser.Read(value)
	if err != nil && !errors.Is(err, io.EOF) {
		err = lifecycle.ClassifyFailure(lifecycle.FailureClassTransport, err)
	}
	return n, err
}

type classifiedWriter struct {
	io.WriteCloser
}

func (w *classifiedWriter) Write(value []byte) (int, error) {
	n, err := w.WriteCloser.Write(value)
	if err == nil && n != len(value) {
		err = io.ErrShortWrite
	}
	if err != nil {
		err = lifecycle.ClassifyFailure(lifecycle.FailureClassTransport, err)
	}
	return n, err
}
