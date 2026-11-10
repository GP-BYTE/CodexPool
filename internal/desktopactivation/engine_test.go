package desktopactivation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

type checker func(context.Context) error

func (c checker) RequireDrained(ctx context.Context) error { return c(ctx) }
func (c checker) Verify(ctx context.Context, target Target) error {
	return c(ctx)
}

func readyProfile(t *testing.T) (*profile.Store, profile.Profile) {
	t.Helper()
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("synthetic", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err = store.UpdateExpected(p, func(next *profile.Profile) error {
		next.Lifecycle = profile.LifecycleReady
		next.Identity = profile.Identity{Email: "synthetic@example.invalid", PlanType: "free", ObservedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(filepath.Join(p.CodexHome, "auth.json"), []byte("synthetic-source-auth")); err != nil {
		t.Fatal(err)
	}
	return store, p
}

func addReadyProfile(t *testing.T, store *profile.Store, alias, auth string) profile.Profile {
	t.Helper()
	p, err := store.CreatePending(alias, profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p, err = store.UpdateExpected(p, func(next *profile.Profile) error {
		next.Lifecycle = profile.LifecycleReady
		next.Identity = profile.Identity{Email: alias + "@example.invalid", PlanType: "free", ObservedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(filepath.Join(p.CodexHome, "auth.json"), []byte(auth)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSyntheticActivationParksAndRecovers(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if !SyntheticRoot(engine.Root) {
		t.Fatal("synthetic marker was not created")
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("synthetic-previous-auth")); err != nil {
		t.Fatal(err)
	}
	target := Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	if err := engine.Activate(context.Background(), target, checker(func(context.Context) error { return nil }), checker(func(context.Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "synthetic-source-auth" {
		t.Fatalf("activated synthetic auth = %q, %v", got, err)
	}
	if err := engine.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err = securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "synthetic-previous-auth" {
		t.Fatalf("recovered synthetic auth = %q, %v", got, err)
	}
	if _, err := os.Stat(engine.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains after recovery: %v", err)
	}
}

func TestInspectLiveStateReadsOnlyTransactionMetadata(t *testing.T) {
	store, p := readyProfile(t)
	state, err := InspectLiveState(store)
	if err != nil || state.Kind != LiveStateNone {
		t.Fatalf("initial live state = %#v, %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "desktop-activation")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only inspection unexpectedly created state: %v", err)
	}
	engine, err := NewLive(store)
	if err != nil {
		t.Fatal(err)
	}
	journal := Journal{Version: 1, Phase: PhaseVerified, ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: string(p.Lifecycle)}
	if err := engine.writeJournal(journal); err != nil {
		t.Fatal(err)
	}
	state, err = InspectLiveState(store)
	if err != nil || state.Kind != LiveStateActive || state.Target.ProfileID != p.ID {
		t.Fatalf("active live state = %#v, %v", state, err)
	}
	if err := engine.writePending(journal); err != nil {
		t.Fatal(err)
	}
	state, err = InspectLiveState(store)
	if err != nil || state.Kind != LiveStateRecoveryRequired {
		t.Fatalf("pending live state = %#v, %v", state, err)
	}
}

func TestSyntheticActivationFailsBeforeWritesWhenNotDrained(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	err = engine.Activate(context.Background(), target, checker(func(context.Context) error { return errors.New("still running") }), checker(func(context.Context) error { return nil }))
	if err == nil {
		t.Fatal("activation unexpectedly proceeded")
	}
	if _, err := os.Stat(engine.destinationPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination changed before drain proof: %v", err)
	}
	if _, err := os.Stat(engine.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal written before drain proof: %v", err)
	}
}

func TestSyntheticActivationRejectsGenerationDrift(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	if _, err := store.UpdateExpected(p, func(next *profile.Profile) error { next.AuthGeneration++; return nil }); err != nil {
		t.Fatal(err)
	}
	err = engine.Activate(context.Background(), target, checker(func(context.Context) error { return nil }), checker(func(context.Context) error { return nil }))
	if !errors.Is(err, profile.ErrProfileChanged) {
		t.Fatalf("activation error = %v, want profile changed", err)
	}
}

func TestSyntheticActivationVerificationFailureRequiresRecovery(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("previous")); err != nil {
		t.Fatal(err)
	}
	target := Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	err = engine.Activate(context.Background(), target, checker(func(context.Context) error { return nil }), checker(func(context.Context) error { return errors.New("fake startup failed") }))
	if err == nil {
		t.Fatal("activation unexpectedly verified")
	}
	if err := engine.Activate(context.Background(), target, checker(func(context.Context) error { return nil }), checker(func(context.Context) error { return nil })); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("second activation error = %v, want recovery required", err)
	}
	if err := engine.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "previous" {
		t.Fatalf("restored auth = %q, %v", got, err)
	}
}

func TestSyntheticActivationSwitchPreservesOriginalDesktopCredential(t *testing.T) {
	store, first := readyProfile(t)
	second := addReadyProfile(t, store, "synthetic-two", "synthetic-second-auth")
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("original-desktop-auth")); err != nil {
		t.Fatal(err)
	}
	allow := checker(func(context.Context) error { return nil })
	firstTarget := Target{ProfileID: first.ID, AuthGeneration: first.AuthGeneration, Lifecycle: first.Lifecycle}
	secondTarget := Target{ProfileID: second.ID, AuthGeneration: second.AuthGeneration, Lifecycle: second.Lifecycle}
	if err := engine.Activate(context.Background(), firstTarget, allow, allow); err != nil {
		t.Fatal(err)
	}
	if err := engine.Activate(context.Background(), secondTarget, allow, allow); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "synthetic-second-auth" {
		t.Fatalf("switched synthetic auth = %q, %v", got, err)
	}
	state, err := engine.readJournal()
	if err != nil || state.ProfileID != second.ID || state.Phase != PhaseVerified {
		t.Fatalf("active state = %#v, %v", state, err)
	}
	if err := engine.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err = securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "original-desktop-auth" {
		t.Fatalf("original desktop auth was not restored: %q, %v", got, err)
	}
}

func TestSyntheticActivationFailedSwitchRollsBackToPriorActiveCredential(t *testing.T) {
	store, first := readyProfile(t)
	second := addReadyProfile(t, store, "synthetic-two", "synthetic-second-auth")
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	allow := checker(func(context.Context) error { return nil })
	firstTarget := Target{ProfileID: first.ID, AuthGeneration: first.AuthGeneration, Lifecycle: first.Lifecycle}
	secondTarget := Target{ProfileID: second.ID, AuthGeneration: second.AuthGeneration, Lifecycle: second.Lifecycle}
	if err := engine.Activate(context.Background(), firstTarget, allow, allow); err != nil {
		t.Fatal(err)
	}
	if err := engine.Activate(context.Background(), secondTarget, allow, checker(func(context.Context) error { return errors.New("fake second launch failed") })); err == nil {
		t.Fatal("second activation unexpectedly verified")
	}
	if err := engine.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "synthetic-source-auth" {
		t.Fatalf("prior active credential was not restored: %q, %v", got, err)
	}
}

func TestRestoreOriginalAfterFailedSwitchReturnsOriginalDesktopCredential(t *testing.T) {
	store, first := readyProfile(t)
	second := addReadyProfile(t, store, "synthetic-two", "synthetic-second-auth")
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("original-desktop-auth")); err != nil {
		t.Fatal(err)
	}
	allow := checker(func(context.Context) error { return nil })
	firstTarget := Target{ProfileID: first.ID, AuthGeneration: first.AuthGeneration, Lifecycle: first.Lifecycle}
	secondTarget := Target{ProfileID: second.ID, AuthGeneration: second.AuthGeneration, Lifecycle: second.Lifecycle}
	if err := engine.Activate(context.Background(), firstTarget, allow, allow); err != nil {
		t.Fatal(err)
	}
	if err := engine.Activate(context.Background(), secondTarget, allow, checker(func(context.Context) error {
		return errors.New("fake second launch failed")
	})); err == nil {
		t.Fatal("second activation unexpectedly verified")
	}
	if err := engine.RestoreOriginal(); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "original-desktop-auth" {
		t.Fatalf("original desktop credential was not restored: %q, %v", got, err)
	}
	for _, path := range []string{engine.pendingPath(), engine.journalPath(), engine.previousPath(), engine.rollbackPath()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery artifact remains at %s: %v", path, err)
		}
	}
	if err := engine.RestoreOriginal(); err != nil {
		t.Fatalf("idempotent restore = %v", err)
	}
}

func TestRestoreOriginalRecoversInterruptedFirstActivationAfterJournalWrite(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("synthetic-source-auth")); err != nil {
		t.Fatal(err)
	}
	pending := Journal{
		Version:        2,
		Phase:          PhaseVerified,
		ProfileID:      p.ID,
		AuthGeneration: p.AuthGeneration,
		Lifecycle:      string(p.Lifecycle),
		PreviousExists: true,
		Parking:        ParkingPrevious,
	}
	if err := securefs.AtomicWriteFile(engine.previousPath(), []byte("original-desktop-auth")); err != nil {
		t.Fatal(err)
	}
	if err := engine.writeJournal(pending); err != nil {
		t.Fatal(err)
	}
	if err := engine.writePending(pending); err != nil {
		t.Fatal(err)
	}
	if err := engine.RestoreOriginal(); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "original-desktop-auth" {
		t.Fatalf("interrupted first activation recovery = %q, %v", got, err)
	}
	for _, path := range []string{engine.pendingPath(), engine.journalPath(), engine.previousPath()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery artifact remains at %s: %v", path, err)
		}
	}
}

func TestRestoreOriginalRecoversDestinationWriteInterruptedBeforeCompletionCheckpoint(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("synthetic-source-auth")); err != nil {
		t.Fatal(err)
	}
	pending := Journal{
		Version:        2,
		Phase:          PhaseActivating,
		ProfileID:      p.ID,
		AuthGeneration: p.AuthGeneration,
		Lifecycle:      string(p.Lifecycle),
		PreviousExists: true,
		Parking:        ParkingPrevious,
	}
	if err := securefs.AtomicWriteFile(engine.previousPath(), []byte("original-desktop-auth")); err != nil {
		t.Fatal(err)
	}
	if err := engine.writePending(pending); err != nil {
		t.Fatal(err)
	}
	if err := engine.RestoreOriginal(); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "original-desktop-auth" {
		t.Fatalf("write-interrupted activation recovery = %q, %v", got, err)
	}
}

func TestSyntheticActivationGenerationDriftAfterParkingIsRecoverable(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if err := securefs.AtomicWriteFile(engine.destinationPath(), []byte("original-desktop-auth")); err != nil {
		t.Fatal(err)
	}
	target := Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	drift := checker(func(context.Context) error {
		_, updateErr := store.UpdateExpected(p, func(next *profile.Profile) error { next.AuthGeneration++; return nil })
		return updateErr
	})
	err = engine.Activate(context.Background(), target, drift, checker(func(context.Context) error { return nil }))
	if !errors.Is(err, profile.ErrProfileChanged) {
		t.Fatalf("activation error = %v, want profile changed", err)
	}
	if err := engine.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err := securefs.ReadPrivateFile(engine.destinationPath(), 1024)
	if err != nil || string(got) != "original-desktop-auth" {
		t.Fatalf("original desktop auth was not recovered: %q, %v", got, err)
	}
}

func TestSyntheticActivationUsesOneGlobalLock(t *testing.T) {
	store, p := readyProfile(t)
	engine, err := NewSynthetic(store, filepath.Join(t.TempDir(), "fake-desktop"))
	if err != nil {
		t.Fatal(err)
	}
	target := Target{ProfileID: p.ID, AuthGeneration: p.AuthGeneration, Lifecycle: p.Lifecycle}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.Activate(context.Background(), target, checker(func(context.Context) error { return nil }), checker(func(context.Context) error {
			close(entered)
			<-release
			return nil
		}))
	}()
	<-entered
	err = engine.Activate(context.Background(), target, checker(func(context.Context) error { return nil }), checker(func(context.Context) error { return nil }))
	if !errors.Is(err, securefs.ErrBusy) {
		t.Fatalf("concurrent activation error = %v, want busy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNewSyntheticRejectsDefaultCodexHome(t *testing.T) {
	store, _ := readyProfile(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSynthetic(store, filepath.Join(home, ".codex")); err == nil {
		t.Fatal("default CODEX_HOME was accepted as synthetic root")
	}
}

func TestNewSyntheticRefusesExistingUnmanagedDirectory(t *testing.T) {
	store, _ := readyProfile(t)
	root := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSynthetic(store, root); err == nil {
		t.Fatal("existing unmanaged directory was accepted")
	}
}
