//go:build darwin || linux || freebsd || openbsd || netbsd

package securefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrBusy = errors.New("resource busy")

type Lock struct {
	f *os.File
}

func TryLock(path string) (*Lock, error) {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := EnsurePrivateFile(path); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, FileMode)
	if err != nil {
		return nil, fmt.Errorf("open lock without following symlinks: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("wrap lock file descriptor")
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fstat lock %q: %w", path, err)
	}
	if err := validatePrivateRegularInfo(info, path); err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("lock file %q has overly broad permissions %o", path, info.Mode().Perm())
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	return &Lock{f: f}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	errUnlock := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	errClose := l.f.Close()
	l.f = nil
	if errUnlock != nil {
		return errUnlock
	}
	return errClose
}
