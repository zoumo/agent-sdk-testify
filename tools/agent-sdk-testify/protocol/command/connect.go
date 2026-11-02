package command

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
)

// ConnectOptions selects an existing WebSocket service. The bridge owns only
// its connection, never the service process. The token is read from a private
// file so it does not enter argv, capture metadata, or diagnostics.
type ConnectOptions struct {
	URL           string
	TokenFile     string
	MaxFrameBytes int64
}

func connectCommand(handler func(context.Context, ConnectOptions, IO) error, streams IO) *cobra.Command {
	var o ConnectOptions
	c := &cobra.Command{Use: "connect --url URL --token-file PATH", Short: "Bridge stdio JSONL to one existing authenticated WebSocket connection", Args: cobra.NoArgs}
	c.Flags().StringVar(&o.URL, "url", "", "Explicit ws/wss endpoint")
	c.Flags().StringVar(&o.TokenFile, "token-file", "", "Private bearer-token file")
	c.Flags().Int64Var(&o.MaxFrameBytes, "max-frame-bytes", 4<<20, "Maximum JSON message bytes")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if o.URL == "" || o.TokenFile == "" || o.MaxFrameBytes < 1 || o.MaxFrameBytes > 1<<30 {
			return Fail(ExitConfiguration, errors.New("connect requires an endpoint, token file and positive bounded frame size"))
		}
		if handler == nil {
			return Fail(ExitUnimplemented, errors.New("connect handler unavailable"))
		}
		return handler(c.Context(), o, streams)
	}
	return c
}
