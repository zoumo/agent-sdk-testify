package capture

import (
	"errors"
	"os"
	"strings"
	"time"
)

// Options describes a single capture, independently of command-line parsing.
// Output must not exist. ProxyVersion identifies the caller's capture binary in
// the provenance manifest; it does not constrain native executable versions.
type Options struct {
	Output         string
	CWD            string
	Env            []string
	ClearEnv       bool
	Command        []string
	NativeVersion  string
	ProxyVersion   string
	StdinEOFAction string
	Timeout        time.Duration
	ProtocolBytes  int64
	StderrBytes    int64
	MaxFrames      int64
	MaxFrameBytes  int64
	CleanupTimeout time.Duration
}

// IO supplies process descriptors so capture can interrupt blocked reads and
// writes. Run owns their use for one invocation, not the caller's descriptors.
type IO struct{ In, Out, Err *os.File }

// ErrorKind describes capture outcomes; callers choose their own exit codes.
type ErrorKind string

const (
	Configuration ErrorKind = "configuration"
	Canceled      ErrorKind = "canceled"
	Failure       ErrorKind = "failure"
)

// Error retains the underlying failure and its capture outcome.
type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func (o Options) validate() error {
	if o.Output == "" || len(o.Command) == 0 || o.Command[0] == "" {
		return errors.New("capture requires an output directory and executable")
	}
	if o.Timeout < 0 || o.CleanupTimeout <= 0 {
		return errors.New("capture timeout must be nonnegative and cleanup timeout positive")
	}
	if o.ProtocolBytes < 0 || o.StderrBytes < 0 || o.MaxFrames < 0 || o.MaxFrameBytes < 0 {
		return errors.New("capture limits must be nonnegative")
	}
	if o.StdinEOFAction != "close" && o.StdinEOFAction != "interrupt" {
		return errors.New("stdin EOF action must be close or interrupt")
	}
	seen := map[string]bool{}
	for _, env := range o.Env {
		key, value, ok := strings.Cut(env, "=")
		if !ok || key == "" || strings.ContainsRune(key+value, 0) || seen[key] {
			return errors.New("environment overrides require unique nonempty KEY=VALUE")
		}
		seen[key] = true
	}
	return nil
}
