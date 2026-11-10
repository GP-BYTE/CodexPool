//go:build !windows

package authflow

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
	"codexpool/internal/usage"
)

func TestPurgeRunsOfficialLogoutBeforeRemovingRegistration(t *testing.T) {
	store, p := readyAuthProfile(t)
	capture := filepath.Join(t.TempDir(), "calls.txt")
	script := fakeLogoutRuntime(t)
	t.Setenv("AUTHFLOW_CAPTURE", capture)
	rt, err := codexruntime.New(script, store)
	if err != nil {
		t.Fatal(err)
	}
	us := usage.NewService(rt, "test-client", "0.153.4")
	svc := New(store, rt, us, strings.NewReader(""), io.Discard, io.Discard)

	removed, err := svc.Remove(context.Background(), p.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if removed.ID != p.ID {
		t.Fatalf("wrong removed profile: %#v", removed)
	}
	if _, err := store.Resolve(p.ID); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("purged profile must be unregistered, got %v", err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "logout\n") != 1 {
		t.Fatalf("expected exactly one official logout, got %q", string(data))
	}
}

func TestDeletingRetryDoesNotRequireCodexOrProfileConfig(t *testing.T) {
	store, p := readyAuthProfile(t)
	p, err := store.Update(p.ID, func(current *profile.Profile) error {
		current.AuthGeneration++
		current.Lifecycle = profile.LifecycleDeleting
		current.Identity = profile.Identity{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.CodexHome, "config.toml")); err != nil {
		t.Fatal(err)
	}
	rt, err := codexruntime.New(filepath.Join(t.TempDir(), "missing-codex"), store)
	if err != nil {
		t.Fatal(err)
	}
	rt.ExpectedVersion = "0.153.4"
	svc := New(store, rt, nil, strings.NewReader(""), io.Discard, io.Discard)

	removed, err := svc.Remove(context.Background(), p.ID, true)
	if err != nil {
		t.Fatalf("deleting retry must only continue local cleanup: %v", err)
	}
	if removed.ID != p.ID {
		t.Fatalf("wrong removed profile: %#v", removed)
	}
	if _, err := store.Resolve(p.ID); !errors.Is(err, profile.ErrNotFound) {
		t.Fatalf("deleting retry must unregister profile, got %v", err)
	}
}

func TestPurgeLogoutPendingRetryRepeatsLogoutThenContinuesDeleting(t *testing.T) {
	store, p := readyAuthProfile(t)
	p, err := store.Update(p.ID, func(current *profile.Profile) error {
		current.AuthGeneration++
		current.Lifecycle = profile.LifecyclePurgeLogoutPending
		current.Identity = profile.Identity{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("AUTHFLOW_CAPTURE", capture)
	rt, _ := codexruntime.New(fakeLogoutRuntime(t), store)
	us := usage.NewService(rt, "test-client", "0.153.4")
	svc := New(store, rt, us, strings.NewReader(""), io.Discard, io.Discard)

	if _, err := svc.Remove(context.Background(), p.ID, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "logout\n") != 1 {
		t.Fatalf("purgeLogoutPending retry should run official logout once, got %q", string(data))
	}
}

func readyAuthProfile(t *testing.T) (*profile.Store, profile.Profile) {
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

func fakeLogoutRuntime(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.153.4'
  exit 0
fi
if [ "${1-unset}" = "logout" ]; then
  printf '%s\n' 'logout' >> "$AUTHFLOW_CAPTURE"
  exit 0
fi
exit 41
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProfileOperationLockBlocksLogoutAndReloginWithoutGenerationChange(t *testing.T) {
	store, p := readyAuthProfile(t)
	capture := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("AUTHFLOW_CAPTURE", capture)
	rt, err := codexruntime.New(fakeLogoutRuntime(t), store)
	if err != nil {
		t.Fatal(err)
	}
	us := usage.NewService(rt, "test-client", "0.153.4")
	svc := New(store, rt, us, strings.NewReader(""), io.Discard, io.Discard)
	lock, err := rt.Guard.TryAcquire(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	if _, err := svc.Logout(context.Background(), p.ID); err == nil {
		t.Fatal("logout must fail busy while the same Profile operation lock is held")
	}
	if _, _, err := svc.Relogin(context.Background(), p.ID, false); err == nil {
		t.Fatal("relogin must fail busy while the same Profile operation lock is held")
	}
	current, err := store.Resolve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AuthGeneration != p.AuthGeneration || current.Lifecycle != profile.LifecycleReady {
		t.Fatalf("busy lifecycle operations must not mutate identity generation/state: before=%#v after=%#v", p, current)
	}
}

func TestPlainRemoveCannotDiscardLogoutPendingRecovery(t *testing.T) {
	store, p := readyAuthProfile(t)
	pending, err := store.UpdateExpected(p, func(current *profile.Profile) error {
		current.AuthGeneration++
		current.Lifecycle = profile.LifecycleLogoutPending
		current.Identity = profile.Identity{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := codexruntime.New(fakeLogoutRuntime(t), store)
	if err != nil {
		t.Fatal(err)
	}
	us := usage.NewService(rt, "test-client", "0.153.4")
	svc := New(store, rt, us, strings.NewReader(""), io.Discard, io.Discard)
	if _, err := svc.RemoveExpected(context.Background(), pending, false); err == nil {
		t.Fatal("plain unregister must not discard logoutPending recovery state")
	}
	current, err := store.Resolve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AuthGeneration != pending.AuthGeneration || current.Lifecycle != profile.LifecycleLogoutPending {
		t.Fatalf("logoutPending recovery state changed unexpectedly: before=%#v after=%#v", pending, current)
	}
}

func TestPurgeRecoveryTombstoneCannotBeOverwrittenByNormalLifecycleCommands(t *testing.T) {
	store, p := readyAuthProfile(t)
	p, err := store.Update(p.ID, func(current *profile.Profile) error {
		current.AuthGeneration++
		current.Lifecycle = profile.LifecyclePurgeLogoutPending
		current.Identity = profile.Identity{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("AUTHFLOW_CAPTURE", capture)
	rt, err := codexruntime.New(fakeLogoutRuntime(t), store)
	if err != nil {
		t.Fatal(err)
	}
	us := usage.NewService(rt, "test-client", "0.153.4")
	svc := New(store, rt, us, strings.NewReader(""), io.Discard, io.Discard)

	if _, _, err := svc.Relogin(context.Background(), p.ID, false); err == nil {
		t.Fatal("relogin must not overwrite purgeLogoutPending")
	}
	if _, err := svc.Logout(context.Background(), p.ID); err == nil {
		t.Fatal("normal logout must not overwrite purgeLogoutPending")
	}
	if _, err := svc.Remove(context.Background(), p.ID, false); err == nil {
		t.Fatal("plain unregister must not discard a purge recovery tombstone")
	}
	current, err := store.Resolve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AuthGeneration != p.AuthGeneration || current.Lifecycle != profile.LifecyclePurgeLogoutPending {
		t.Fatalf("purge recovery state changed unexpectedly: before=%#v after=%#v", p, current)
	}
}
