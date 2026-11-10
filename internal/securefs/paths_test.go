//go:build !windows

package securefs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSafeChildRejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if _, err := SafeChild(root, "..", "outside"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	inside, err := SafeChild(root, "account", "codex-home")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(inside)) != root {
		t.Fatalf("unexpected child path: %s", inside)
	}
}

func TestPrivateDirectoryAndAtomicFilePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("directory permissions too broad: %o", dirInfo.Mode().Perm())
	}
	file := filepath.Join(root, "state.json")
	if err := AtomicWriteFile(file, []byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("file permissions too broad: %o", fileInfo.Mode().Perm())
	}
}

func TestAtomicWriteReportsDurabilityUncertainAfterVisibleRename(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(root, "state.json")
	sentinel := errors.New("synthetic directory sync failure")
	err := atomicWriteFile(path, []byte("new\n"), func(string) error { return sentinel })
	if err == nil || AtomicWriteCommitState(err) != CommitDurabilityUncertain || !errors.Is(err, sentinel) {
		t.Fatalf("post-rename sync failure must be durability-uncertain: state=%v err=%v", AtomicWriteCommitState(err), err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "new\n" {
		t.Fatalf("rename should already be visible despite durability uncertainty: data=%q err=%v", data, readErr)
	}
}

type faultingAtomicTempFile struct {
	*os.File
	stage string
	err   error
}

func (f *faultingAtomicTempFile) Write(p []byte) (int, error) {
	switch f.stage {
	case "write":
		return 0, f.err
	case "short-write":
		if len(p) == 0 {
			return 0, nil
		}
		return len(p) - 1, nil
	default:
		return f.File.Write(p)
	}
}

func (f *faultingAtomicTempFile) Sync() error {
	if f.stage == "sync" {
		return f.err
	}
	return f.File.Sync()
}

func (f *faultingAtomicTempFile) Close() error {
	closeErr := f.File.Close()
	if f.stage == "close" {
		return f.err
	}
	return closeErr
}

func TestAtomicWritePreRenameFailuresRemainUncommittedAndPreserveOldTarget(t *testing.T) {
	for _, stage := range []string{"create", "write", "short-write", "sync", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "private")
			if err := EnsurePrivateDir(root); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "state.json")
			if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("synthetic " + stage + " failure")
			ops := defaultAtomicWriteOps()
			switch stage {
			case "create":
				ops.createTemp = func(string, string) (atomicTempFile, error) { return nil, sentinel }
			case "rename":
				ops.rename = func(string, string) error { return sentinel }
			default:
				ops.createTemp = func(dir, pattern string) (atomicTempFile, error) {
					file, err := os.CreateTemp(dir, pattern)
					if err != nil {
						return nil, err
					}
					return &faultingAtomicTempFile{File: file, stage: stage, err: sentinel}, nil
				}
			}
			err := atomicWriteFileWithOps(path, []byte("new\n"), ops)
			if err == nil || AtomicWriteCommitState(err) != CommitNotCommitted {
				t.Fatalf("%s failure must be uncommitted: state=%v err=%v", stage, AtomicWriteCommitState(err), err)
			}
			if stage == "short-write" {
				if !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("short write must preserve io.ErrShortWrite: %v", err)
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("%s failure lost its cause: %v", stage, err)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != "old\n" {
				t.Fatalf("%s failure changed old target: data=%q err=%v", stage, data, readErr)
			}
		})
	}
}

func TestEnsurePrivateFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateFile(link); err == nil {
		t.Fatal("symlink private file must be rejected")
	}
}

func TestRemoveTreeRejectsRootOutsideAndSymlink(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "managed")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := EnsurePrivateDir(outside); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(root, outside); err == nil {
		t.Fatal("outside removal must be rejected")
	}
	if err := RemoveTree(root, root); err == nil {
		t.Fatal("managed root itself must not be removable")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(root, link); err == nil {
		t.Fatal("symlink tree target must be rejected")
	}
	if _, err := os.Stat(outside); errors.Is(err, os.ErrNotExist) {
		t.Fatal("outside directory was unexpectedly removed")
	}
}

func TestRemoveTreeDeletesNestedManagedTreeWithoutFollowingDanglingSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	target := filepath.Join(root, "profile")
	if err := EnsurePrivateDir(filepath.Join(target, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "nested", "data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(target, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(root, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nested managed tree still exists after removal: %v", err)
	}
}

