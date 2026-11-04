package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrBusy = errors.New("session action busy")

type Lock struct {
	file *os.File
}

func lockFile(path string, exclusive, wait bool) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if !wait {
		mode |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), mode); err != nil {
		_ = f.Close()
		if !wait && (errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)) {
			return nil, fmt.Errorf("%w: wait for all clients and peers to close", ErrBusy)
		}
		return nil, err
	}
	return &Lock{file: f}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close())
	l.file = nil
	return err
}

func (l *Lock) Downgrade() error {
	if l == nil || l.file == nil {
		return errors.New("missing lock")
	}
	return syscall.Flock(int(l.file.Fd()), syscall.LOCK_SH)
}

func metadataLock(sessionDir string) (*Lock, error) {
	return lockFile(filepath.Join(sessionDir, "locks", "metadata.lock"), true, true)
}

func actionLock(sessionDir string, exclusive bool) (*Lock, error) {
	return lockFile(filepath.Join(sessionDir, "locks", "action.lock"), exclusive, false)
}

func lockExport(file *os.File) (*Lock, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return &Lock{file: file}, nil
}
