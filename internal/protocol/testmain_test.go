package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/zoumo/agent-sdk-testify/internal/protocol/capture"
	"github.com/zoumo/agent-sdk-testify/internal/testutil"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && (os.Args[1] == "capture-kernel" || os.Args[1] == "replay-kernel") {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		var err error
		if os.Args[1] == "capture-kernel" {
			var options capture.Options
			err = json.Unmarshal([]byte(os.Args[2]), &options)
			if err == nil {
				err = capture.Run(ctx, options, capture.IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
			}
		} else {
			var options MockOptions
			err = json.Unmarshal([]byte(os.Args[2]), &options)
			if err == nil {
				err = ValidateNativeArgs(options.Provider, options.NativeArgs)
			}
			if err == nil {
				err = Mock(ctx, options, IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr})
			}
		}
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
	cleanup, err := testutil.PrepareBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
