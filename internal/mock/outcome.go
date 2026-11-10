package mock

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// replayOutcome owns the protocol facts and receipt shared by process and
// in-process mock transports. Transport adapters only add their own receipt
// fields and cleanup behavior.
type replayOutcome struct {
	path    string
	file    *os.File
	receipt tool.Object

	fullConsumption bool
	clientEOF       bool
	replayErr       error
	historyErr      error
}

func openReplayOutcome(path string, receipt tool.Object) (*replayOutcome, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &replayOutcome{path: path, file: file, receipt: receipt}, nil
}

func (o *replayOutcome) bind(manifest tool.Object, document *recording.Document) {
	o.receipt["planSHA256"] = manifest["planSHA256"]
	o.receipt["provenance"] = manifest["provenance"]
	o.receipt["recordingSHA256"] = document.SHA256
	o.receipt["frames"] = len(document.Frames)
}

func (o *replayOutcome) recordReplay(consumed bool, err error) {
	o.fullConsumption = consumed
	o.replayErr = err
	o.clientEOF = err == nil
}

func (o *replayOutcome) recordHistory(err error) {
	o.historyErr = err
}

// finish records the common facts, invokes transport cleanup, and publishes the
// receipt exactly once. The original errors remain in the returned chain.
func (o *replayOutcome) finish(stopCause error, cleanup func(error) error) error {
	cleanupCause := stopCause
	if cleanupCause == nil {
		cleanupCause = errors.Join(o.replayErr, o.historyErr)
	}
	var cleanupErr error
	if cleanup != nil {
		cleanupErr = cleanup(cleanupCause)
	}
	failureClass := replayFailureClass(stopCause, o.replayErr, o.historyErr, cleanupErr)
	finalErr := errors.Join(stopCause, o.replayErr, o.historyErr)
	if finalErr == nil {
		finalErr = cleanupErr
	} else {
		finalErr = errors.Join(finalErr, cleanupErr)
	}
	closeErr := o.file.Close()
	if closeErr != nil {
		finalErr = errors.Join(finalErr, closeErr)
		failureClass = lifecycle.FailureClassCleanup
	}
	if finalErr != nil && failureClass == "" {
		failureClass = lifecycle.FailureClassInternal
	}
	o.receipt["fullConsumption"] = o.fullConsumption
	o.receipt["clientEOF"] = o.clientEOF
	o.receipt["cleanupComplete"] = cleanupErr == nil && closeErr == nil
	if finalErr != nil {
		o.receipt["status"] = "failed"
		o.receipt["error"] = finalErr.Error()
	} else {
		o.receipt["status"] = "passed"
	}
	if failureClass != "" {
		o.receipt["failureClass"] = string(failureClass)
	}
	if _, hasExitCode := o.receipt["exitCode"]; hasExitCode {
		if finalErr == nil {
			o.receipt["exitCode"] = 0
		} else {
			o.receipt["exitCode"] = 1
		}
	}

	reportErr := atomicMockJSON(o.path, o.receipt)
	if reportErr != nil {
		finalErr = errors.Join(finalErr, reportErr)
		failureClass = lifecycle.FailureClassCleanup
	}
	if finalErr == nil {
		return nil
	}
	if failureClass == "" {
		failureClass = lifecycle.FailureClassInternal
	}
	return classifyReplayOutcome(failureClass, finalErr)
}

func replayFailureClass(stopCause, replayErr, historyErr, cleanupErr error) lifecycle.FailureClass {
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
		switch {
		case transportFailure(replayErr):
			return lifecycle.FailureClassTransport
		default:
			return lifecycle.FailureClassStrictReplay
		}
	}
	if historyErr != nil {
		if class := lifecycle.FailureClassOf(historyErr); class != "" {
			return class
		}
		return lifecycle.FailureClassInternal
	}
	if cleanupErr != nil {
		return lifecycle.FailureClassCleanup
	}
	return ""
}

func classifyReplayOutcome(class lifecycle.FailureClass, err error) error {
	if err == nil {
		return nil
	}
	if lifecycle.FailureClassOf(err) == class {
		return err
	}
	// ClassifyFailure preserves an existing nested class, which cannot express
	// that the replay outcome or receipt publication phase is authoritative.
	return &lifecycle.ClassifiedError{Class: class, Err: err}
}

func transportFailure(err error) bool {
	if errors.Is(err, io.ErrShortWrite) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EBADF) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return true
	}
	var syscallErr *os.SyscallError
	return errors.As(err, &syscallErr)
}
