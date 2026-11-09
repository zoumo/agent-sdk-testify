package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"github.com/zoumo/agent-sdk-testify/internal/corpus"
	"github.com/zoumo/agent-sdk-testify/internal/lifecycle"
	"github.com/zoumo/agent-sdk-testify/internal/mock"
	"github.com/zoumo/agent-sdk-testify/internal/tool"
)

// StartMockStreams are the fixture-side closeable streams transferred to a
// StartMockAgent run.
type StartMockStreams struct {
	Reader io.ReadCloser
	Writer io.WriteCloser
	Stderr io.Writer
}

// ExecutionStart is the immutable public projection of one claimed execution
// overlay.
type ExecutionStart struct {
	FixtureID       string
	ExecutionID     string
	ConnectionID    string
	Material        corpus.Material
	ExecutionSHA256 string
	ExecutionPath   string

	Workspace    string
	ProtocolCwd  string
	Home         string
	Config       string
	Tmp          string
	HistoryFiles map[string]string
	Env          map[string]string
	NativeArgs   []string
	Transport    lifecycle.MockTransport
}

// MockRun is an in-process mock connection handle.
type MockRun struct {
	Start ExecutionStart

	runCtx      context.Context
	cancel      context.CancelCauseFunc
	streams     *inProcessStreamCloser
	stderr      io.Writer
	release     chan struct{}
	full        chan struct{}
	fullOnce    sync.Once
	done        chan struct{}
	finishOnce  sync.Once
	claimDone   chan struct{}
	claimOnce   sync.Once
	stopOnce    sync.Once
	mu          sync.RWMutex
	claimed     bool
	finished    bool
	stopReason  error
	stopErr     error
	terminal    *lifecycle.MockResult
	terminalErr error
}

type startSignal struct {
	start ExecutionStart
	err   error
}

// StartMock validates, claims, writes running evidence and exposes the overlay
// before releasing replay.
func StartMock(ctx context.Context, o MockOptions, streams StartMockStreams) (*MockRun, error) {
	if ctx == nil {
		return nil, errors.New("mock requires a context")
	}
	if o.FixtureDir == "" {
		return nil, errors.New("mock requires --fixture-dir")
	}
	if !filepath.IsAbs(o.FixtureDir) {
		return nil, errors.New("--fixture-dir must be an absolute path")
	}
	if o.Timeout < 0 {
		return nil, errors.New("mock timeout must be nonnegative")
	}
	if nilStream(streams.Reader) || nilStream(streams.Writer) || nilStream(streams.Stderr) {
		return nil, errors.New("mock requires input, output and error streams")
	}
	if sameStream(streams.Reader, streams.Writer) {
		return nil, errors.New("mock reader and writer must be separate streams")
	}
	prepared, err := prepareInProcessMock(o)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	run := &MockRun{
		runCtx:    runCtx,
		cancel:    cancel,
		streams:   &inProcessStreamCloser{reader: streams.Reader, writer: streams.Writer},
		stderr:    streams.Stderr,
		release:   make(chan struct{}),
		full:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		claimDone: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(run.release) })
	}
	started := make(chan startSignal, 1)
	go func() {
		select {
		case <-ctx.Done():
			select {
			case <-run.claimDone:
				_ = run.stop(lifecycle.ContextFailureClass(ctx.Err()), ctx.Err())
			case <-run.done:
			}
		case <-run.done:
		}
	}()
	go func() {
		result, runErr := mock.RunInProcess(runCtx, prepared, mock.InProcessStreams{
			Reader: streams.Reader,
			Writer: streams.Writer,
			Stderr: streams.Stderr,
		}, mock.InProcessCallbacks{
			Started: func(state lifecycle.MockContext) error {
				run.markClaimed()
				start, err := executionStart(state)
				if err != nil {
					err = lifecycle.ClassifyFailure(lifecycle.FailureClassInternal, err)
					started <- startSignal{err: err}
					return err
				}
				started <- startSignal{start: start}
				<-run.release
				return nil
			},
			FullConsumption: run.markFullConsumption,
			CloseStreams:    run.streams.Close,
			StopCause:       run.stopCause,
		})
		run.finish(result, runErr)
	}()

	var signal startSignal
	haveStart := false
	select {
	case signal = <-started:
		haveStart = true
	case <-run.done:
		select {
		case signal = <-started:
			haveStart = true
		default:
		}
	case <-ctx.Done():
		select {
		case signal = <-started:
			haveStart = true
		default:
		}
	}
	if haveStart {
		if signal.err == nil {
			run.Start = signal.start
		}
	}
	release()
	if haveStart && signal.err != nil {
		<-run.done
		err := run.terminalError()
		return run, err
	}
	if !haveStart {
		if run.wasClaimed() {
			_ = run.stop(lifecycle.ContextFailureClass(ctx.Err()), ctx.Err())
			<-run.done
			err := run.terminalError()
			return run, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err := run.terminalError()
		if err == nil {
			err = errors.New("mock failed before start")
		}
		return nil, err
	}
	if ctx.Err() != nil {
		_ = run.stop(lifecycle.ContextFailureClass(ctx.Err()), ctx.Err())
		<-run.done
		err := run.terminalError()
		return run, err
	}
	return run, nil
}

