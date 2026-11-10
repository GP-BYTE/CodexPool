package usage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
	"codexpool/internal/scheduler"
)

type Service struct {
	Runtime         *codexruntime.Runtime
	ClientVersion   string
	EvidenceVersion string
	QueryTimeout    time.Duration
	StopTimeout     time.Duration
}

type FetchResult struct {
	Account GetAccountResponse `json:"account"`
	Cache   CacheRecord        `json:"cache"`
}

func NewService(runtime *codexruntime.Runtime, clientVersion, evidenceVersion string) *Service {
	if runtime != nil && strings.TrimSpace(evidenceVersion) != "" {
		runtime.ExpectedVersion = strings.TrimSpace(evidenceVersion)
	}
	return &Service{
		Runtime:         runtime,
		ClientVersion:   clientVersion,
		EvidenceVersion: evidenceVersion,
		QueryTimeout:    30 * time.Second,
		StopTimeout:     3 * time.Second,
	}
}

func (s *Service) Fetch(ctx context.Context, p profile.Profile) (FetchResult, error) {
	if s == nil || s.Runtime == nil || s.Runtime.Store == nil {
		return FetchResult{}, errors.New("usage service runtime is required")
	}
	attemptedAt := time.Now().UTC()
	queryTimeout := s.QueryTimeout
	if queryTimeout <= 0 {
		queryTimeout = 30 * time.Second
	}
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	server, runtimeVersion, err := s.Runtime.StartAppServer(queryCtx, p)
	if err != nil {
		if errors.Is(err, scheduler.ErrBusy) {
			current, resolveErr := s.Runtime.Store.Resolve(p.ID)
			if resolveErr == nil && profile.SameCommitIdentity(current, p) {
				if cached, cacheErr := s.readCompatibleCache(current); cacheErr == nil {
					cached = MarkCachedRunning(cached)
					return FetchResult{Cache: cached}, nil
				}
			}
		}
		// Runtime/app-server startup failures must not erase the last known-good
		// normalized snapshot from the dashboard. ReadCache already validates the
		// Profile generation, account binding, and normalized snapshot invariants.
		if cached, cacheErr := s.readCompatibleCache(p); cacheErr == nil {
			cached.Status = CacheUnavailable
			cached.LastAttemptAt = attemptedAt
			cached.ErrorCode = classifyError(err)
			return FetchResult{Cache: cached}, err
		}
		return FetchResult{}, err
	}
	defer server.ReleaseLease()

	var account GetAccountResponse
	var snapshot Snapshot
	queryErr := server.Client.Initialize(queryCtx, "codexpool", "CodexPool", s.ClientVersion)
	if queryErr == nil {
		account, queryErr = ReadAccountInitialized(queryCtx, server.Client)
		if queryErr == nil && accountIdentityMismatch(p, account) {
			queryErr = ErrIdentityChanged
		}
	}
	if queryErr == nil {
		var limits GetAccountRateLimitsResponse
		limits, queryErr = ReadRateLimitsInitialized(queryCtx, server.Client)
		if queryErr == nil && limitsIdentityMismatch(p, limits) {
			queryErr = ErrIdentityChanged
		}
		if queryErr == nil {
			snapshot = Normalize(limits, time.Now())
		}
	}

	stopTimeout := s.StopTimeout
	if stopTimeout <= 0 {
		stopTimeout = 3 * time.Second
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), stopTimeout)
	stopErr := server.Close(stopCtx)
	stopCancel()
	select {
	case <-server.ResourceDone():
		// Owned process/pipes are finalized. Keep the Profile lease until result
		// validation and cache/registry commit below have also completed.
	default:
		return FetchResult{Account: account}, fmt.Errorf("Codex app-server cleanup is still pending; Profile lease remains held: %w", stopErr)
	}

	current, commitErr := s.currentForCommit(p)
	if commitErr != nil {
		return FetchResult{Account: account}, commitErr
	}

	if queryErr != nil {
		if isAuthenticationFailure(queryErr) {
			updated, transitionErr := s.transitionAuthenticationFailure(current)
			if transitionErr != nil {
				return FetchResult{Account: account}, transitionErr
			}
			record := CacheRecord{
				ProfileID:      updated.ID,
				AuthGeneration: updated.AuthGeneration,
				CodexVersion:   runtimeVersion,
				LastAttemptAt:  attemptedAt,
				Status:         CacheUnavailable,
				ErrorCode:      classifyError(queryErr),
			}
			return FetchResult{Account: account, Cache: record}, queryErr
		}
		record := s.unavailableRecord(current, runtimeVersion, attemptedAt, classifyError(queryErr))
		if writeErr := WriteCache(current, record); writeErr != nil {
			return FetchResult{Account: account, Cache: record}, fmt.Errorf("write unavailable usage cache: %w", writeErr)
		}
		return FetchResult{Account: account, Cache: record}, queryErr
	}
	if stopErr != nil && !errors.Is(stopErr, os.ErrProcessDone) {
		record := s.unavailableRecord(current, runtimeVersion, attemptedAt, "appServerStop")
		record.Snapshot = &snapshot
		record.FetchedAt = snapshot.FetchedAt
		if writeErr := WriteCache(current, record); writeErr != nil {
			return FetchResult{Account: account, Cache: record}, fmt.Errorf("close app server and write unavailable cache: %w", writeErr)
		}
		return FetchResult{Account: account, Cache: record}, fmt.Errorf("close app server: %w", stopErr)
	}

	record := CacheRecord{
		ProfileID:      current.ID,
		AuthGeneration: current.AuthGeneration,
		CodexVersion:   runtimeVersion,
		FetchedAt:      snapshot.FetchedAt,
		LastAttemptAt:  attemptedAt,
		Status:         CacheFresh,
		Snapshot:       &snapshot,
	}
	if err := WriteCache(current, record); err != nil {
		return FetchResult{Account: account, Cache: record}, fmt.Errorf("write usage cache: %w", err)
	}
	return FetchResult{Account: account, Cache: record}, nil
}

