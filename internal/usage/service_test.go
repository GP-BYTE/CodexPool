package usage

import (
	"path/filepath"
	"testing"
	"time"

	"codexpool/internal/profile"
)

func TestAccountIdentityMismatchUsesKnownEmailOnly(t *testing.T) {
	known := profile.Profile{Identity: profile.Identity{Email: "Known@Example.invalid"}}
	same := "known@example.invalid"
	other := "other@example.invalid"
	if accountIdentityMismatch(known, GetAccountResponse{Account: &Account{Type: "chatgpt", Email: &same}}) {
		t.Fatal("email comparison should be case-insensitive")
	}
	if !accountIdentityMismatch(known, GetAccountResponse{Account: &Account{Type: "chatgpt", Email: &other}}) {
		t.Fatal("different known email must be treated as identity change")
	}
	if accountIdentityMismatch(profile.Profile{}, GetAccountResponse{Account: &Account{Type: "chatgpt", Email: &other}}) {
		t.Fatal("missing prior identity is not evidence of a mismatch")
	}
}

func TestLimitsIdentityMismatchUsesOpaqueBackendIDOnlyWhenBothKnown(t *testing.T) {
	idA := "account-a"
	idB := "account-b"
	p := profile.Profile{Identity: profile.Identity{BackendAccountID: idA}}
	if limitsIdentityMismatch(p, GetAccountRateLimitsResponse{AccountID: &idA}) {
		t.Fatal("same opaque backend account id must match")
	}
	if !limitsIdentityMismatch(p, GetAccountRateLimitsResponse{AccountID: &idB}) {
		t.Fatal("different opaque backend account id must fail closed")
	}
	if limitsIdentityMismatch(p, GetAccountRateLimitsResponse{}) {
		t.Fatal("server null account id must remain unknown, not mismatched")
	}
}

func TestComparableBucketRequiresUniqueFiveHourAndWeeklyMapping(t *testing.T) {
	five := FiveHourMinutes
	week := WeeklyMinutes
	window5 := &Window{WindowDurationMins: &five}
	windowW := &Window{WindowDurationMins: &week}
	snapshot := &Snapshot{Buckets: map[string]Bucket{
		"a": {Primary: window5, Secondary: windowW},
	}}
	key, _, ok, ambiguous := ComparableBucket(snapshot)
	if !ok || ambiguous || key != "a" {
		t.Fatalf("unexpected unique mapping: key=%q ok=%t ambiguous=%t", key, ok, ambiguous)
	}
	snapshot.Buckets["b"] = Bucket{Primary: windowW, Secondary: window5}
	if _, _, ok, ambiguous := ComparableBucket(snapshot); ok || !ambiguous {
		t.Fatalf("two matching buckets must be ambiguous: ok=%t ambiguous=%t", ok, ambiguous)
	}
}

func TestCreditTextDoesNotInventCurrency(t *testing.T) {
	balance := "240.50"
	got := CreditText(Bucket{Credits: &CreditsSnapshot{HasCredits: true, Balance: &balance}})
	if got != balance {
		t.Fatalf("credit balance must be preserved verbatim, got %q", got)
	}
}

func TestSnapshotCreditTextDoesNotDependOnComparableFiveHourWeeklyWindows(t *testing.T) {
	monthly := int64(43200)
	balance := "1000"
	snapshot := &Snapshot{Buckets: map[string]Bucket{
		"codex": {
			Primary: &Window{WindowDurationMins: &monthly, RemainingPercentDerived: 99},
			Credits: &CreditsSnapshot{HasCredits: true, Balance: &balance},
		},
	}}
	if _, _, ok, ambiguous := ComparableBucket(snapshot); ok || ambiguous {
		t.Fatalf("43200-minute window must not be guessed as 5H/weekly: ok=%t ambiguous=%t", ok, ambiguous)
	}
	got, ok, ambiguous := SnapshotCreditText(snapshot)
	if !ok || ambiguous || got != "1000" {
		t.Fatalf("credits must remain independently displayable, got=%q ok=%t ambiguous=%t", got, ok, ambiguous)
	}
}

