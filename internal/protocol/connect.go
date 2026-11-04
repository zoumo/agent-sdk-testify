package protocol

import (
	"context"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
)

// ConnectOptions and Connect retain the old official bridge until migration.
type ConnectOptions = capture.ConnectOptions

func Connect(ctx context.Context, o ConnectOptions, streams IO) error {
	return capture.Connect(ctx, o, streams)
}
