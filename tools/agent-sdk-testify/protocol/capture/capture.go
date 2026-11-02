// Package capture records provider-independent stdio JSONL with owned process
// lifecycle, lossless native JSON, and explicit failure evidence.
package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

// Run owns streams for one invocation and reports capture failures without CLI
// exit codes. Native version and proxy identity are supplied, never probed.
func Run(ctx context.Context, o Options, streams IO) error {
	if err := o.validate(); err != nil {
		return &Error{Kind: Configuration, Err: err}
	}
	start := time.Now()
	cmd, identity, err := prepare(o)
	if err != nil {
		return &Error{Kind: Configuration, Err: fmt.Errorf("record preflight: %w", err)}
	}
	if err = os.Mkdir(o.Output, 0700); err != nil {
		return &Error{Kind: Configuration, Err: fmt.Errorf("reserve output: %w", err)}
	}
	journal, err := openJournal(o, start)
	report := recording.TransportReport{Version: 1, Reason: "EOF", CleanupComplete: true}
	if err != nil {
		report.Reason = "transport-error"
	} else {
		if o.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, o.Timeout)
			defer cancel()
		}
		err = journal.run(ctx, cmd, streams, &report)
	}
	err = journal.finish(&report, identity, err)
	if err == nil {
		return nil
	}
	kind := Failure
	if report.Reason == "deadline" || report.Reason == "canceled" {
		kind = Canceled
	}
	return &Error{Kind: kind, Err: err}
}

type workerResult struct {
	name string
	err  error
	eof  bool
}

func (j *journal) run(ctx context.Context, cmd *exec.Cmd, streams IO, report *recording.TransportReport) (runErr error) {
	var owned []*os.File
	closeFiles := func(files ...*os.File) {
		for _, file := range files {
			if file != nil {
				_ = file.Close()
			}
		}
	}
	defer func() { closeFiles(owned...) }()
	pipe := func() (*os.File, *os.File, error) {
		r, w, e := os.Pipe()
		if e == nil {
			owned = append(owned, r, w)
		}
		return r, w, e
	}
	childIn, input, err := pipe()
	if err != nil {
		report.Reason = "transport-error"
		return err
	}
	output, childOut, err := pipe()
	if err != nil {
		report.Reason = "transport-error"
		return err
	}
	stderr, childErr, err := pipe()
	if err != nil {
		report.Reason = "transport-error"
		return err
	}
	clientIn, err := interruptible(streams.In)
	if err != nil {
		report.Reason = "transport-error"
		return err
	}
	owned = append(owned, clientIn)
	clientOut, err := interruptible(streams.Out)
	if err != nil {
		report.Reason = "transport-error"
		return err
	}
	owned = append(owned, clientOut)
	if ctx.Err() != nil {
		report.Reason = contextReason(ctx)
		return ctx.Err()
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = childIn, childOut, childErr
	if err = cmd.Start(); err != nil {
		report.Reason = "launch-error"
		return fmt.Errorf("launch native: %w", err)
	}
	report.NativeLaunched = true
	report.CleanupComplete = false
	closeFiles(childIn, childOut, childErr)

	results := make(chan workerResult, 3)
	var inputStopped atomic.Bool
	go func() {
		eof, err := j.protocol(clientIn, input, recording.ClientToServer, &inputStopped)
		results <- workerResult{name: "input", err: err, eof: eof}
	}()
	go func() {
		eof, err := j.protocol(output, clientOut, recording.ServerToClient, &inputStopped)
		results <- workerResult{name: "output", err: err, eof: eof}
	}()
	go func() { results <- workerResult{name: "stderr", err: j.copyStderr(stderr)} }()
	waited := make(chan nativeExit, 1)
	abandonWait := make(chan struct{})
	nativePID := cmd.Process.Pid
	var processMu sync.Mutex
	go func() { waited <- waitNative(cmd, &processMu, abandonWait) }()
	var cleanup <-chan error
	cleanupStarted := false
	beginCleanup := func() {
		if cleanupStarted {
			return
		}
		cleanupStarted = true
		done := make(chan error, 1)
		cleanup = done
		go func() { done <- cleanupGroup(nativePID, j.options.CleanupTimeout) }()
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		inputStopped.Store(true)
		closeFiles(clientIn, clientOut, input, output, stderr)
		beginCleanup()
	}
	failed := func(why string, e error) {
		if runErr == nil {
			report.Reason = why
		}
		runErr = errors.Join(runErr, e)
		stop()
	}
	remaining := 3
	outputDone, stderrDone, waitDone, cleanupDone := false, false, false, false
	cleanupOK := true
	contextDone := ctx.Done()
	for remaining > 0 || !waitDone || !cleanupDone {
		select {
		case result := <-results:
			remaining--
			if result.name == "output" {
				outputDone = true
			}
			if result.name == "stderr" {
				stderrDone = true
			}
			if result.err != nil {
				failed(reason(result.err), result.err)
			}
			if result.name == "input" && result.eof && result.err == nil {
				report.ClientEOF = true
				if !stopped && !waitDone && ctx.Err() == nil {
					if j.options.StdinEOFAction == "interrupt" {
						// Signal only the direct child. Keep its stdin open while
						// its own shutdown handler flushes and exits; neither the
						// recorder nor the child's process group is interrupted.
						processMu.Lock()
						err := cmd.Process.Signal(os.Interrupt)
						processMu.Unlock()
						if err != nil {
							failed("transport-error", fmt.Errorf("interrupt native on client EOF: %w", err))
						} else {
							report.StdinEOFAction = "interrupt"
							report.StdinEOFSignal = "SIGINT"
						}
					} else if err := input.Close(); err != nil {
						failed("transport-error", fmt.Errorf("close native stdin on client EOF: %w", err))
					} else {
						report.StdinEOFAction = "close"
					}
				}
			}
		case exit := <-waited:
			waitDone = true
			waited = nil
			report.Reaped, report.NativeExitCode, report.NativeSignal = exit.reaped, exit.code, exit.signal
			if exit.err != nil {
				// A nonzero exit still leaves valid bytes in the stdout/stderr
				// pipes. Drain them before interrupting the idle client reader.
				if runErr == nil {
					report.Reason = "native-error"
				}
				runErr = errors.Join(runErr, exit.err)
			}
			beginCleanup()
		case err := <-cleanup:
			cleanupDone = true
			cleanup = nil
			if err != nil {
				cleanupOK = false
				failed("cleanup-error", err)
				close(abandonWait)
			}
		case <-contextDone:
			contextDone = nil
			failed(contextReason(ctx), ctx.Err())
		}
		// Only interrupt an idle client after the process exited and all native
		// output drained. Pending fragments still fail in the input worker.
		if waitDone && outputDone && stderrDone {
			inputStopped.Store(true)
			closeFiles(clientIn)
		}
	}
	report.CleanupComplete = cleanupOK && report.Reaped
	return runErr
}
func contextReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "deadline"
	}
	return "canceled"
}
