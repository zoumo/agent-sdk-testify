package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/rawjson"
	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

type failure struct {
	reason string
	err    error
}

func (f *failure) Error() string          { return f.err.Error() }
func (f *failure) Unwrap() error          { return f.err }
func fail(reason string, err error) error { return &failure{reason, err} }
func reason(err error) string {
	var f *failure
	if errors.As(err, &f) {
		return f.reason
	}
	return "transport-error"
}

// The lock serializes observation counters and envelope writes, never pipe I/O.
// In particular, blocked client output cannot prevent input from reaching a child.
type journal struct {
	mu                                 sync.Mutex
	options                            Options
	start                              time.Time
	raw, stderr                        *os.File
	frames, protocolBytes, stderrBytes int64
	artifacts                          []string
}

func createPrivate(dir, name string) (*os.File, error) {
	return os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
func openJournal(o Options, start time.Time) (*journal, error) {
	j := &journal{options: o, start: start}
	var err error
	j.raw, err = createPrivate(o.Output, "raw.jsonl")
	if err != nil {
		return j, err
	}
	j.artifacts = append(j.artifacts, "raw.jsonl")
	j.stderr, err = createPrivate(o.Output, "stderr.raw")
	if err == nil {
		j.artifacts = append(j.artifacts, "stderr.raw")
	}
	return j, err
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		data = data[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
func (j *journal) observed(n int, stderr bool) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	counter, limit := &j.protocolBytes, j.options.ProtocolBytes
	if stderr {
		counter, limit = &j.stderrBytes, j.options.StderrBytes
	}
	before := *counter
	*counter += int64(n)
	if limit > 0 && *counter > limit {
		keep := limit - before
		if keep < 0 {
			keep = 0
		}
		return int(keep), fail("limit", fmt.Errorf("observed %s byte limit %d exceeded", map[bool]string{true: "stderr", false: "protocol"}[stderr], limit))
	}
	return n, nil
}
func (j *journal) frame(direction recording.Direction, payload []byte) (int64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	ordinal := j.frames + 1
	describe := func(err error) (int64, error) { return ordinal, fmt.Errorf("%s frame %d: %w", direction, ordinal, err) }
	if j.options.MaxFrames > 0 && ordinal > j.options.MaxFrames {
		return describe(fail("limit", fmt.Errorf("frame limit %d exceeded", j.options.MaxFrames)))
	}
	value, err := rawjson.Parse(payload)
	if err != nil {
		return describe(err)
	}
	if value.Kind() != rawjson.Object {
		return describe(errors.New("native frame must be a JSON object"))
	}
	data, err := recording.EncodeFrame(recording.Frame{Ordinal: ordinal, Direction: direction, Channel: "protocol", Epoch: 1, ElapsedMS: time.Since(j.start).Milliseconds(), Wire: value})
	if err != nil {
		return describe(err)
	}
	if err = writeAll(j.raw, data); err != nil {
		return describe(err)
	}
	j.frames++
	return ordinal, nil
}
func (j *journal) partial(direction recording.Direction, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if max := j.options.MaxFrameBytes; max > 0 && int64(len(data)) > max {
		data = data[:max]
	}
	name := string(direction) + ".partial.raw"
	file, err := createPrivate(j.options.Output, name)
	if err != nil {
		return fmt.Errorf("save %s: %w", name, err)
	}
	j.mu.Lock()
	j.artifacts = append(j.artifacts, name)
	j.mu.Unlock()
	return errors.Join(writeAll(file, data), file.Close())
}
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	if err = errors.Join(copyErr, f.Close()); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// finish reserves both metadata files before reporting success. If a later
// metadata write fails, it attempts to mark the already-owned transport file
// failed as well; filesystem failure is always returned to the CLI.
func (j *journal) finish(report *recording.TransportReport, identity recording.NativeIdentity, runErr error) error {
	for _, f := range []*os.File{j.raw, j.stderr} {
		if f != nil {
			runErr = errors.Join(runErr, f.Close())
		}
	}
	report.Frames, report.ProtocolBytes, report.StderrBytes = j.frames, j.protocolBytes, j.stderrBytes
	report.Milliseconds = time.Since(j.start).Milliseconds()
	manifest := recording.Manifest{Version: 1, RecordingFormat: recording.CurrentFormat(), ProxyVersion: j.options.ProxyVersion, Native: identity, Files: map[string]string{}}
	for _, name := range j.artifacts {
		digest, err := hashFile(filepath.Join(j.options.Output, name))
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("hash %s: %w", name, err))
		} else {
			manifest.Files[name] = digest
		}
	}
	transport, err := createPrivate(j.options.Output, "transport.json")
	if err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write transport.json: %w", err))
	}
	manifestFile, err := createPrivate(j.options.Output, "manifest.json")
	if err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("write manifest.json: %w", err))
	}
	updateStatus := func() {
		report.CaptureStatus = "complete"
		if runErr != nil {
			report.CaptureStatus = "failed"
			report.Error = runErr.Error()
			if report.Reason == "EOF" {
				report.Reason = "transport-error"
			}
		}
		manifest.CaptureStatus = report.CaptureStatus
	}
	updateStatus()
	writtenError := report.Error
	writeMetadata := func(file *os.File, value any) error {
		data, err := recording.EncodeReport(value)
		if err != nil {
			return err
		}
		return writeAll(file, data)
	}
	if transport != nil {
		if err = writeMetadata(transport, report); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write transport.json: %w", err))
		}
		if digest, e := hashFile(filepath.Join(j.options.Output, "transport.json")); e != nil {
			runErr = errors.Join(runErr, e)
		} else {
			manifest.Files["transport.json"] = digest
		}
	}
	updateStatus()
	if manifestFile != nil {
		err = writeMetadata(manifestFile, manifest)
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write manifest.json: %w", err))
		}
	}
	if transport != nil {
		// Retain the same exclusively-created inode: never follow a replaced path.
		// When manifest publication fails, the surviving transport report must not
		// claim that the capture was successfully delivered.
		if runErr != nil && writtenError != runErr.Error() {
			updateStatus()
			_, seekErr := transport.Seek(0, 0)
			if seekErr == nil {
				data, encodeErr := recording.EncodeReport(report)
				if encodeErr == nil {
					encodeErr = writeAll(transport, data)
				}
				if encodeErr == nil {
					encodeErr = transport.Truncate(int64(len(data)))
				}
				seekErr = encodeErr
			}
			runErr = errors.Join(runErr, seekErr)
			if digest, hashErr := hashFile(filepath.Join(j.options.Output, "transport.json")); hashErr == nil {
				manifest.Files["transport.json"] = digest
			} else {
				runErr = errors.Join(runErr, hashErr)
			}
			if manifestFile != nil {
				if _, err = manifestFile.Seek(0, 0); err == nil {
					data, encodeErr := recording.EncodeReport(manifest)
					if encodeErr == nil {
						encodeErr = writeAll(manifestFile, data)
					}
					if encodeErr == nil {
						encodeErr = manifestFile.Truncate(int64(len(data)))
					}
					err = encodeErr
				}
				runErr = errors.Join(runErr, err)
			}
		}
		runErr = errors.Join(runErr, transport.Close())
	}
	if manifestFile != nil {
		runErr = errors.Join(runErr, manifestFile.Close())
	}
	return runErr
}
