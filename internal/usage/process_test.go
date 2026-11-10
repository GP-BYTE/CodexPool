//go:build !windows

package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codexpool/internal/codexruntime"
	"codexpool/internal/profile"
)

func TestFetchUsesShortLivedSyntheticAppServerAndCachesAllowlistedSnapshot(t *testing.T) {
	store, p := readyUsageProfile(t, "")
	script := fakeAppServerScript(t)
	rt, err := codexruntime.New(script, store)
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "app-env.txt")
	t.Setenv("FAKE_APP_CAPTURE", capture)
	t.Setenv("OPENAI_BASE_URL", "https://override.invalid")
	service := NewService(rt, "test-client", "0.153.4")
	// Race-instrumented package-wide test runs can make even the synthetic
	// version probe exceed two seconds on a loaded host. Keep this timeout well
	// below the production default while avoiding a test-only scheduling race.
	service.QueryTimeout = 10 * time.Second
	service.StopTimeout = 5 * time.Second

	result, err := service.Fetch(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cache.Status != CacheFresh || result.Cache.Snapshot == nil {
		t.Fatalf("unexpected cache: %#v", result.Cache)
	}
	bucket := result.Cache.Snapshot.Buckets["codex"]
	five := WindowByDuration(bucket, FiveHourMinutes)
	week := WindowByDuration(bucket, WeeklyMinutes)
	if five == nil || five.RemainingPercentDerived != 88 {
		t.Fatalf("unexpected 5H window: %#v", five)
	}
	if week == nil || week.RemainingPercentDerived != 66 {
		t.Fatalf("unexpected weekly window: %#v", week)
	}
	if got := CreditText(bucket); got != "synthetic-credit" {
		t.Fatalf("unexpected credit value: %q", got)
	}
	if result.Cache.Snapshot.ResetCredits == nil || result.Cache.Snapshot.ResetCredits.AvailableCount != 2 {
		t.Fatalf("unexpected reset credits: %#v", result.Cache.Snapshot.ResetCredits)
	}
	cached, err := ReadCache(p)
	if err != nil {
		t.Fatal(err)
	}
	if cached.ProfileID != p.ID || cached.AuthGeneration != p.AuthGeneration {
		t.Fatalf("cache lost profile/generation binding: %#v", cached)
	}

	envCapture, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	text := string(envCapture)
	if !strings.Contains(text, "CODEX_HOME="+p.CodexHome) {
		t.Fatalf("app-server did not receive isolated home:\n%s", text)
	}
	if !strings.Contains(text, "OPENAI_BASE_URL=unset") {
		t.Fatalf("endpoint override leaked into app-server:\n%s", text)
	}
	if !strings.Contains(text, filepath.Join(filepath.Dir(p.CodexHome), "cache", "query-cwd")) {
		t.Fatalf("app-server did not start in neutral private cwd:\n%s", text)
	}
}

func TestFetchAcceptsCompatibleQuotaProtocolFromNewerRuntime(t *testing.T) {
	store, p := readyUsageProfile(t, "")
	script := fakeAppServerScript(t)
	rt, err := codexruntime.New(script, store)
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "app-env.txt")
	t.Setenv("FAKE_APP_CAPTURE", capture)
	t.Setenv("FAKE_APP_VERSION", "0.999.0")
	service := NewService(rt, "test-client", "0.153.4")
	service.QueryTimeout = 10 * time.Second
	service.StopTimeout = 5 * time.Second

	result, err := service.Fetch(context.Background(), p)
	if err != nil {
		t.Fatalf("compatible read-only protocol must survive a runtime version bump: %v", err)
	}
	if result.Cache.Status != CacheFresh || result.Cache.Snapshot == nil {
		t.Fatalf("unexpected cache after runtime upgrade: %#v", result.Cache)
	}
	if result.Cache.CodexVersion != "0.999.0" {
		t.Fatalf("cache must record actual runtime provenance, got %q", result.Cache.CodexVersion)
	}
	bucket := result.Cache.Snapshot.Buckets["codex"]
	if five := WindowByDuration(bucket, FiveHourMinutes); five == nil || five.RemainingPercentDerived != 88 {
		t.Fatalf("compatible upgraded runtime lost 5H quota: %#v", five)
	}
	if week := WindowByDuration(bucket, WeeklyMinutes); week == nil || week.RemainingPercentDerived != 66 {
		t.Fatalf("compatible upgraded runtime lost weekly quota: %#v", week)
	}
}

