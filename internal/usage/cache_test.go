package usage

import (
	"path/filepath"
	"testing"
	"time"

	"codexpool/internal/profile"
)

func TestCacheIsBoundToProfileAndAuthGeneration(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1900000000, 0).UTC()
	record := CacheRecord{
		CodexVersion:  "0.153.4",
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot:      &Snapshot{FetchedAt: now, Buckets: map[string]Bucket{}},
	}
	if err := WriteCache(p, record); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCache(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != p.ID || got.AuthGeneration != p.AuthGeneration {
		t.Fatalf("cache binding mismatch: %#v", got)
	}

	newGeneration := p
	newGeneration.AuthGeneration++
	if _, err := ReadCache(newGeneration); err == nil {
		t.Fatal("stale cache must not be accepted after auth generation changes")
	}
}

func TestReadCacheRejectsKnownBackendAccountMismatch(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	p.Identity.BackendAccountID = "acct_expected"
	cachedAccount := "acct_other"
	now := time.Unix(1900000000, 0).UTC()
	if err := WriteCache(p, CacheRecord{
		CodexVersion:  "0.153.4",
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot:      &Snapshot{AccountID: &cachedAccount, FetchedAt: now, Buckets: map[string]Bucket{}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(p); err == nil {
		t.Fatal("cache with a different known backend account id must fail closed")
	}
	unknownIdentity := p
	unknownIdentity.Identity.BackendAccountID = ""
	if _, err := ReadCache(unknownIdentity); err != nil {
		t.Fatalf("missing local account id must remain unknown rather than mismatched: %v", err)
	}
}

func TestFreshnessRequiresFreshStatusAndAge(t *testing.T) {
	now := time.Unix(1900000000, 0).UTC()
	record := CacheRecord{
		Status:    CacheFresh,
		FetchedAt: now.Add(-30 * time.Second),
		Snapshot:  &Snapshot{FetchedAt: now.Add(-30 * time.Second)},
	}
	if !IsFresh(record, time.Minute, now) {
		t.Fatal("expected fresh cache")
	}
	record.Status = CacheStale
	if IsFresh(record, time.Minute, now) {
		t.Fatal("stale status must not be treated as fresh")
	}
	record.Status = CacheFresh
	record.FetchedAt = now.Add(-2 * time.Minute)
	if IsFresh(record, time.Minute, now) {
		t.Fatal("expired cache must not be treated as fresh")
	}
	record.FetchedAt = now.Add(time.Minute)
	if IsFresh(record, time.Minute, now) {
		t.Fatal("future-dated cache must not be treated as fresh")
	}
}

func TestReadCacheMissingIsNotSyntheticSuccess(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	p, _ := store.CreatePending("a", profile.StoreFile)
	if _, err := ReadCache(p); err == nil {
		t.Fatal("missing cache must remain unavailable")
	}
}

func TestReadCacheRejectsCorruptFreshSnapshotInvariants(t *testing.T) {
	store, _ := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	p, _ := store.CreatePending("a", profile.StoreFile)
	now := time.Unix(1900000000, 0).UTC()
	five := FiveHourMinutes
	bad := CacheRecord{
		CodexVersion:  "0.153.4",
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot: &Snapshot{
			FetchedAt: now,
			Buckets: map[string]Bucket{
				"codex": {
					SourceKey: "codex",
					Primary: &Window{
						UsedPercent:             10,
						RemainingPercentDerived: 500,
						WindowDurationMins:      &five,
					},
				},
			},
		},
	}
	if err := WriteCache(p, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(p); err == nil {
		t.Fatal("corrupt derived percentage must make the cache unavailable")
	}

	bad.Status = CacheStatus("invented")
	bad.Snapshot = nil
	bad.FetchedAt = time.Time{}
	if err := WriteCache(p, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(p); err == nil {
		t.Fatal("unknown cache status must fail closed")
	}
}
