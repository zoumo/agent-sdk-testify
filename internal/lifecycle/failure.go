package lifecycle

import (
	"context"
	"errors"
)

// FailureClass is the machine-readable terminal reason for an action.
type FailureClass string

const (
	FailureClassCanceled     FailureClass = "canceled"
	FailureClassAborted      FailureClass = "aborted"
	FailureClassTransport    FailureClass = "transport"
	FailureClassStrictReplay FailureClass = "strict-replay"
	FailureClassTimeout      FailureClass = "timeout"
	FailureClassCleanup      FailureClass = "cleanup"
	FailureClassInternal     FailureClass = "internal"
)

// ClassifiedError binds one failure class to its diagnostic without erasing
// the underlying error for callers that need context or I/O matching.
type ClassifiedError struct {
	Class FailureClass
	Err   error
}

func (e *ClassifiedError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return string(e.Class)
	}
	return string(e.Class) + ": " + e.Err.Error()
}

func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ClassifyFailure returns nil for nil and preserves an existing terminal class.
func ClassifyFailure(class FailureClass, err error) error {
	if err == nil {
		return nil
	}
	if FailureClassOf(err) != "" {
		return err
	}
	return &ClassifiedError{Class: class, Err: err}
}

// FailureClassOf returns the first terminal class carried by an error tree.
func FailureClassOf(err error) FailureClass {
	var classified *ClassifiedError
	if errors.As(err, &classified) {
		return classified.Class
	}
	return ""
}

// ContextFailureClass maps caller-context termination to the terminal class
// used by mock evidence.
func ContextFailureClass(err error) FailureClass {
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureClassTimeout
	}
	return FailureClassCanceled
}

func contextFailure(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	if FailureClassOf(cause) != "" {
		return cause
	}
	return ClassifyFailure(ContextFailureClass(ctx.Err()), cause)
}
