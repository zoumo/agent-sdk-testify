package capture

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
)

// runRemote uses the same journal workers as local capture. The bridge owns
// only its socket and pipes; there is no native process to launch or reap.
func (j *journal) runRemote(ctx context.Context, streams IO, report *recording.TransportReport) (runErr error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var owned []*os.File
	closeFiles := func() {
		for _, f := range owned {
			_ = f.Close()
		}
	}
	defer closeFiles()
	pipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			owned = append(owned, r, w)
		}
		return r, w, err
	}
	input, toRemote, err := pipe()
	if err != nil {
		return err
	}
	fromRemote, output, err := pipe()
	if err != nil {
		return err
	}
	clientIn, err := interruptible(streams.In)
	if err != nil {
		return err
	}
	owned = append(owned, clientIn)
	clientOut, err := interruptible(streams.Out)
	if err != nil {
		return err
	}
	owned = append(owned, clientOut)
	var stopped atomic.Bool
	results := make(chan workerResult, 3)
	go func() {
		eof, err := j.protocol(clientIn, toRemote, recording.ClientToServer, &stopped)
		_ = toRemote.Close()
		results <- workerResult{name: "input", eof: eof, err: err}
	}()
	go func() {
		eof, err := j.protocol(fromRemote, clientOut, recording.ServerToClient, &stopped)
		results <- workerResult{name: "output", eof: eof, err: err}
	}()
	go func() {
		err := connect(ctx, *j.options.Remote, IO{In: input, Out: output}, report)
		_ = output.Close()
		results <- workerResult{name: "remote", err: err}
	}()
	var drainTimer *time.Timer
	var drainDeadline <-chan time.Time
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()
	outputDone := false
	contextDone := ctx.Done()
	for remaining := 3; remaining > 0; {
		select {
		case result := <-results:
			remaining--
			if result.name == "output" {
				outputDone = true
				drainDeadline = nil
				if drainTimer != nil {
					drainTimer.Stop()
				}
			}
			if result.name == "input" && result.eof && result.err == nil {
				report.ClientEOF = true
			}
			if result.err != nil {
				if runErr == nil {
					report.Reason = reason(result.err)
					if parent.Err() != nil {
						report.Reason = contextReason(parent)
					}
				}
				runErr = errors.Join(runErr, result.err)
				stopped.Store(true)
				if result.name == "remote" {
					// Drain observed bytes, but bound backpressured client output by
					// the same cleanup allowance used by local capture.
					if !outputDone {
						drainTimer = time.NewTimer(j.options.CleanupTimeout)
						drainDeadline = drainTimer.C
					}
					_ = clientIn.Close()
					_ = input.Close()
					_ = toRemote.Close()
				} else {
					cancel()
					closeFiles()
				}
			}
		case <-drainDeadline:
			drainDeadline = nil
			runErr = errors.Join(runErr, errors.New("remote output drain exceeded cleanup deadline"))
			stopped.Store(true)
			cancel()
			closeFiles()
		case <-contextDone:
			contextDone = nil
			if runErr == nil {
				report.Reason = contextReason(ctx)
			}
			runErr = errors.Join(runErr, ctx.Err())
			stopped.Store(true)
			cancel()
			closeFiles()
		}
	}
	report.CleanupComplete = !report.RemoteConnected || report.RemoteClosed
	return runErr
}