// FullConsumption reports the one-shot safe point for SDK input half-close.
func (r *MockRun) FullConsumption() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.full
}

// Done closes after replay, protocol receipt and lifecycle finalization for
// this connection have ended.
func (r *MockRun) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}

// Wait is the authoritative repeatable terminal-result path.
func (r *MockRun) Wait(ctx context.Context) (*lifecycle.MockResult, error) {
	if r == nil {
		return nil, errors.New("mock run is nil")
	}
	if ctx == nil {
		return nil, errors.New("mock wait requires a context")
	}
	select {
	case <-r.done:
		result, err := r.terminalSnapshot()
		return result, err
	default:
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		select {
		case <-r.done:
		default:
			_ = r.stop(lifecycle.ContextFailureClass(ctx.Err()), ctx.Err())
		}
		<-r.done
	}
	result, err := r.terminalSnapshot()
	return result, err
}

// Abort requests cancellation and closes fixture-side streams. It is
// idempotent; Wait remains the only final-report path.
func (r *MockRun) Abort() error {
	if r == nil {
		return errors.New("mock run is nil")
	}
	return r.stop(lifecycle.FailureClassAborted, errors.New("mock run aborted"))
}

func (r *MockRun) markFullConsumption() {
	r.fullOnce.Do(func() {
		r.full <- struct{}{}
		close(r.full)
	})
}

func (r *MockRun) stop(class lifecycle.FailureClass, cause error) error {
	r.mu.RLock()
	finished := r.finished
	r.mu.RUnlock()
	if finished {
		return nil
	}
	r.stopOnce.Do(func() {
		classified := lifecycle.ClassifyFailure(class, cause)
		r.mu.Lock()
		r.stopReason = classified
		r.mu.Unlock()
		r.cancel(classified)
		closeErr := r.streams.Close(classified)
		r.mu.Lock()
		r.stopErr = closeErr
		r.mu.Unlock()
	})
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stopErr
}

func (r *MockRun) stopCause() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stopReason
}

func (r *MockRun) finish(result *lifecycle.MockResult, runErr error) {
	r.finishOnce.Do(func() {
		if !r.streams.Closed() {
			runErr = errors.Join(runErr, r.streams.Close(runErr))
		}
		if runErr != nil {
			if lifecycle.FailureClassOf(runErr) == "" {
				class := lifecycle.FailureClassCleanup
				if result == nil {
					class = lifecycle.FailureClassInternal
				}
				runErr = lifecycle.ClassifyFailure(class, runErr)
			}
			if r.stderr != nil {
				_, _ = fmt.Fprintf(r.stderr, "mock: %v\n", runErr)
			}
		}
		r.mu.Lock()
		if result != nil {
			r.claimed = true
		}
		r.finished = true
		r.terminal = result
		r.terminalErr = runErr
		r.mu.Unlock()
		r.cancel(nil)
		close(r.done)
	})
}

func (r *MockRun) markClaimed() {
	r.mu.Lock()
	r.claimed = true
	r.mu.Unlock()
	r.claimOnce.Do(func() { close(r.claimDone) })
}

