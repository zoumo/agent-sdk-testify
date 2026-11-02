package capture

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

func (j *journal) protocol(src, dst *os.File, direction recording.Direction, inputStopped *atomic.Bool) (eof bool, err error) {
	var pending []byte
	defer func() { err = errors.Join(err, j.partial(direction, pending)) }()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buffer)
		allowed, limitErr := j.observed(n, false)
		chunk := buffer[:allowed]
		for len(chunk) > 0 {
			end := bytes.IndexByte(chunk, '\n')
			size := len(chunk)
			complete := end >= 0
			if complete {
				size = end + 1
			}
			piece := chunk[:size]
			chunk = chunk[size:]
			// Keep only the permitted prefix on an explicit frame-size failure. CR
			// immediately before LF is framing, but any other CR is payload.
			payloadSize := len(pending) + len(piece)
			if complete {
				payloadSize--
			}
			last := byte(0)
			if complete && len(piece) >= 2 {
				last = piece[len(piece)-2]
			} else if complete && len(pending) > 0 {
				last = pending[len(pending)-1]
			} else if !complete {
				last = piece[len(piece)-1]
			}
			if last == '\r' {
				payloadSize--
			}
			if max := j.options.MaxFrameBytes; max > 0 && int64(payloadSize) > max {
				keep := max - int64(len(pending))
				if keep > 0 {
					pending = append(pending, piece[:min(int64(len(piece)), keep)]...)
				}
				return false, fail("limit", fmt.Errorf("%s native frame payload exceeds %d bytes", direction, max))
			}
			pending = append(pending, piece...)
			if !complete {
				continue
			}
			payload := pending[:len(pending)-1]
			if len(payload) > 0 && payload[len(payload)-1] == '\r' {
				payload = payload[:len(payload)-1]
			}
			ordinal, e := j.frame(direction, payload)
			if e != nil {
				return false, e
			}
			// Retain the complete observed frame even if forwarding subsequently fails.
			line := pending
			pending = nil
			if e = writeAll(dst, line); e != nil {
				return false, fmt.Errorf("%s frame %d forwarding: %w", direction, ordinal, e)
			}
		}
		if limitErr != nil {
			return false, limitErr
		}
		if readErr != nil {
			if len(pending) > 0 {
				return false, fmt.Errorf("%s unterminated native frame (%d bytes): %w", direction, len(pending), readErr)
			}
			if errors.Is(readErr, io.EOF) {
				return true, nil
			}
			if direction == recording.ClientToServer && inputStopped.Load() && errors.Is(readErr, os.ErrClosed) {
				return false, nil
			}
			return false, fmt.Errorf("%s read: %w", direction, readErr)
		}
	}
}
func (j *journal) copyStderr(src *os.File) error {
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buffer)
		allowed, limitErr := j.observed(n, true)
		if err := writeAll(j.stderr, buffer[:allowed]); err != nil {
			return fmt.Errorf("save native stderr: %w", err)
		}
		if limitErr != nil {
			return limitErr
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("native stderr read: %w", readErr)
		}
	}
}
