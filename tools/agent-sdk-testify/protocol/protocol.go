// Package protocol owns recording, strict native replay and transport bridging.
// It has no case selection, driver assertions or SDK dependencies.
package protocol

import (
	"context"
	"errors"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/capture"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/command"
)

// Execute runs a protocol operation using caller-owned cancellation and streams.
func Execute(ctx context.Context, args []string, streams command.IO) int {
	return command.Execute(ctx, args, command.Handlers{Record: runRecord, Mock: runMock, Connect: runConnect}, streams)
}

// record maps the CLI contract at its boundary; capture has no command dependency.
func runRecord(ctx context.Context, o command.RecordOptions, streams command.IO) error {
	err := capture.Run(ctx, capture.Options{
		Output: o.Output, CWD: o.CWD, Env: o.Env, ClearEnv: o.ClearEnv,
		Command: o.Command, NativeVersion: o.NativeVersion, ProxyVersion: command.Version,
		StdinEOFAction: o.StdinEOFAction, Timeout: o.Timeout,
		ProtocolBytes: o.ProtocolBytes, StderrBytes: o.StderrBytes,
		MaxFrames: o.MaxFrames, MaxFrameBytes: o.MaxFrameBytes, CleanupTimeout: o.CleanupTimeout,
	}, capture.IO{In: streams.In, Out: streams.Out, Err: streams.Err})
	if err == nil {
		return nil
	}
	code := command.ExitFailure
	var failure *capture.Error
	if errors.As(err, &failure) {
		switch failure.Kind {
		case capture.Configuration:
			code = command.ExitConfiguration
		case capture.Canceled:
			code = command.ExitCanceled
		}
	}
	return command.Fail(code, err)
}
