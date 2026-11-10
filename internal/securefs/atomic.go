package securefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type CommitState uint8

const (
	CommitNotCommitted CommitState = iota
	CommitCommitted
	CommitDurabilityUncertain
)

type AtomicWriteError struct {
	State CommitState
	Err   error
}

func (e *AtomicWriteError) Error() string { return e.Err.Error() }
func (e *AtomicWriteError) Unwrap() error { return e.Err }

func AtomicWriteCommitState(err error) CommitState {
	if err == nil {
		return CommitCommitted
	}
	var writeErr *AtomicWriteError
	if errors.As(err, &writeErr) {
		return writeErr.State
	}
	return CommitNotCommitted
}

func atomicWriteFailure(state CommitState, format string, args ...any) error {
	return &AtomicWriteError{State: state, Err: fmt.Errorf(format, args...)}
}

type atomicTempFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type atomicWriteOps struct {
	createTemp func(string, string) (atomicTempFile, error)
	rename     func(string, string) error
	syncParent func(string) error
}

func defaultAtomicWriteOps() atomicWriteOps {
	return atomicWriteOps{
		createTemp: func(dir, pattern string) (atomicTempFile, error) { return os.CreateTemp(dir, pattern) },
		rename:     os.Rename,
		syncParent: syncParentDirectory,
	}
}

func AtomicWriteFile(path string, data []byte) error {
	return atomicWriteFileWithOps(path, data, defaultAtomicWriteOps())
}

func atomicWriteFile(path string, data []byte, syncParent func(string) error) error {
	ops := defaultAtomicWriteOps()
	ops.syncParent = syncParent
	return atomicWriteFileWithOps(path, data, ops)
}

func atomicWriteFileWithOps(path string, data []byte, ops atomicWriteOps) error {
	dir := filepath.Dir(path)
	if err := EnsurePrivateDir(dir); err != nil {
		return atomicWriteFailure(CommitNotCommitted, "prepare parent directory: %w", err)
	}
	if err := EnsurePrivateFile(path); err != nil {
		return atomicWriteFailure(CommitNotCommitted, "prepare target file: %w", err)
	}
	if ops.createTemp == nil || ops.rename == nil || ops.syncParent == nil {
		return atomicWriteFailure(CommitNotCommitted, "atomic write operations are incomplete")
	}
	f, err := ops.createTemp(dir, ".codexpool-tmp-*")
	if err != nil {
		return atomicWriteFailure(CommitNotCommitted, "create temp file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(FileMode); err != nil {
		_ = f.Close()
		return atomicWriteFailure(CommitNotCommitted, "chmod temp file: %w", err)
	}
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return atomicWriteFailure(CommitNotCommitted, "write temp file: %w", err)
	}
	if n != len(data) {
		_ = f.Close()
		return atomicWriteFailure(CommitNotCommitted, "write temp file: %w (%d of %d bytes)", io.ErrShortWrite, n, len(data))
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return atomicWriteFailure(CommitNotCommitted, "sync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return atomicWriteFailure(CommitNotCommitted, "close temp file: %w", err)
	}
	if err := ops.rename(tmp, path); err != nil {
		return atomicWriteFailure(CommitNotCommitted, "replace %q: %w", path, err)
	}
	if err := ops.syncParent(dir); err != nil {
		return atomicWriteFailure(CommitDurabilityUncertain, "replacement is visible but parent-directory durability is uncertain: %w", err)
	}
	return nil
}

func syncParentDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent directory %q: %w", dir, err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("sync parent directory %q: %w", dir, err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close parent directory %q: %w", dir, err)
	}
	return nil
}
