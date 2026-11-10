// Package desktopactivation contains the intentionally synthetic-only
// transaction model for a future Desktop handoff.  It is not wired to the
// CLI or TUI, and its constructor only operates below a caller-supplied
// synthetic root.
package desktopactivation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

const syntheticMarkerName = ".codexpool-desktop-activation-synthetic"
const syntheticMarkerContents = "codexpool-desktop-activation-synthetic-v1\n"

var ErrRecoveryRequired = errors.New("desktop activation recovery is required")

type LiveState string

const (
	LiveStateNone             LiveState = "none"
	LiveStateActive           LiveState = "active"
	LiveStateRecoveryRequired LiveState = "recoveryRequired"
)

// State is read-only routing metadata for the normal CLI/TUI. It never reads
// the Desktop destination or any credential bytes.
type State struct {
	Kind   LiveState
	Target Target
}

// Target is a commit identity, not a mutable alias.  Every transaction
// resolves it again while holding the activation lock.
type Target struct {
	ProfileID      string            `json:"profileId"`
	AuthGeneration uint64            `json:"authGeneration"`
	Lifecycle      profile.Lifecycle `json:"lifecycle"`
}

type Phase string

const (
	PhasePrepared Phase = "prepared"
	PhaseParked   Phase = "parked"
	// PhaseActivating is committed before the destination write. Recovery must
	// treat it as possibly written: a process can stop after AtomicWriteFile
	// succeeds but before the following phase checkpoint is persisted.
	PhaseActivating Phase = "activating"
	PhaseActivated  Phase = "activated"
	PhaseVerified   Phase = "verified"
	// PhaseRestored is a durable checkpoint: the Desktop destination has
	// already been restored, so retrying recovery must only finish cleanup.
	PhaseRestored Phase = "restored"
)

type ParkingKind string

const (
	ParkingPrevious ParkingKind = "previous"
	ParkingRollback ParkingKind = "rollback"
)

// Journal deliberately contains only routing and recovery metadata. It never
// contains credential bytes.
type Journal struct {
	Version        int         `json:"version"`
	Phase          Phase       `json:"phase"`
	ProfileID      string      `json:"profileId"`
	AuthGeneration uint64      `json:"authGeneration"`
	Lifecycle      string      `json:"lifecycle"`
	PreviousExists bool        `json:"previousExists"`
	Parking        ParkingKind `json:"parking,omitempty"`
}

// DrainChecker models the evidence a future native Desktop controller must
// produce before a handoff. The production controller does not exist yet.
type DrainChecker interface {
	RequireDrained(context.Context) error
}

// Verifier is likewise synthetic-only. A future real integration must have a
// separately reviewed native verification protocol, not infer success from a
// file write.
type Verifier interface {
	Verify(context.Context, Target) error
}

type Engine struct {
	Store         *profile.Store
	Root          string
	desktopAuth   string
	syntheticOnly bool
}

