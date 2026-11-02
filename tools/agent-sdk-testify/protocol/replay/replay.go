package replay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/rawjson"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

var ErrDisconnected = errors.New("scripted disconnect")

// play keeps transport mechanics separate from the two native ID contracts.
// Run supplies cancelable owned files; tests may supply finite in-memory streams.
func play(ctx context.Context, document *recording.Document, limits recording.Limits, handshakes string, input io.Reader, output io.Writer, complete func() error) (err error) {
	a, err := newAdapter(document.Provider)
	if err != nil {
		return err
	}
	if handshakes != "" && (document.Provider != "codex" || (handshakes != "answer" && handshakes != "answer-and-assert")) {
		return errors.New("invalid SDK test handshakes")
	}
	limits, err = limits.Resolve(document.Provider)
	if err != nil {
		return err
	}
	p := peerIO{input: bufio.NewReader(input), output: output, limits: limits}
	var ordinal int64
	defer func() {
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		if err != nil && ordinal != 0 {
			err = fmt.Errorf("ordinal %d: %w", ordinal, err)
		}
	}()
	for _, frame := range document.Frames {
		ordinal = frame.Ordinal
		if err = ctx.Err(); err != nil {
			return err
		}
		if frame.Delay != 0 {
			timer := time.NewTimer(frame.Delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if frame.Disconnect {
			return ErrDisconnected
		}
		if frame.Direction == recording.ClientToServer {
			actual, err := p.read()
			if err != nil {
				return err
			}
			reverse, err := a.client(frame.Wire, actual)
			if err != nil {
				return err
			}
			if reverse && handshakes != "" {
				if err = p.barrier("replay/answer"); err != nil {
					return err
				}
			}
		} else {
			emitted, err := a.server(frame.Wire)
			if err != nil {
				return err
			}
			if err = p.write(emitted.Raw()); err != nil {
				return err
			}
		}
	}
	if err = a.complete(); err != nil {
		return err
	}
	if handshakes == "answer-and-assert" {
		if err = p.barrier("replay/assert"); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Consumption is intentionally observable BEFORE EOF, but never before the
	// pending request check. It lets the caller close input without deadlocking.
	if complete != nil {
		if err = complete(); err != nil {
			return err
		}
	}
	if _, err = p.input.ReadByte(); err == nil {
		return errors.New("extra client byte after scenario")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

type peerIO struct {
	input                 *bufio.Reader
	output                io.Writer
	limits                recording.Limits
	readBytes, writeBytes int64
}

func (p *peerIO) read() (rawjson.Value, error) {
	var line []byte
	for {
		part, err := p.input.ReadSlice('\n')
		if int64(len(part)) > p.limits.MaxBytes-p.readBytes {
			return rawjson.Value{}, errors.New("client traffic byte limit exceeded")
		}
		p.readBytes += int64(len(part))
		// Allow the exclusive payload bound plus at most CR and LF. This
		// bounds accumulation even for an attacker that never sends a newline.
		if int64(len(line))+int64(len(part))-1 > p.limits.MaxFrameBytes {
			return rawjson.Value{}, errors.New("client frame byte limit exceeded")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return rawjson.Value{}, err
		}
		if len(line) == 0 {
			return rawjson.Value{}, errors.New("early client EOF")
		}
		break
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if int64(len(line)) >= p.limits.MaxFrameBytes {
		return rawjson.Value{}, errors.New("client frame byte limit exceeded")
	}
	value, err := rawjson.Parse(line)
	if err != nil {
		return rawjson.Value{}, err
	}
	if value.Kind() != rawjson.Object {
		return rawjson.Value{}, errors.New("client JSON must be an object")
	}
	return value, nil
}

func (p *peerIO) write(payload []byte) error {
	if int64(len(payload)) >= p.limits.MaxFrameBytes {
		return errors.New("server frame byte limit exceeded")
	}
	data := append(payload, '\n')
	if int64(len(data)) > p.limits.MaxBytes-p.writeBytes {
		return errors.New("server traffic byte limit exceeded")
	}
	p.writeBytes += int64(len(data))
	n, err := p.output.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func (p *peerIO) barrier(method string) error {
	request, err := p.read()
	if err != nil {
		return fmt.Errorf("SDK test synchronization %s: %w", method, err)
	}
	value := member(request, "id")
	if len(request.Keys()) != 2 || stringMember(request, "method") != method {
		return errors.New("missing exact SDK test synchronization " + method)
	}
	if _, err = identity(value, false); err != nil {
		return err
	}
	response := append([]byte(`{"id":`), value.Raw()...)
	response = append(response, []byte(`,"result":{}}`)...)
	return p.write(response)
}