func (r *MockRun) wasClaimed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.claimed
}

func (r *MockRun) terminalError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.terminalErr
}

func (r *MockRun) terminalSnapshot() (*lifecycle.MockResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneMockResult(r.terminal), r.terminalErr
}

func executionStart(run lifecycle.MockContext) (ExecutionStart, error) {
	execution := run.Execution()
	hash, err := tool.FileDigest(filepath.Join(run.ExecutionDir(), "execution.json"))
	if err != nil {
		return ExecutionStart{}, err
	}
	transport := lifecycle.MockTransport("")
	for _, connection := range execution.Connections {
		if connection.ID == run.Connection() {
			transport = lifecycle.MockTransport(connection.Transport)
			break
		}
	}
	if transport == "" {
		return ExecutionStart{}, errors.New("mock connection is absent from execution")
	}
	return ExecutionStart{
		FixtureID:       execution.FixtureID,
		ExecutionID:     execution.ExecutionID,
		ConnectionID:    run.Connection(),
		Material:        run.Artifact(),
		ExecutionSHA256: hash,
		ExecutionPath:   filepath.Join(run.ExecutionDir(), "execution.json"),
		Workspace:       execution.Workspace,
		ProtocolCwd:     execution.ProtocolCwd,
		Home:            execution.Home,
		Config:          execution.Config,
		Tmp:             execution.Tmp,
		HistoryFiles:    maps.Clone(execution.HistoryFiles),
		Env:             maps.Clone(execution.Env),
		NativeArgs:      slices.Clone(execution.NativeArgs),
		Transport:       transport,
	}, nil
}

func cloneMockResult(source *lifecycle.MockResult) *lifecycle.MockResult {
	if source == nil {
		return nil
	}
	clone := *source
	if source.Material != nil {
		material := *source.Material
		material.Files = maps.Clone(source.Material.Files)
		material.Connections = slices.Clone(source.Material.Connections)
		if source.Material.OfficialOrigin != nil {
			origin := *source.Material.OfficialOrigin
			origin.Connections = slices.Clone(source.Material.OfficialOrigin.Connections)
			material.OfficialOrigin = &origin
		}
		clone.Material = &material
	}
	if source.Execution != nil {
		report := *source.Execution
		report.Material.Files = maps.Clone(source.Execution.Material.Files)
		report.Material.Connections = slices.Clone(source.Execution.Material.Connections)
		if source.Execution.Material.OfficialOrigin != nil {
			origin := *source.Execution.Material.OfficialOrigin
			origin.Connections = slices.Clone(source.Execution.Material.OfficialOrigin.Connections)
			report.Material.OfficialOrigin = &origin
		}
		report.Connections = slices.Clone(source.Execution.Connections)
		for i := range report.Connections {
			if report.Connections[i].ExitCode != nil {
				code := *report.Connections[i].ExitCode
				report.Connections[i].ExitCode = &code
			}
		}
		clone.Execution = &report
	}
	return &clone
}

func nilStream(stream any) bool {
	if stream == nil {
		return true
	}
	value := reflect.ValueOf(stream)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func sameStream(left, right any) bool {
	leftValue, rightValue := reflect.ValueOf(left), reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() {
		return false
	}
	if leftValue.Type().Comparable() {
		return leftValue.Interface() == rightValue.Interface()
	}
	if leftValue.Kind() == reflect.Pointer {
		return leftValue.Pointer() == rightValue.Pointer()
	}
	return false
}

type inProcessStreamCloser struct {
	mu     sync.Mutex
	reader io.ReadCloser
	writer io.WriteCloser
	closed bool
	err    error
}

func (s *inProcessStreamCloser) Close(cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.err
	}
	s.closed = true
	writerErr := closeStream(s.writer, cause)
	readerErr := closeStream(s.reader, cause)
	s.err = errors.Join(writerErr, readerErr)
	return s.err
}

func (s *inProcessStreamCloser) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func closeStream(stream io.Closer, cause error) error {
	if cause != nil {
		if closeWithError, ok := stream.(interface{ CloseWithError(error) error }); ok {
			return closeWithError.CloseWithError(cause)
		}
	}
	return stream.Close()
}