func NewSynthetic(store *profile.Store, root string) (*Engine, error) {
	if store == nil {
		return nil, errors.New("profile store is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve synthetic root: %w", err)
	}
	abs = filepath.Clean(abs)
	if home, homeErr := os.UserHomeDir(); homeErr == nil && filepath.Clean(filepath.Join(home, ".codex")) == abs {
		return nil, errors.New("refuse default CODEX_HOME as a synthetic activation root")
	}
	if filepath.Clean(store.Root) == abs {
		return nil, errors.New("synthetic activation root must be separate from CODEXPOOL_HOME")
	}
	markerPath := filepath.Join(abs, syntheticMarkerName)
	if _, err := os.Lstat(abs); err == nil {
		marker, markerErr := securefs.ReadPrivateFile(markerPath, 1024)
		if errors.Is(markerErr, os.ErrNotExist) {
			return nil, fmt.Errorf("refuse to adopt existing directory %q as a synthetic activation root", abs)
		}
		if markerErr != nil {
			return nil, fmt.Errorf("read synthetic activation marker: %w", markerErr)
		}
		if string(marker) != syntheticMarkerContents {
			return nil, errors.New("synthetic activation root has an unrecognized marker")
		}
		if err := securefs.EnsurePrivateDir(abs); err != nil {
			return nil, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := securefs.EnsurePrivateDir(abs); err != nil {
			return nil, err
		}
		if err := securefs.AtomicWriteFile(markerPath, []byte(syntheticMarkerContents)); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("inspect synthetic activation root: %w", err)
	}
	return &Engine{Store: store, Root: abs, desktopAuth: filepath.Join(abs, "desktop-home", "auth.json"), syntheticOnly: true}, nil
}

// NewLive creates the state holder for the separately-invoked macOS helper.
// It is deliberately not reachable from the normal CodexPool CLI/TUI. The
// destination is pinned to this user's default CODEX_HOME, never caller input.
func NewLive(store *profile.Store) (*Engine, error) {
	if store == nil {
		return nil, errors.New("profile store is required")
	}
	if _, err := store.Load(); err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home: %w", err)
	}
	root := filepath.Join(store.Root, "desktop-activation")
	if err := securefs.EnsurePrivateDir(root); err != nil {
		return nil, err
	}
	markerPath := filepath.Join(root, ".codexpool-desktop-activation-live")
	marker, markerErr := securefs.ReadPrivateFile(markerPath, 1024)
	if errors.Is(markerErr, os.ErrNotExist) {
		if err := securefs.AtomicWriteFile(markerPath, []byte("codexpool-desktop-activation-live-v1\n")); err != nil {
			return nil, err
		}
	} else if markerErr != nil || string(marker) != "codexpool-desktop-activation-live-v1\n" {
		return nil, errors.New("Desktop activation state root has an unrecognized marker")
	}
	return &Engine{Store: store, Root: root, desktopAuth: filepath.Join(home, ".codex", "auth.json")}, nil
}

// InspectLiveState reads only CodexPool's transaction journal. It deliberately
// does not call NewLive, so opening the normal TUI cannot create state or touch
// the default CODEX_HOME.
func InspectLiveState(store *profile.Store) (State, error) {
	if store == nil {
		return State{}, errors.New("profile store is required")
	}
	root := filepath.Join(store.Root, "desktop-activation")
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return State{Kind: LiveStateNone}, nil
	} else if err != nil {
		return State{}, fmt.Errorf("inspect Desktop activation state: %w", err)
	}
	e := &Engine{Store: store, Root: root}
	if pending, err := e.readPending(); err == nil {
		return State{Kind: LiveStateRecoveryRequired, Target: targetFromJournal(pending)}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	if journal, err := e.readJournal(); err == nil {
		if journal.Phase != PhaseVerified {
			return State{Kind: LiveStateRecoveryRequired, Target: targetFromJournal(journal)}, nil
		}
		return State{Kind: LiveStateActive, Target: targetFromJournal(journal)}, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return State{Kind: LiveStateNone}, nil
	} else {
		return State{}, err
	}
}

func targetFromJournal(j Journal) Target {
	return Target{ProfileID: j.ProfileID, AuthGeneration: j.AuthGeneration, Lifecycle: profile.Lifecycle(j.Lifecycle)}
}

