package securefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	DirMode  = 0o700
	FileMode = 0o600
)

func DefaultRoot() (string, error) {
	if v := strings.TrimSpace(os.Getenv("CODEXPOOL_HOME")); v != "" {
		return filepath.Abs(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".codexpool"), nil
}

func EnsurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refuse non-directory or symlink private path %q", path)
		}
		if err := validateOwnedInfo(info, path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private directory %q: %w", path, err)
	} else {
		if err := rejectSymlinkPath(path); err != nil {
			return err
		}
		if err := os.MkdirAll(path, DirMode); err != nil {
			return fmt.Errorf("create private directory %q: %w", path, err)
		}
		info, err = os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect created private directory %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("created private path %q is not a directory", path)
		}
		if err := validateOwnedInfo(info, path); err != nil {
			return err
		}
	}
	if err := hardenPrivateDir(path); err != nil {
		return err
	}
	return nil
}

func EnsurePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat private file %q: %w", path, err)
	}
	if err := validatePrivateRegularInfo(info, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private file %q has overly broad permissions %o", path, info.Mode().Perm())
	}
	return nil
}

func SafeChild(root string, elems ...string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	parts := append([]string{rootAbs}, elems...)
	candidate := filepath.Clean(filepath.Join(parts...))
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes root: %q", candidate)
	}
	return candidate, nil
}

func ValidateManagedPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("managed path escapes root: %q", targetAbs)
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect managed root %q: %w", rootAbs, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("managed root %q must be a real directory", rootAbs)
	}
	if err := validateOwnedInfo(rootInfo, rootAbs); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := rootAbs
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect managed path component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlink inside managed root: %q", current)
		}
		if err := validateOwnedInfo(info, current); err != nil {
			return err
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("managed ancestor %q is not a directory", current)
		}
	}
	return nil
}

func RemoveTree(root, target string) error {
	return removeTree(root, target, nil)
}

func removeTree(root, target string, afterAnchor func() error) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("refuse removal outside managed root: %q", targetAbs)
	}
	if err := ValidateManagedPath(rootAbs, targetAbs); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(rootAbs)
	if err != nil {
		return fmt.Errorf("inspect removal root %q: %w", rootAbs, err)
	}
	targetInfo, err := os.Lstat(targetAbs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect removal target %q: %w", targetAbs, err)
	}
	if targetInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse symlink removal target %q", targetAbs)
	}

	rootHandle, err := os.OpenRoot(rootAbs)
	if err != nil {
		return fmt.Errorf("open managed removal root %q: %w", rootAbs, err)
	}
	defer rootHandle.Close()
	rootFile, err := rootHandle.Open(".")
	if err != nil {
		return fmt.Errorf("open managed removal root anchor: %w", err)
	}
	anchoredRootInfo, statErr := rootFile.Stat()
	closeErr := rootFile.Close()
	if statErr != nil {
		return fmt.Errorf("stat managed removal root anchor: %w", statErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close managed removal root anchor: %w", closeErr)
	}
	if !os.SameFile(rootInfo, anchoredRootInfo) {
		return fmt.Errorf("managed removal root changed while it was being anchored")
	}
	if afterAnchor != nil {
		if err := afterAnchor(); err != nil {
			return err
		}
	}
	return removeRootEntry(rootHandle, rel, targetInfo)
}

func removeRootEntry(parent *os.Root, name string, expected os.FileInfo) error {
	if err := parent.Remove(name); err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}

	child, err := parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open managed removal target %q: %w", name, err)
	}
	defer child.Close()
	anchor, err := child.Open(".")
	if err != nil {
		return fmt.Errorf("open managed removal target anchor %q: %w", name, err)
	}
	actual, statErr := anchor.Stat()
	if statErr != nil {
		_ = anchor.Close()
		return fmt.Errorf("stat managed removal target anchor %q: %w", name, statErr)
	}
	if expected != nil && !os.SameFile(expected, actual) {
		_ = anchor.Close()
		return fmt.Errorf("managed removal target %q changed after validation", name)
	}
	entries, readErr := anchor.ReadDir(-1)
	closeErr := anchor.Close()
	if readErr != nil {
		return fmt.Errorf("read managed removal target %q: %w", name, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close managed removal target %q: %w", name, closeErr)
	}
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect managed removal child %q: %w", entry.Name(), err)
		}
		if err := removeRootEntry(child, entry.Name(), entryInfo); err != nil {
			return err
		}
	}

	current, err := parent.Open(name)
	if err != nil {
		return fmt.Errorf("reopen managed removal target %q before final remove: %w", name, err)
	}
	currentInfo, statErr := current.Stat()
	_ = current.Close()
	if statErr != nil {
		return fmt.Errorf("restat managed removal target %q before final remove: %w", name, statErr)
	}
	if expected != nil && !os.SameFile(expected, currentInfo) {
		return fmt.Errorf("managed removal target %q changed before final remove", name)
	}
	if err := parent.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove managed target %q: %w", name, err)
	}
	return nil
}

func rejectSymlinkPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, candidate := range []string{filepath.Dir(abs), abs} {
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect path component %q: %w", candidate, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse symlink path component %q", candidate)
		}
	}
	return nil
}
