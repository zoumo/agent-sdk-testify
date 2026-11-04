package protocol

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zoumo/agent-sdk-testify/protocol/recording"
	"github.com/zoumo/agent-sdk-testify/protocol/replay"
)

type MockOptions struct {
	Provider         string
	Scenario         string
	Receipt          string
	Timeout          time.Duration
	Limits           recording.Limits
	Handshakes       string
	ProgressFD       int
	RecordingVersion string
	NativeArgs       []string
}

func Mock(ctx context.Context, options MockOptions, streams IO) error {
	source, err := os.Open(options.Scenario)
	if err != nil {
		return err
	}
	document, readErr := recording.Read(source, options.Provider, options.Limits)
	err = errors.Join(readErr, source.Close())
	if err != nil {
		return err
	}
	if options.RecordingVersion != "" {
		for _, frame := range document.Frames {
			if value, present := frame.Wire.Member("claude_code_version"); present {
				version, e := value.String()
				if e != nil || version != options.RecordingVersion {
					return fmt.Errorf("ordinal %d: native source version mismatch", frame.Ordinal)
				}
			}
		}
	}
	// O_EXCL at publication also protects against a concurrent creator.
	if options.Receipt != "" {
		_, err = os.Lstat(options.Receipt)
		if err == nil {
			return errors.New("receipt already exists")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
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
		return err
	}
	if options.Receipt != "" {
		err = writeMockReceipt(ctx, options.Receipt, recording.Receipt{
			Version: 1, Status: "passed", FullConsumption: true, NativeLaunched: false,
			Peer: "Go native mock", Provider: options.Provider, Frames: len(document.Frames),
			RecordingSHA256: document.SHA256, ClientEOF: true, CleanupComplete: true,
		})
	} else {
		err = ctx.Err()
	}
	return err
}

func writeMockReceipt(ctx context.Context, path string, receipt recording.Receipt) error {
	data, err := recording.EncodeReport(receipt)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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