func (e *Engine) Activate(ctx context.Context, target Target, drain DrainChecker, verifier Verifier) error {
	if e == nil || e.Store == nil {
		return errors.New("desktop activation engine is not configured")
	}
	if drain == nil || verifier == nil {
		return errors.New("synthetic drain checker and verifier are required")
	}
	lock, err := securefs.TryLock(e.lockPath())
	if err != nil {
		return fmt.Errorf("desktop activation lock: %w", err)
	}
	defer lock.Close()
	state, stateErr := e.readJournal()
	stateExists := stateErr == nil
	if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return stateErr
	}
	if stateExists && state.Phase != PhaseVerified {
		return ErrRecoveryRequired
	}
	if _, err := e.readPending(); err == nil {
		return ErrRecoveryRequired
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if stateExists && state.ProfileID == target.ProfileID && state.AuthGeneration == target.AuthGeneration && state.Lifecycle == string(target.Lifecycle) {
		return errors.New("synthetic Desktop is already active for this immutable target")
	}
	p, err := e.resolveTarget(target)
	if err != nil {
		return err
	}
	if err := drain.RequireDrained(ctx); err != nil {
		return fmt.Errorf("Desktop is not proven drained: %w", err)
	}

	source, err := securefs.ReadPrivateFile(filepath.Join(p.CodexHome, "auth.json"), 1<<20)
	if err != nil {
		return fmt.Errorf("read synthetic source credential: %w", err)
	}
	previous, previousExists, err := e.readDestination()
	if err != nil {
		return err
	}
	parking := ParkingPrevious
	if stateExists {
		parking = ParkingRollback
	}
	pending := Journal{Version: 2, Phase: PhasePrepared, ProfileID: target.ProfileID, AuthGeneration: target.AuthGeneration, Lifecycle: string(target.Lifecycle), PreviousExists: previousExists, Parking: parking}
	if err := e.writePending(pending); err != nil {
		return err
	}
	if previousExists {
		parkingPath := e.parkingPath(pending)
		if err := securefs.AtomicWriteFile(parkingPath, previous); err != nil {
			return fmt.Errorf("park synthetic Desktop credential: %w", err)
		}
	}
	pending.Phase = PhaseParked
	if err := e.writePending(pending); err != nil {
		return err
	}
	// Re-resolve after potentially slow filesystem work. Alias changes do not
	// matter, but generation or lifecycle changes make the handoff unsafe.
	if _, err := e.resolveTarget(target); err != nil {
		return err
	}
	pending.Phase = PhaseActivating
	if err := e.writePending(pending); err != nil {
		return err
	}
	if err := securefs.AtomicWriteFile(e.destinationPath(), source); err != nil {
		return fmt.Errorf("activate synthetic Desktop credential: %w", err)
	}
	pending.Phase = PhaseActivated
	if err := e.writePending(pending); err != nil {
		return err
	}
	if err := verifier.Verify(ctx, target); err != nil {
		return fmt.Errorf("synthetic Desktop verification failed; recovery remains required: %w", err)
	}
	pending.Phase = PhaseVerified
	if err := e.writeJournal(pending); err != nil {
		return err
	}
	if err := removePrivateFile(e.pendingPath()); err != nil {
		return err
	}
	if stateExists {
		if err := removePrivateFile(e.rollbackPath()); err != nil {
			return err
		}
	}
	return nil
}

// Recover restores the parked synthetic destination. It never modifies the
// selected Profile's source credential and is intentionally unavailable unless
// a journal exists below the synthetic root.
func (e *Engine) Recover() error {
	if e == nil || e.Store == nil {
		return errors.New("desktop activation engine is not configured")
	}
	lock, err := securefs.TryLock(e.lockPath())
	if err != nil {
		return fmt.Errorf("desktop activation lock: %w", err)
	}
	defer lock.Close()
	pending, pendingErr := e.readPending()
	if pendingErr == nil {
		return e.recoverPendingLocked(pending)
	}
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}
	journal, err := e.readJournal()
	if err != nil {
		return err
	}
	return e.restoreVerifiedLocked(journal)
}

