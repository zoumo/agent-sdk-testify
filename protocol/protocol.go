// Package protocol owns recording, strict native replay and transport bridging.
// It has no case selection, driver assertions, command parsing or SDK
// dependencies.
package protocol

import (
	"context"
	"time"

	"github.com/zoumo/agent-sdk-testify/internal/version"
	"github.com/zoumo/agent-sdk-testify/protocol/capture"
)

type RecordOptions struct {
	Output         string
	CWD            string
	Env            []string
	ClearEnv       bool
	Command        []string
	NativeVersion  string
	StdinEOFAction string
	Timeout        time.Duration
	ProtocolBytes  int64
	StderrBytes    int64
	MaxFrames      int64
	MaxFrameBytes  int64
	CleanupTimeout time.Duration
}

func Record(ctx context.Context, o RecordOptions, streams IO) error {
	return capture.Run(ctx, capture.Options{
		Output: o.Output, CWD: o.CWD, Env: o.Env, ClearEnv: o.ClearEnv,
		Command: o.Command, NativeVersion: o.NativeVersion, ProxyVersion: version.Version,
		StdinEOFAction: o.StdinEOFAction, Timeout: o.Timeout,
		ProtocolBytes: o.ProtocolBytes, StderrBytes: o.StderrBytes,
		MaxFrames: o.MaxFrames, MaxFrameBytes: o.MaxFrameBytes, CleanupTimeout: o.CleanupTimeout,
	}, streams)
}
