package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/internal/protocol/replay"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && (os.Args[1] == "capture-kernel" || os.Args[1] == "replay-kernel") {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		cancelTimeout := func() {}
		var err error
		if os.Args[1] == "capture-kernel" {
			var options capture.Options
			err = json.Unmarshal([]byte(os.Args[2]), &options)
			if err == nil {
				err = capture.Run(ctx, options, capture.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
			}
		} else {
			var options replayProcessOptions
			err = json.Unmarshal([]byte(os.Args[2]), &options)
			if err == nil {
				// This is a controlled kernel process, with no material loader,
				// timeout policy, native argument facade or receipt publisher.
				var data []byte
				data, err = os.ReadFile(options.Source)
				if err == nil {
					var document *recording.Document
					document, err = recording.Read(bytes.NewReader(data), options.Provider, options.Limits)
					if err == nil {
						if options.Timeout != 0 {
							ctx, cancelTimeout = context.WithTimeout(ctx, options.Timeout)
						}
						progressFD := -1
						if len(os.Args) == 4 && os.Args[3] == "progress" {
							progressFD = 3
						}
						err = replay.Run(ctx, document, options.Limits, options.Handshakes, os.Stdin, os.Stdout, progressFD)
					}
				}
			}
		}
		cancelTimeout()
		stop()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if os.Getenv("AGENT_MOCK_RECORD_TEST_ROLE") != "" {
		os.Exit(m.Run())
	}
	guard, err := testutil.PrepareRaceGuard()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(guard(m.Run()))
}
