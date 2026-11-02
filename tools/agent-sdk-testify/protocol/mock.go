package protocol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/command"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/replay"
)

func runMock(ctx context.Context, options command.MockOptions, streams command.IO) error {
	source, err := os.Open(options.Scenario)
	if err != nil {
		return command.Fail(command.ExitConfiguration, err)
	}
	document, readErr := recording.Read(source, options.Provider, options.Limits)
	err = errors.Join(readErr, source.Close())
	if err != nil {
		return command.Fail(command.ExitConfiguration, err)
	}
	if options.RecordingVersion != "" {
		for _, frame := range document.Frames {
			if value, present := frame.Wire.Member("claude_code_version"); present {
				version, e := value.String()
				if e != nil || version != options.RecordingVersion {
					return command.Fail(command.ExitConfiguration, fmt.Errorf("ordinal %d: native source version mismatch", frame.Ordinal))
				}
			}
		}
	}
	// O_EXCL at publication also protects against a concurrent creator.
	if options.Receipt != "" {
		_, err = os.Lstat(options.Receipt)
		if err == nil {
			return command.Fail(command.ExitConfiguration, errors.New("receipt already exists"))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return command.Fail(command.ExitConfiguration, err)
		}
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = document.Timeout
	}
	if timeout == 0 {
		timeout = 30 * time.Second
		if options.Provider == "claude" {
			timeout = 10 * time.Second
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = replay.Run(ctx, document, options.Limits, options.Handshakes, streams.In, streams.Out, options.ProgressFD)
	if err != nil {
		code := command.ExitFailure
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = command.ExitCanceled
		} else if errors.Is(err, replay.ErrDisconnected) {
			code = command.ExitDisconnect
		}
		return command.Fail(code, err)
	}
	if options.Receipt != "" {
		err = writeMockReceipt(ctx, options.Receipt, recording.Receipt{
			Version: 1, FullConsumption: true, NativeLaunched: false,
			Peer: "Go native mock", Provider: options.Provider, Frames: len(document.Frames),
			RecordingSHA256: document.SHA256, ClientEOF: true,
		})
	} else {
		err = ctx.Err()
	}
	if err != nil {
		code := command.ExitFailure
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = command.ExitCanceled
		}
		return command.Fail(code, err)
	}
	return nil
}

func writeMockReceipt(ctx context.Context, path string, receipt recording.Receipt) error {
	data, err := recording.EncodeReport(receipt)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Close(), ctx.Err())
	if err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}