// RestoreOriginal restores the Desktop credential that existed before the
// first active CodexPool handoff. It is idempotent so a failed post-restore
// launch or a user retry cannot strand the transaction. It is intentionally
// only called by the explicitly confirmed standalone helper.
func (e *Engine) RestoreOriginal() error {
	if e == nil || e.Store == nil {
		return errors.New("desktop activation engine is not configured")
	}
	lock, err := securefs.TryLock(e.lockPath())
	if err != nil {
		return fmt.Errorf("desktop activation lock: %w", err)
	}
	defer lock.Close()

	pending, pendingErr := e.readPending()
	if pendingErr == nil {
		parking := e.parkingKindForRecovery(pending)
		if err := e.recoverPendingLocked(pending); err != nil {
			return err
		}
		if parking == ParkingPrevious {
			journal, err := e.readJournal()
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if journal.ProfileID != pending.ProfileID || journal.AuthGeneration != pending.AuthGeneration || journal.Lifecycle != pending.Lifecycle {
				return errors.New("Desktop activation journal changed during first-handoff recovery")
			}
			return removePrivateFileIfPresent(e.journalPath())
		}
	} else if !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}

	journal, err := e.readJournal()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return e.restoreVerifiedLocked(journal)
}

func (e *Engine) recoverPendingLocked(pending Journal) error {
	parkingPath := e.parkingPathForRecovery(pending)
	switch pending.Phase {
	case PhasePrepared, PhaseParked:
		// The destination has not been written yet. Discard only the staged
		// backup and transaction record; leaving the destination untouched is
		// safer than inferring that activation happened.
	case PhaseActivating, PhaseActivated, PhaseVerified:
		if pending.PreviousExists {
			previous, err := securefs.ReadPrivateFile(parkingPath, 1<<20)
			if err != nil {
				return fmt.Errorf("read parked synthetic Desktop credential: %w", err)
			}
			if err := securefs.AtomicWriteFile(e.destinationPath(), previous); err != nil {
				return fmt.Errorf("restore parked synthetic Desktop credential: %w", err)
			}
		} else if err := removePrivateFileIfPresent(e.destinationPath()); err != nil {
			return err
		}
		pending.Phase = PhaseRestored
		if err := e.writePending(pending); err != nil {
			return err
		}
	case PhaseRestored:
		// The credential write completed in an earlier attempt; continue with
		// cleanup without touching the destination again.
	default:
		return ErrRecoveryRequired
	}
	if err := removePrivateFileIfPresent(parkingPath); err != nil {
		return err
	}
	return removePrivateFileIfPresent(e.pendingPath())
}

func (e *Engine) restoreVerifiedLocked(journal Journal) error {
	switch journal.Phase {
	case PhaseVerified:
		if journal.PreviousExists {
			previous, err := securefs.ReadPrivateFile(e.previousPath(), 1<<20)
			if err != nil {
				return fmt.Errorf("read parked synthetic Desktop credential: %w", err)
			}
			if err := securefs.AtomicWriteFile(e.destinationPath(), previous); err != nil {
				return fmt.Errorf("restore parked synthetic Desktop credential: %w", err)
			}
		} else if err := removePrivateFileIfPresent(e.destinationPath()); err != nil {
			return err
		}
		journal.Phase = PhaseRestored
		if err := e.writeJournal(journal); err != nil {
			return err
		}
	case PhaseRestored:
		// Destination recovery already completed; only journal cleanup remains.
	default:
		return ErrRecoveryRequired
	}
	if err := removePrivateFileIfPresent(e.previousPath()); err != nil {
		return err
	}
	return removePrivateFileIfPresent(e.journalPath())
}

func (e *Engine) resolveTarget(target Target) (profile.Profile, error) {
	if target.ProfileID == "" || target.AuthGeneration == 0 || target.Lifecycle != profile.LifecycleReady {
		return profile.Profile{}, errors.New("invalid immutable Desktop activation target")
	}
	p, err := e.Store.Resolve(target.ProfileID)
	if err != nil {
		return profile.Profile{}, err
	}
	if p.ID != target.ProfileID || p.AuthGeneration != target.AuthGeneration || p.Lifecycle != target.Lifecycle || p.CredentialStore != profile.StoreFile {
		return profile.Profile{}, profile.ErrProfileChanged
	}
	if _, err := e.Store.ProfileRoot(p); err != nil {
		return profile.Profile{}, err
	}
	return p, nil
}