func TestFetchBusyReturnsRunningCachedWithoutSecondReader(t *testing.T) {
	store, p := readyUsageProfile(t, "")
	rt, _ := codexruntime.New(filepath.Join(t.TempDir(), "unused-codex"), store)
	service := NewService(rt, "test-client", "0.153.4")
	now := time.Now().UTC()
	if err := WriteCache(p, CacheRecord{
		CodexVersion:  "0.153.4",
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot:      &Snapshot{FetchedAt: now, Buckets: map[string]Bucket{}},
	}); err != nil {
		t.Fatal(err)
	}
	lock, err := rt.Guard.TryAcquire(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	result, err := service.Fetch(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cache.Status != CacheRunningCached {
		t.Fatalf("busy profile should expose cached-running state, got %s", result.Cache.Status)
	}
}

func TestFetchIdentityChangeClearsOldSnapshotAndFailsClosed(t *testing.T) {
	store, p := readyUsageProfile(t, "different@example.invalid")
	script := fakeAppServerScript(t)
	rt, _ := codexruntime.New(script, store)
	capture := filepath.Join(t.TempDir(), "identity.txt")
	t.Setenv("FAKE_APP_CAPTURE", capture)
	service := NewService(rt, "test-client", "0.153.4")
	service.QueryTimeout = 10 * time.Second
	service.StopTimeout = 5 * time.Second
	now := time.Now().UTC()
	if err := WriteCache(p, CacheRecord{
		FetchedAt:     now,
		LastAttemptAt: now,
		Status:        CacheFresh,
		Snapshot:      &Snapshot{FetchedAt: now, Buckets: map[string]Bucket{"old": {SourceKey: "old"}}},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := service.Fetch(context.Background(), p)
	if err == nil || !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("expected identity change rejection, got %v", err)
	}
	if result.Cache.Status != CacheUnavailable || result.Cache.ErrorCode != "identityChanged" {
		t.Fatalf("unexpected identity-change cache state: %#v", result.Cache)
	}
	if result.Cache.Snapshot != nil {
		t.Fatal("old account snapshot must be cleared on identity change")
	}
	current, err := store.Resolve(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AuthGeneration != p.AuthGeneration+1 || current.Lifecycle != profile.LifecycleReauthRequired {
		t.Fatalf("identity change must advance generation and require reauth: %#v", current)
	}
	if _, err := ReadCache(p); err == nil {
		t.Fatal("old-generation cache must be removed after identity change")
	}
	if _, err := ReadCache(current); err == nil {
		t.Fatal("identity change must not persist an unavailable snapshot under the new generation")
	}
}

func readyUsageProfile(t *testing.T, email string) (*profile.Store, profile.Profile) {
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
		current.Identity.PlanType = "plus"
		current.Identity.ObservedAt = time.Now().UTC()
		if email != "" {
			current.Identity.Email = email
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, p
}

func fakeAppServerScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-app-server.sh")
	script := `#!/bin/sh
if [ "${1-unset}" = "--version" ]; then
  printf 'codex-cli %s\n' "${FAKE_APP_VERSION-0.153.4}"
  exit 0
fi
if [ "${1-unset}" != "app-server" ]; then
  exit 23
fi
{
  printf 'CODEX_HOME=%s\n' "$CODEX_HOME"
  printf 'OPENAI_BASE_URL=%s\n' "${OPENAI_BASE_URL-unset}"
  printf 'PWD=%s\n' "$PWD"
} > "$FAKE_APP_CAPTURE"
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      printf '%s\n' '{"id":1,"result":{}}'
      ;;
    *'"method":"initialized"'*)
      ;;
    *'"method":"account/read"'*)
      printf '%s\n' '{"id":2,"result":{"account":{"type":"chatgpt","email":"synthetic@example.invalid","planType":"plus"},"requiresOpenaiAuth":true}}'
      ;;
    *'"method":"account/rateLimits/read"'*)
      printf '%s\n' '{"id":3,"result":{"ordinaryUsageAllowed":null,"rateLimits":{"limitId":"codex","limitName":"Codex","normalModelSlug":null,"primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1900000000},"secondary":{"usedPercent":34,"windowDurationMins":10080,"resetsAt":1900001000},"credits":{"hasCredits":true,"unlimited":false,"balance":"synthetic-credit"},"individualLimit":null,"spendControlReached":false,"planType":"plus","rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":{"availableCount":2,"credits":null},"accountId":"synthetic-account","rateLimitUpsell":null}}'
      ;;
  esac
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