func TestSnapshotCreditTextRejectsConflictingBucketBalances(t *testing.T) {
	one := "1000"
	two := "2000"
	snapshot := &Snapshot{Buckets: map[string]Bucket{
		"a": {Credits: &CreditsSnapshot{HasCredits: true, Balance: &one}},
		"b": {Credits: &CreditsSnapshot{HasCredits: true, Balance: &two}},
	}}
	if got, ok, ambiguous := SnapshotCreditText(snapshot); got != "—" || ok || !ambiguous {
		t.Fatalf("conflicting credit balances must stay unknown: got=%q ok=%t ambiguous=%t", got, ok, ambiguous)
	}
}

func TestSnapshotWindowsExposeStandardAndNonstandardDurationsWithoutGuessing(t *testing.T) {
	five := FiveHourMinutes
	month := int64(43200)
	snapshot := &Snapshot{Buckets: map[string]Bucket{
		"codex": {
			Primary:   &Window{WindowDurationMins: &month, RemainingPercentDerived: 99},
			Secondary: &Window{WindowDurationMins: &five, RemainingPercentDerived: 88},
		},
	}}
	windows := SnapshotWindows(snapshot)
	if len(windows) != 2 {
		t.Fatalf("expected both service windows to remain observable, got %#v", windows)
	}
	gotFive, ok, ambiguous := SnapshotWindowByDuration(snapshot, FiveHourMinutes)
	if !ok || ambiguous || gotFive.RemainingPercentDerived != 88 {
		t.Fatalf("unique 5H window was not resolved: %#v ok=%t ambiguous=%t", gotFive, ok, ambiguous)
	}
	if _, ok, ambiguous := SnapshotWindowByDuration(snapshot, WeeklyMinutes); ok || ambiguous {
		t.Fatalf("missing weekly window must stay unavailable: ok=%t ambiguous=%t", ok, ambiguous)
	}
	if windows[0].Window.WindowDurationMins == nil || *windows[0].Window.WindowDurationMins != month || windows[0].Window.RemainingPercentDerived != 99 {
		t.Fatalf("43200-minute service window must remain visible as itself: %#v", windows[0])
	}
}

func TestSnapshotWindowByDurationRejectsAmbiguousDuplicateDurations(t *testing.T) {
	five := FiveHourMinutes
	snapshot := &Snapshot{Buckets: map[string]Bucket{
		"a": {Primary: &Window{WindowDurationMins: &five, RemainingPercentDerived: 80}},
		"b": {Primary: &Window{WindowDurationMins: &five, RemainingPercentDerived: 60}},
	}}
	if _, ok, ambiguous := SnapshotWindowByDuration(snapshot, FiveHourMinutes); ok || !ambiguous {
		t.Fatalf("duplicate 5H windows must be ambiguous: ok=%t ambiguous=%t", ok, ambiguous)
	}
}

func TestCachedKeepsValidatedNormalizedSnapshotAcrossCodexVersions(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := WriteCache(p, CacheRecord{
		CodexVersion:  "0.152.0",
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot:      &Snapshot{FetchedAt: now, Buckets: map[string]Bucket{}},
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, "test-client", "0.153.4")
	got, err := service.Cached(p)
	if err != nil {
		t.Fatalf("validated normalized cache must remain readable across runtime upgrades: %v", err)
	}
	if got.CodexVersion != "0.152.0" {
		t.Fatalf("cache must retain runtime provenance, got %q", got.CodexVersion)
	}
}

func TestUnavailableRecordCarriesLastValidatedSnapshotAcrossCodexVersions(t *testing.T) {
	store, err := profile.NewStore(filepath.Join(t.TempDir(), "pool"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePending("a", profile.StoreFile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := WriteCache(p, CacheRecord{
		CodexVersion:  "0.152.0",
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot:      &Snapshot{FetchedAt: now, Buckets: map[string]Bucket{"old": {SourceKey: "old"}}},
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, "test-client", "0.153.4")
	record := service.unavailableRecord(p, "0.153.4", now.Add(time.Second), "rpcUnavailable")
	if record.Snapshot == nil || record.FetchedAt.IsZero() {
		t.Fatalf("last validated snapshot should remain displayable after a runtime upgrade failure: %#v", record)
	}
	if record.CodexVersion != "0.152.0" {
		t.Fatalf("carried snapshot must retain its original runtime provenance: %#v", record)
	}
}
