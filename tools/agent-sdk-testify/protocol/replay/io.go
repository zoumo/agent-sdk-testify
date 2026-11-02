package replay

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"

	"github.com/zoumo/agent-sdk-go/tools/agent-sdk-testify/protocol/recording"
)

// Run owns duplicate protocol descriptors and the optional progress descriptor.
// Nonblocking descriptors register with Go's poller: closing them interrupts
// idle reads AND backpressured writes, including inherited process stdio.
// No reader/writer goroutine can outlive this call.
func Run(ctx context.Context, document *recording.Document, limits recording.Limits, handshakes string, input, output *os.File, progressFD int) (err error) {
	if ctx == nil || document == nil || input == nil || output == nil {
		return errors.New("context, document and protocol files required")
	}
	// Acquire the inherited descriptor before creating any new descriptors.
	// Otherwise a missing progress fd could be reused by a protocol duplicate,
	// sending the marker onto stdout or closing the wrong stream.
	var progress *ownedFile
	if progressFD != -1 {
		if progressFD < 3 {
			return errors.New("invalid progress descriptor")
		}
		if err = syscall.SetNonblock(progressFD, true); err != nil {
			return err
		}
		progress = &ownedFile{File: os.NewFile(uintptr(progressFD), "mock-progress")}
		defer func() { err = errors.Join(err, progress.Close()) }()
	}
	in, err := duplicatePollFile(input)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	out, err := duplicatePollFile(output)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, out.Close()) }()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = in.Close()
		_ = out.Close()
		if progress != nil {
			_ = progress.Close()
		}
		close(closed)
	})
	defer func() {
		if !stop() {
			<-closed
		}
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
	}()
	var complete func() error
	if progress != nil {
		complete = func() error {
			_, writeErr := progress.Write([]byte{'1'})
			return errors.Join(writeErr, progress.Close())
		}
	}
	return play(ctx, document, limits, handshakes, in, out, complete)
}

type ownedFile struct {
	*os.File
	once sync.Once
	err  error
}

func (f *ownedFile) Close() error {
	f.once.Do(func() { f.err = f.File.Close() })
	return f.err
}

func duplicatePollFile(source *os.File) (*ownedFile, error) {
	raw, err := source.SyscallConn()
	if err != nil {
		return nil, err
	}
	fd := -1
	var duplicateErr error
	err = raw.Control(func(original uintptr) { fd, duplicateErr = syscall.Dup(int(original)) })
	if err = errors.Join(err, duplicateErr); err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err = syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	return &ownedFile{File: os.NewFile(uintptr(fd), source.Name()+" (mock)")}, nil
}
