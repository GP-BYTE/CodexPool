//go:build !windows

package codexruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

func TestRunInteractiveUsesProfileHomeFiltersEndpointAndPreservesArgs(t *testing.T) {
	store, p := readyProfile(t)
	script := fakeCaptureScript(t)
	rt, err := New(script, store)
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "capture.txt")
	t.Setenv("FAKE_CAPTURE", capture)
	t.Setenv("OPENAI_BASE_URL", "https://override.invalid")
	wantCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	if err := rt.RunInteractive(context.Background(), p, []string{"exec", "synthetic"}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := readCapture(t, capture)
	for _, want := range []string{
		"CODEX_HOME=" + p.CodexHome,
		"OPENAI_BASE_URL=unset",
		"PWD=" + wantCWD,
		"ARG1=exec",
		"ARG2=synthetic",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("capture missing %q:\n%s", want, got)
		}
	}
}

func TestLoginInteractiveDelegatesToOfficialLoginShapeWithoutConfigOverrideArg(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	script := fakeCaptureScript(t)
	rt, _ := New(script, store)
	capture := filepath.Join(t.TempDir(), "login.txt")
	t.Setenv("FAKE_CAPTURE", capture)

	if err := rt.LoginInteractive(context.Background(), p, true, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got := readCapture(t, capture)
	if !strings.Contains(got, "ARG1=login") || !strings.Contains(got, "ARG2=--device-auth") {
		t.Fatalf("unexpected login command shape:\n%s", got)
	}
	if strings.Contains(got, "ARG1=-c") || strings.Contains(got, "ARG2=-c") {
		t.Fatalf("login must rely on the private Profile config instead of CLI config override:\n%s", got)
	}
	if !strings.Contains(got, "CODEX_HOME="+p.CodexHome) {
		t.Fatalf("login did not receive isolated CODEX_HOME:\n%s", got)
	}
}

func TestLoginCoordinatorFailsClosedWhenAnotherLoginIsActive(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	script := fakeCaptureScript(t)
	rt, _ := New(script, store)
	lockPath, err := store.GlobalLoginLockPath()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := securefs.TryLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	err = rt.LoginInteractive(context.Background(), p, false, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !errors.Is(err, securefs.ErrBusy) {
		t.Fatalf("expected busy login coordinator, got %v", err)
	}
}

func TestRunInteractiveRejectsStaleGenerationBeforeStartingProcess(t *testing.T) {
	store, p := readyProfile(t)
	script := fakeCaptureScript(t)
	rt, _ := New(script, store)
	capture := filepath.Join(t.TempDir(), "stale.txt")
	t.Setenv("FAKE_CAPTURE", capture)
	if _, err := store.Update(p.ID, func(current *profile.Profile) error {
		current.AuthGeneration++
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err := rt.RunInteractive(context.Background(), p, []string{"exec"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("expected stale generation rejection, got %v", err)
	}
	if _, statErr := os.Stat(capture); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fake Codex process should not have started, stat=%v", statErr)
	}
}

func readyProfile(t *testing.T) (*profile.Store, profile.Profile) {
	t.Helper()
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err = store.Update(p.ID, func(current *profile.Profile) error {
		current.Lifecycle = profile.LifecycleReady
		current.Identity = profile.Identity{PlanType: "plus", ObservedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, p
}

func fakeCaptureScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.153.4'
  exit 0
fi
{
  printf 'CODEX_HOME=%s\n' "$CODEX_HOME"
  printf 'OPENAI_BASE_URL=%s\n' "${OPENAI_BASE_URL-unset}"
  printf 'PWD=%s\n' "$PWD"
  printf 'ARG1=%s\n' "${1-unset}"
  printf 'ARG2=%s\n' "${2-unset}"
  printf 'ARG3=%s\n' "${3-unset}"
} > "$FAKE_CAPTURE"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readCapture(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRequireCompatibleRejectsRuntimeEvidenceMismatch(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	path := filepath.Join(t.TempDir(), "mismatch-codex.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.154.0'
  exit 0
fi
exit 40
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, _ := New(path, store)
	rt.ExpectedVersion = "0.153.4"
	actual, err := rt.RequireCompatible(context.Background())
	if err == nil || !errors.Is(err, ErrUnsupportedRuntime) || actual != "0.154.0" {
		t.Fatalf("expected exact evidence mismatch rejection, actual=%q err=%v", actual, err)
	}
}

func TestRequireCompatiblePinsExactPrereleaseBuild(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	path := filepath.Join(t.TempDir(), "prerelease-codex.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.154.0-alpha.6.2'
  exit 0
fi
exit 40
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, _ := New(path, store)
	rt.ExpectedVersion = "0.154.0-alpha.6.2"
	if actual, err := rt.RequireCompatible(context.Background()); err != nil || actual != "0.154.0-alpha.6.2" {
		t.Fatalf("exact prerelease evidence must match: actual=%q err=%v", actual, err)
	}
	rt.ExpectedVersion = "0.154.0-alpha.6.3"
	if actual, err := rt.RequireCompatible(context.Background()); err == nil || !errors.Is(err, ErrUnsupportedRuntime) || actual != "0.154.0-alpha.6.2" {
		t.Fatalf("different prerelease build must fail closed: actual=%q err=%v", actual, err)
	}
}

func TestVersionProbeUsesIsolatedUnauthenticatedHome(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	capture := filepath.Join(t.TempDir(), "version-home.txt")
	path := filepath.Join(t.TempDir(), "version-codex.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' "$CODEX_HOME" > "$VERSION_HOME_CAPTURE"
  printf '%s\n' 'codex-cli 0.153.4'
  exit 0
fi
exit 40
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERSION_HOME_CAPTURE", capture)
	rt, _ := New(path, store)
	if _, err := rt.Version(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(readCapture(t, capture))
	want := filepath.Join(store.Root, "runtime-probe", "codex-home")
	if got != want {
		t.Fatalf("version probe must use isolated unauthenticated CODEX_HOME: got=%q want=%q", got, want)
	}
}

func TestAppServerCloseIsIdempotentAndReapsOwnedProcess(t *testing.T) {
	store, p := readyProfile(t)
	path := filepath.Join(t.TempDir(), "app-server-codex.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.153.4'
  exit 0
fi
if [ "${1-unset}" != "app-server" ]; then
  exit 42
fi
while IFS= read -r line; do
  :
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, _ := New(path, store)
	rt.ExpectedVersion = "0.153.4"
	server, version, err := rt.StartAppServer(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer server.ReleaseLease()
	if version != "0.153.4" {
		t.Fatalf("unexpected runtime version: %q", version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := server.Close(ctx); err != nil {
		t.Fatalf("second Close must return the completed close result without blocking: %v", err)
	}
}

func TestAppServerReadPathCanProbeNewerRuntimeWhileInteractiveGateStaysPinned(t *testing.T) {
	store, p := readyProfile(t)
	path := filepath.Join(t.TempDir(), "forward-compatible-app-server.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.999.0'
  exit 0
fi
if [ "${1-unset}" != "app-server" ]; then
  exit 42
fi
while IFS= read -r line; do
  :
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, err := New(path, store)
	if err != nil {
		t.Fatal(err)
	}
	rt.ExpectedVersion = "0.153.4"
	if _, err := rt.RequireCompatible(context.Background()); err == nil || !errors.Is(err, ErrUnsupportedRuntime) {
		t.Fatalf("interactive compatibility gate must remain pinned, got %v", err)
	}
	server, version, err := rt.StartAppServer(context.Background(), p)
	if err != nil {
		t.Fatalf("read-only app-server path should reach strict wire validation on a newer runtime: %v", err)
	}
	defer server.ReleaseLease()
	if version != "0.999.0" {
		t.Fatalf("unexpected probed runtime version: %q", version)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerConcurrentCloseIsIdempotent(t *testing.T) {
	store, p := readyProfile(t)
	path := filepath.Join(t.TempDir(), "concurrent-close-app-server.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.153.4'
  exit 0
fi
if [ "${1-unset}" != "app-server" ]; then
  exit 42
fi
while IFS= read -r line; do
  :
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, err := New(path, store)
	if err != nil {
		t.Fatal(err)
	}
	rt.ExpectedVersion = "0.153.4"
	server, _, err := rt.StartAppServer(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer server.ReleaseLease()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- server.Close(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close callers must observe the same completed cleanup without error: %v", err)
		}
	}
	if err := server.ReleaseLease(); err != nil {
		t.Fatal(err)
	}
	second, err := rt.Guard.TryAcquire(p.ID)
	if err != nil {
		t.Fatalf("Profile lease was not reusable after concurrent Close completed: %v", err)
	}
	_ = second.Close()
}

func TestAppServerCloseTerminatesDescendantsBeforeLeaseReuse(t *testing.T) {
	store, p := readyProfile(t)
	pidCapture := filepath.Join(t.TempDir(), "helper.pid")
	t.Setenv("HELPER_PID_CAPTURE", pidCapture)
	path := filepath.Join(t.TempDir(), "descendant-app-server.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.153.4'
  exit 0
fi
if [ "${1-unset}" != "app-server" ]; then
  exit 42
fi
(
  while :; do
    sleep 1
  done
) &
helper_pid=$!
printf '%s\n' "$helper_pid" > "$HELPER_PID_CAPTURE"
while IFS= read -r line; do
  :
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rt, err := New(path, store)
	if err != nil {
		t.Fatal(err)
	}
	rt.ExpectedVersion = "0.153.4"
	server, _, err := rt.StartAppServer(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer server.ReleaseLease()

	var helperPID int
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidCapture)
		if readErr == nil {
			helperPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if helperPID <= 0 {
		t.Fatal("fake app-server did not publish descendant pid")
	}
	if err := syscall.Kill(helperPID, 0); err != nil {
		t.Fatalf("fake descendant was not alive before close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatalf("close failed while cleaning owned descendant: %v", err)
	}
	if err := server.ReleaseLease(); err != nil {
		t.Fatal(err)
	}
	if !waitProcessGone(helperPID, time.Second) {
		_ = syscall.Kill(helperPID, syscall.SIGKILL)
		t.Fatalf("owned descendant %d remained alive after AppServer cleanup", helperPID)
	}
	second, err := rt.Guard.TryAcquire(p.ID)
	if err != nil {
		t.Fatalf("Profile lease should be reusable only after descendant cleanup: %v", err)
	}
	_ = second.Close()
}

func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestChildExitCodeMapsUnixSignalConvention(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	err := cmd.Wait()
	if err == nil {
		t.Fatal("signal-terminated child unexpectedly succeeded")
	}
	code, ok := ChildExitCode(err)
	if !ok || code != 143 {
		t.Fatalf("SIGTERM should map to 143, got code=%d ok=%t err=%v", code, ok, err)
	}
}

func TestGracefulCancellationSendsInterruptBeforeFallbackKill(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	interrupted := filepath.Join(t.TempDir(), "interrupted")
	path := filepath.Join(t.TempDir(), "cancel-aware.sh")
	script := `#!/bin/sh
trap 'printf interrupted > "$INTERRUPTED_CAPTURE"; exit 42' INT
printf ready > "$READY_CAPTURE"
while :; do
  :
done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(), "READY_CAPTURE="+ready, "INTERRUPTED_CAPTURE="+interrupted)
	configureGracefulCancellation(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("cancel-aware helper did not become ready")
	}
	cancel()
	err := cmd.Wait()
	if err == nil {
		t.Fatal("interrupt-trapped helper unexpectedly succeeded")
	}
	code, ok := ChildExitCode(err)
	if !ok || code != 42 {
		t.Fatalf("graceful SIGINT trap should exit 42 before fallback kill, code=%d ok=%t err=%v", code, ok, err)
	}
	data, readErr := os.ReadFile(interrupted)
	if readErr != nil || string(data) != "interrupted" {
		t.Fatalf("child did not observe SIGINT before fallback kill: data=%q err=%v", data, readErr)
	}
}

func TestAppServerRetainsProfileLeaseUntilLateReapCompletes(t *testing.T) {
	store, p := readyProfile(t)
	rt, err := New(filepath.Join(t.TempDir(), "unused-codex"), store)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.Guard.TryAcquire(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitCh := make(chan error, 1)
	server := &AppServer{
		waitCh:       waitCh,
		lease:        lease,
		resourceDone: make(chan struct{}),
		reapTimeout:  20 * time.Millisecond,
		closeDone:    make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Close(ctx); err == nil {
		t.Fatal("canceled close with unreaped process must report cleanup pending")
	}
	if err := server.ReleaseLease(); err != nil {
		t.Fatal(err)
	}
	if second, err := rt.Guard.TryAcquire(p.ID); err == nil {
		_ = second.Close()
		t.Fatal("Profile lease must remain busy while process reap is still pending")
	}
	waitCh <- nil
	select {
	case <-server.ResourceDone():
	case <-time.After(time.Second):
		t.Fatal("late process reap did not finish AppServer resource cleanup")
	}
	second, err := rt.Guard.TryAcquire(p.ID)
	if err != nil {
		t.Fatalf("Profile lease should be released after confirmed reap: %v", err)
	}
	_ = second.Close()
}
