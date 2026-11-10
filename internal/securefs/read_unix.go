//go:build darwin || linux || freebsd || openbsd || netbsd

package securefs

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

func ReadPrivateFile(path string, maxBytes int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("wrap private file descriptor for %q", path)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("fstat private file %q: %w", path, err)
	}
	if err := validatePrivateRegularInfo(info, path); err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("private file %q has overly broad permissions %o", path, info.Mode().Perm())
	}
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read private file %q: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("private file %q exceeds %d bytes", path, maxBytes)
	}
	return data, nil
}