func (s *Service) currentForCommit(expected profile.Profile) (profile.Profile, error) {
	current, err := s.Runtime.Store.Resolve(expected.ID)
	if err != nil {
		return profile.Profile{}, err
	}
	if !profile.SameCommitIdentity(current, expected) {
		return profile.Profile{}, fmt.Errorf("%w: %s", profile.ErrProfileChanged, expected.ID)
	}
	if current.Lifecycle != profile.LifecycleReady && current.Lifecycle != profile.LifecyclePending {
		return profile.Profile{}, fmt.Errorf("%w: profile %s is %s", profile.ErrProfileChanged, current.ID, current.Lifecycle)
	}
	return current, nil
}

func (s *Service) transitionAuthenticationFailure(expected profile.Profile) (profile.Profile, error) {
	updated, err := s.Runtime.Store.UpdateExpected(expected, func(current *profile.Profile) error {
		current.AuthGeneration++
		current.Lifecycle = profile.LifecycleReauthRequired
		current.Identity = profile.Identity{}
		return nil
	})
	if err != nil {
		return profile.Profile{}, err
	}
	if err := RemoveCache(updated); err != nil {
		return updated, fmt.Errorf("clear usage cache after authentication failure: %w", err)
	}
	return updated, nil
}

func isAuthenticationFailure(err error) bool {
	return errors.Is(err, ErrNotAuthenticated) || errors.Is(err, ErrNotChatGPT) || errors.Is(err, ErrIdentityChanged)
}

func (s *Service) Cached(p profile.Profile) (CacheRecord, error) {
	return s.readCompatibleCache(p)
}

func (s *Service) readCompatibleCache(p profile.Profile) (CacheRecord, error) {
	// CacheRecord stores CodexVersion for provenance, not as a decoding gate.
	// The persisted payload is CodexPool's normalized schema; ReadCache strictly
	// re-validates its structure, Profile generation, and account identity.
	return ReadCache(p)
}

func (s *Service) unavailableRecord(p profile.Profile, runtimeVersion string, attemptedAt time.Time, code string) CacheRecord {
	record := CacheRecord{
		ProfileID:      p.ID,
		AuthGeneration: p.AuthGeneration,
		CodexVersion:   runtimeVersion,
		LastAttemptAt:  attemptedAt,
		Status:         CacheUnavailable,
		ErrorCode:      code,
	}
	if previous, err := ReadCache(p); err == nil && previous.AuthGeneration == p.AuthGeneration {
		record.Snapshot = previous.Snapshot
		record.FetchedAt = previous.FetchedAt
		if previous.Snapshot != nil {
			// Keep the runtime provenance of the snapshot that is actually shown.
			// A failed attempt with a newer runtime must not relabel older data.
			record.CodexVersion = previous.CodexVersion
		}
	}
	return record
}

func accountIdentityMismatch(p profile.Profile, account GetAccountResponse) bool {
	if p.Identity.Email == "" || account.Account == nil || account.Account.Email == nil {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(p.Identity.Email), strings.TrimSpace(*account.Account.Email))
}

func limitsIdentityMismatch(p profile.Profile, limits GetAccountRateLimitsResponse) bool {
	if p.Identity.BackendAccountID == "" || limits.AccountID == nil {
		return false
	}
	return p.Identity.BackendAccountID != *limits.AccountID
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, ErrNotAuthenticated):
		return "notAuthenticated"
	case errors.Is(err, ErrNotChatGPT):
		return "notChatGPT"
	case errors.Is(err, ErrIdentityChanged):
		return "identityChanged"
	case errors.Is(err, ErrProtocolIncompatible):
		return "protocolIncompatible"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "rpcUnavailable"
	}
}