func (e *Engine) readDestination() ([]byte, bool, error) {
	b, err := securefs.ReadPrivateFile(e.destinationPath(), 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read synthetic Desktop credential: %w", err)
	}
	return b, true, nil
}

func (e *Engine) readJournal() (Journal, error) {
	return e.readJournalFile(e.journalPath())
}

func (e *Engine) readPending() (Journal, error) {
	return e.readJournalFile(e.pendingPath())
}

func (e *Engine) readJournalFile(path string) (Journal, error) {
	b, err := securefs.ReadPrivateFile(path, 64<<10)
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("decode synthetic activation journal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Journal{}, errors.New("synthetic activation journal contains multiple JSON values")
	}
	if (journal.Version != 1 && journal.Version != 2) || journal.ProfileID == "" || journal.AuthGeneration == 0 || journal.Lifecycle != string(profile.LifecycleReady) {
		return Journal{}, errors.New("synthetic activation journal is invalid")
	}
	switch journal.Phase {
	case PhasePrepared, PhaseParked, PhaseActivating, PhaseActivated, PhaseVerified, PhaseRestored:
		if journal.Version == 2 && journal.Parking != "" && journal.Parking != ParkingPrevious && journal.Parking != ParkingRollback {
			return Journal{}, errors.New("synthetic activation journal has invalid parking kind")
		}
		return journal, nil
	default:
		return Journal{}, errors.New("synthetic activation journal has invalid phase")
	}
}

func (e *Engine) writeJournal(journal Journal) error {
	return e.writeJournalFile(e.journalPath(), journal)
}

func (e *Engine) writePending(journal Journal) error {
	return e.writeJournalFile(e.pendingPath(), journal)
}

func (e *Engine) writeJournalFile(path string, journal Journal) error {
	b, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return securefs.AtomicWriteFile(path, append(b, '\n'))
}

func (e *Engine) destinationPath() string { return e.desktopAuth }
func (e *Engine) previousPath() string    { return filepath.Join(e.Root, "parking", "previous-auth.json") }
func (e *Engine) rollbackPath() string    { return filepath.Join(e.Root, "parking", "rollback-auth.json") }
func (e *Engine) parkingPath(journal Journal) string {
	if journal.Parking == ParkingRollback {
		return e.rollbackPath()
	}
	return e.previousPath()
}

func (e *Engine) parkingKindForRecovery(journal Journal) ParkingKind {
	if journal.Parking == ParkingPrevious || journal.Parking == ParkingRollback {
		return journal.Parking
	}
	// Version 1 did not persist this fact. A rollback backup is created only
	// for a switch from an already-active handoff, so prefer it when present.
	if _, err := os.Lstat(e.rollbackPath()); err == nil {
		return ParkingRollback
	}
	return ParkingPrevious
}

func (e *Engine) parkingPathForRecovery(journal Journal) string {
	if e.parkingKindForRecovery(journal) == ParkingRollback {
		return e.rollbackPath()
	}
	return e.previousPath()
}
func (e *Engine) journalPath() string { return filepath.Join(e.Root, "activation-journal.json") }
func (e *Engine) pendingPath() string { return filepath.Join(e.Root, "activation-pending.json") }
func (e *Engine) lockPath() string    { return filepath.Join(e.Root, "desktop-activation.lock") }

func removePrivateFile(path string) error {
	if err := securefs.EnsurePrivateFile(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private synthetic file %q: %w", filepath.Base(path), err)
	}
	return nil
}

func removePrivateFileIfPresent(path string) error {
	if err := securefs.EnsurePrivateFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove private synthetic file %q: %w", filepath.Base(path), err)
	}
	return nil
}

// SyntheticRoot reports whether root was created by NewSynthetic. It is useful
// to test callers and intentionally does not make any real Desktop action safe.
func SyntheticRoot(root string) bool {
	b, err := securefs.ReadPrivateFile(filepath.Join(root, syntheticMarkerName), 1024)
	return err == nil && strings.TrimSpace(string(b)) == strings.TrimSpace(syntheticMarkerContents)
}