func TestRemoveTreeAnchoredRootRejectsAncestorSwapOutsideManagedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "managed")
	accounts := filepath.Join(root, "accounts")
	target := filepath.Join(accounts, "profile")
	if err := EnsurePrivateDir(target); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := EnsurePrivateDir(filepath.Join(outside, "profile")); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "profile", "keep")
	if err := os.WriteFile(sentinel, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldAccounts := filepath.Join(root, "accounts-original")
	err := removeTree(root, target, func() error {
		if err := os.Rename(accounts, oldAccounts); err != nil {
			return err
		}
		return os.Symlink(outside, accounts)
	})
	if err == nil {
		t.Fatal("ancestor replacement with a symlink outside the anchored root must fail closed")
	}
	data, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(data) != "outside\n" {
		t.Fatalf("outside sentinel was modified by raced removal: data=%q err=%v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(oldAccounts, "profile")); statErr != nil {
		t.Fatalf("original target should remain available for explicit recovery after raced removal: %v", statErr)
	}
}

func TestValidateManagedPathRejectsSymlinkAncestor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "managed")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := EnsurePrivateDir(outside); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "accounts")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedPath(root, filepath.Join(link, "p_deadbeef")); err == nil {
		t.Fatal("managed symlink ancestor must be rejected")
	}
}

func TestTryLockRejectsSymlinkAndHardLinkedLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "locks")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink.lock")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(symlink); err == nil {
		t.Fatal("symlink lock file must be rejected")
	}
	hardlink := filepath.Join(root, "hardlink.lock")
	if err := os.Link(target, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(hardlink); err == nil {
		t.Fatal("hard-linked lock file must be rejected")
	}
}

func TestLockFileInodeIsStableAcrossNormalReleaseAndReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "profile.lock")
	first, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("CodexPool must keep lock files persistent; unlink/recreate would split flock across inodes")
	}
}

func TestTryLockSetsCloseOnExec(t *testing.T) {
	root := filepath.Join(t.TempDir(), "locks")
	lock, err := TryLock(filepath.Join(root, "profile.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, lock.f.Fd(), uintptr(syscall.F_GETFD), 0)
	if errno != 0 {
		t.Fatalf("fcntl(F_GETFD): %v", errno)
	}
	if flags&uintptr(syscall.FD_CLOEXEC) == 0 {
		t.Fatal("lock file descriptor must be close-on-exec")
	}
}

func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("CODEXPOOL_LOCK_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CODEXPOOL_LOCK_HELPER_MODE")
	if mode == "hold" {
		lock, err := TryLock(os.Getenv("CODEXPOOL_LOCK_HELPER_PATH"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer lock.Close()
	}
	fmt.Fprintln(os.Stdout, "ready")
	time.Sleep(30 * time.Second)
}

func TestFlockIsReleasedWhenHoldingProcessIsKilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "profile.lock")
	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		"CODEXPOOL_LOCK_HELPER=1",
		"CODEXPOOL_LOCK_HELPER_MODE=hold",
		"CODEXPOOL_LOCK_HELPER_PATH="+path,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	if scanner := bufio.NewScanner(stdout); !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("lock helper did not report readiness")
	}
	if lock, err := TryLock(path); err == nil {
		_ = lock.Close()
		t.Fatal("parent unexpectedly acquired lock while helper held it")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed lock helper unexpectedly exited successfully")
	}
	cmd.Process = nil
	lock, err := TryLock(path)
	if err != nil {
		t.Fatalf("OS flock was not released after holder process death: %v", err)
	}
	_ = lock.Close()
}

func TestLockDescriptorDoesNotLeakAcrossExec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locks", "profile.lock")
	lock, err := TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		"CODEXPOOL_LOCK_HELPER=1",
		"CODEXPOOL_LOCK_HELPER_MODE=sleep",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	if scanner := bufio.NewScanner(stdout); !scanner.Scan() || scanner.Text() != "ready" {
		_ = lock.Close()
		t.Fatal("exec helper did not report readiness")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := TryLock(path)
	if err != nil {
		t.Fatalf("exec child retained the parent's lock descriptor: %v", err)
	}
	_ = second.Close()
}

func TestReadPrivateFileRejectsSymlinkAndHardLink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(symlink, 1024); err == nil {
		t.Fatal("private read must not follow a symlink")
	}
	hardlink := filepath.Join(root, "hardlink")
	if err := os.Link(target, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(hardlink, 1024); err == nil {
		t.Fatal("private read must reject a hard-linked file")
	}
}
