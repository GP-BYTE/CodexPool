//go:build darwin || linux || freebsd || openbsd || netbsd

package securefs

import (
	"fmt"
	"os"
	"syscall"
)

func validateOwnedInfo(info os.FileInfo, path string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify owner for %q", path)
	}
	if uint32(os.Geteuid()) != stat.Uid {
		return fmt.Errorf("refuse %q owned by uid %d instead of current uid %d", path, stat.Uid, os.Geteuid())
	}
	return nil
}

func validatePrivateRegularInfo(info os.FileInfo, path string) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse symlink file %q", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse non-regular file %q", path)
	}
	if err := validateOwnedInfo(info, path); err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot verify link count for %q", path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("refuse hard-linked private file %q with link count %d", path, stat.Nlink)
	}
	return nil
}

func hardenPrivateDir(path string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open private directory without following symlinks %q: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("wrap private directory descriptor %q", path)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("fstat private directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("private path %q is not a directory", path)
	}
	if err := validateOwnedInfo(info, path); err != nil {
		return err
	}
	if err := syscall.Fchmod(fd, DirMode); err != nil {
		return fmt.Errorf("fchmod private directory %q: %w", path, err)
	}
	return nil
}
