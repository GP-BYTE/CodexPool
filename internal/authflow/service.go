package authflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
	"codexpool/internal/usage"
)

type Service struct {
	Store   *profile.Store
	Runtime *codexruntime.Runtime
	Usage   *usage.Service
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

func New(store *profile.Store, runtime *codexruntime.Runtime, usageService *usage.Service, stdin io.Reader, stdout, stderr io.Writer) *Service {
	return &Service{
		Store:   store,
		Runtime: runtime,
		Usage:   usageService,
		Stdin:   stdin,
		Stdout:  stdout,
		Stderr:  stderr,
	}
}

func (s *Service) Add(ctx context.Context, alias string, credentialStore profile.CredentialStore, deviceAuth bool) (profile.Profile, usage.FetchResult, error) {
	if s == nil || s.Store == nil || s.Runtime == nil || s.Usage == nil {
		return profile.Profile{}, usage.FetchResult{}, errors.New("authflow service is not configured")
	}
	if credentialStore != profile.StoreFile {
		return profile.Profile{}, usage.FetchResult{}, codexruntime.ErrUnsupportedCredentialStore
	}
	if _, err := s.Runtime.RequireLoginCompatible(ctx); err != nil {
		return profile.Profile{}, usage.FetchResult{}, err
	}
	p, err := s.Store.CreatePending(alias, credentialStore)
	if err != nil {
		return profile.Profile{}, usage.FetchResult{}, err
	}
	if err := s.Runtime.LoginInteractive(ctx, p, deviceAuth, s.Stdin, s.Stdout, s.Stderr); err != nil {
		return p, usage.FetchResult{}, fmt.Errorf("official Codex login did not complete; profile remains pending: %w", err)
	}
	return s.finalizeAuthenticated(ctx, p)
}

func (s *Service) Relogin(ctx context.Context, aliasOrID string, deviceAuth bool) (profile.Profile, usage.FetchResult, error) {
	p, err := s.Store.Resolve(aliasOrID)
	if err != nil {
		return profile.Profile{}, usage.FetchResult{}, err
	}
	return s.ReloginExpected(ctx, p, deviceAuth)
}

func (s *Service) ReloginExpected(ctx context.Context, expected profile.Profile, deviceAuth bool) (profile.Profile, usage.FetchResult, error) {
	p, err := s.Runtime.LoginAnd(ctx, expected, deviceAuth, s.Stdin, s.Stdout, s.Stderr, func(current profile.Profile) (profile.Profile, error) {
		if current.Lifecycle == profile.LifecycleDeleting || current.Lifecycle == profile.LifecycleLogoutPending || current.Lifecycle == profile.LifecyclePurgeLogoutPending {
			return current, fmt.Errorf("profile %s is %s and cannot start re-login", current.Alias, current.Lifecycle)
		}
		updated, updateErr := s.Store.UpdateExpected(current, func(next *profile.Profile) error {
			next.AuthGeneration++
			next.Lifecycle = profile.LifecyclePending
			next.Identity = profile.Identity{}
			return nil
		})
		if updateErr != nil {
			return profile.Profile{}, updateErr
		}
		if cacheErr := usage.RemoveCache(updated); cacheErr != nil {
			return updated, fmt.Errorf("re-login transition persisted but old usage cache could not be removed: %w", cacheErr)
		}
		return updated, nil
	})
	if err != nil {
		return p, usage.FetchResult{}, fmt.Errorf("official Codex re-login did not complete; Profile was not switched outside its exclusive lock: %w", err)
	}
	return s.finalizeAuthenticated(ctx, p)
}

func (s *Service) Logout(ctx context.Context, aliasOrID string) (profile.Profile, error) {
	p, err := s.Store.Resolve(aliasOrID)
	if err != nil {
		return profile.Profile{}, err
	}
	return s.LogoutExpected(ctx, p)
}

func (s *Service) LogoutExpected(ctx context.Context, expected profile.Profile) (profile.Profile, error) {
	var completed profile.Profile
	transitioned, err := s.Runtime.LogoutTransition(ctx, expected, s.Stdin, s.Stdout, s.Stderr, func(current profile.Profile) (profile.Profile, error) {
		if current.Lifecycle == profile.LifecycleDeleting || current.Lifecycle == profile.LifecyclePurgeLogoutPending {
			return current, fmt.Errorf("profile %s is %s and cannot run normal logout; finish the pending purge instead", current.Alias, current.Lifecycle)
		}
		if current.Lifecycle == profile.LifecycleLogoutPending {
			return current, nil
		}
		updated, updateErr := s.Store.UpdateExpected(current, func(next *profile.Profile) error {
			next.AuthGeneration++
			next.Lifecycle = profile.LifecycleLogoutPending
			next.Identity = profile.Identity{}
			return nil
		})
		if updateErr != nil {
			return current, updateErr
		}
		if cacheErr := usage.RemoveCache(updated); cacheErr != nil {
			return updated, fmt.Errorf("logoutPending persisted but usage cache cleanup failed: %w", cacheErr)
		}
		return updated, nil
	}, func(current profile.Profile) error {
		var updateErr error
		completed, updateErr = s.Store.UpdateExpected(current, func(next *profile.Profile) error {
			next.Lifecycle = profile.LifecycleReauthRequired
			next.Identity = profile.Identity{}
			return nil
		})
		return updateErr
	})
	if err != nil {
		if transitioned.ID != "" {
			return transitioned, fmt.Errorf("logout did not complete; recoverable state %s was preserved: %w", transitioned.Lifecycle, err)
		}
		return expected, fmt.Errorf("logout did not complete: %w", err)
	}
	return completed, nil
}

func (s *Service) Remove(ctx context.Context, aliasOrID string, purge bool) (profile.Profile, error) {
	p, err := s.Store.Resolve(aliasOrID)
	if err != nil {
		return profile.Profile{}, err
	}
	return s.RemoveExpected(ctx, p, purge)
}

func (s *Service) RemoveExpected(ctx context.Context, expected profile.Profile, purge bool) (profile.Profile, error) {
	p := expected
	if !purge {
		lock, lockErr := s.Runtime.Guard.TryAcquire(p.ID)
		if lockErr != nil {
			return p, lockErr
		}
		defer lock.Close()
		current, resolveErr := s.Store.Resolve(p.ID)
		if resolveErr != nil {
			return p, resolveErr
		}
		if !profile.SameCommitIdentity(current, p) {
			return current, codexruntime.ErrProfileChanged
		}
		if current.Lifecycle == profile.LifecycleLogoutPending || current.Lifecycle == profile.LifecyclePurgeLogoutPending || current.Lifecycle == profile.LifecycleDeleting {
			return current, fmt.Errorf("profile %s is %s; finish the pending recovery operation before unregistering", current.Alias, current.Lifecycle)
		}
		return s.Store.RemoveRegistration(current.ID)
	}

	if p.Lifecycle == profile.LifecycleDeleting {
		return s.finishDeleting(p)
	}

	var removed profile.Profile
	var deleting profile.Profile
	transitioned, err := s.Runtime.LogoutTransition(ctx, p, s.Stdin, s.Stdout, s.Stderr, func(current profile.Profile) (profile.Profile, error) {
		if current.Lifecycle == profile.LifecycleDeleting {
			return current, codexruntime.ErrProfileChanged
		}
		if current.Lifecycle == profile.LifecyclePurgeLogoutPending {
			return current, nil
		}
		updated, updateErr := s.Store.UpdateExpected(current, func(next *profile.Profile) error {
			next.AuthGeneration++
			next.Lifecycle = profile.LifecyclePurgeLogoutPending
			next.Identity = profile.Identity{}
			return nil
		})
		if updateErr != nil {
			return current, updateErr
		}
		if cacheErr := usage.RemoveCache(updated); cacheErr != nil {
			return updated, fmt.Errorf("purge logout tombstone persisted but usage cache cleanup failed: %w", cacheErr)
		}
		return updated, nil
	}, func(current profile.Profile) error {
		var updateErr error
		deleting, updateErr = s.Store.UpdateExpected(current, func(next *profile.Profile) error {
			next.Lifecycle = profile.LifecycleDeleting
			next.Identity = profile.Identity{}
			return nil
		})
		if updateErr != nil {
			return fmt.Errorf("official logout completed but deleting tombstone could not be persisted: %w", updateErr)
		}
		var finishErr error
		removed, finishErr = s.finishDeletingLocked(deleting)
		return finishErr
	})
	if err != nil {
		state := transitioned
		if deleting.ID != "" {
			state = deleting
		}
		if state.ID != "" {
			return state, fmt.Errorf("purge did not complete; recoverable state %s was preserved: %w", state.Lifecycle, err)
		}
		return p, fmt.Errorf("purge did not complete: %w", err)
	}
	return removed, nil
}

func (s *Service) finishDeleting(expected profile.Profile) (profile.Profile, error) {
	lock, err := s.Runtime.Guard.TryAcquire(expected.ID)
	if err != nil {
		return expected, err
	}
	defer lock.Close()
	current, err := s.Store.Resolve(expected.ID)
	if err != nil {
		return expected, err
	}
	if !profile.SameCommitIdentity(current, expected) || current.Lifecycle != profile.LifecycleDeleting {
		return current, codexruntime.ErrProfileChanged
	}
	return s.finishDeletingLocked(current)
}

func (s *Service) finishDeletingLocked(current profile.Profile) (profile.Profile, error) {
	if current.Lifecycle != profile.LifecycleDeleting {
		return current, fmt.Errorf("profile %s is %s, not deleting", current.Alias, current.Lifecycle)
	}
	if purgeErr := s.Store.PurgeData(current); purgeErr != nil {
		return current, fmt.Errorf("profile remains registered as deleting because local data purge failed: %w", purgeErr)
	}
	removed, removeErr := s.Store.RemoveRegistration(current.ID)
	if removeErr != nil {
		return current, fmt.Errorf("profile data was purged but deleting tombstone could not be removed: %w", removeErr)
	}
	return removed, nil
}

func (s *Service) Rename(aliasOrID, newAlias string) (profile.Profile, error) {
	return s.Store.Rename(aliasOrID, newAlias)
}

func (s *Service) finalizeAuthenticated(ctx context.Context, p profile.Profile) (profile.Profile, usage.FetchResult, error) {
	result, fetchErr := s.Usage.Fetch(ctx, p)
	if fetchErr != nil && requiresReauth(fetchErr) {
		current, resolveErr := s.Store.Resolve(p.ID)
		if resolveErr != nil {
			return p, result, resolveErr
		}
		return current, result, fetchErr
	}

	account := result.Account.Account
	if account == nil {
		if fetchErr != nil {
			return p, result, fmt.Errorf("official Codex login completed, but Profile identity could not be verified; Profile remains pending: %w", fetchErr)
		}
		return p, result, usage.ErrNotAuthenticated
	}
	if account.Type != "chatgpt" {
		return p, result, usage.ErrNotChatGPT
	}

	identity := profile.Identity{
		PlanType:   account.PlanType,
		ObservedAt: time.Now().UTC(),
	}
	if account.Email != nil {
		identity.Email = *account.Email
	}
	if result.Cache.Snapshot != nil && result.Cache.Snapshot.AccountID != nil {
		identity.BackendAccountID = *result.Cache.Snapshot.AccountID
	}

	lock, lockErr := s.Runtime.Guard.TryAcquire(p.ID)
	if lockErr != nil {
		return p, result, lockErr
	}
	defer lock.Close()
	current, resolveErr := s.Store.Resolve(p.ID)
	if resolveErr != nil {
		return p, result, resolveErr
	}
	if !profile.SameCommitIdentity(current, p) {
		return current, result, codexruntime.ErrProfileChanged
	}
	updated, updateErr := s.Store.UpdateExpected(current, func(next *profile.Profile) error {
		next.Lifecycle = profile.LifecycleReady
		next.Identity = identity
		return nil
	})
	if updateErr != nil {
		return current, result, updateErr
	}
	if fetchErr != nil {
		return updated, result, fmt.Errorf("authenticated profile is ready but usage is unavailable: %w", fetchErr)
	}
	return updated, result, nil
}

func requiresReauth(err error) bool {
	return errors.Is(err, usage.ErrNotAuthenticated) ||
		errors.Is(err, usage.ErrNotChatGPT) ||
		errors.Is(err, usage.ErrIdentityChanged)
}
