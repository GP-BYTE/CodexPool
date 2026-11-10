package selector

import (
	"errors"
	"testing"
	"time"

	"codexpool/internal/profile"
	"codexpool/internal/usage"
)

func TestRankUsesMinimumWindowThenWeeklyAndActiveTieBreak(t *testing.T) {
	now := time.Unix(1900000000, 0)
	a := candidate("p_a", "a", 90, 60, now)
	b := candidate("p_b", "b", 70, 80, now)
	c := candidate("p_c", "c", 70, 80, now)

	ranked := Rank([]Candidate{a, c, b}, "p_b", time.Minute, now)
	if len(ranked) != 3 {
		t.Fatalf("expected three candidates, got %d", len(ranked))
	}
	if ranked[0].Alias != "b" {
		t.Fatalf("active stable tie-break should choose b, got %#v", ranked)
	}
	if ranked[0].MinimumLeft != 70 || ranked[0].WeeklyLeft != 80 {
		t.Fatalf("unexpected top score: %#v", ranked[0])
	}
	if ranked[2].Alias != "a" {
		t.Fatalf("minimum window should rank a last despite high 5H: %#v", ranked)
	}
}

func TestAutoFailsClosedForUncertainInstalledProtocol(t *testing.T) {
	now := time.Unix(1900000000, 0)
	_, err := Auto([]Candidate{candidate("p_a", "a", 90, 90, now)}, "", time.Minute, now, false)
	if err == nil || !errors.Is(err, ErrNoReliableCandidate) {
		t.Fatalf("expected conservative auto refusal, got %v", err)
	}
	got, err := Auto([]Candidate{candidate("p_a", "a", 90, 90, now)}, "", time.Minute, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Uncertain || got.Alias != "a" {
		t.Fatalf("unexpected explicit uncertain selection: %#v", got)
	}
}

func TestRankExcludesSpendControlReachedAndStale(t *testing.T) {
	now := time.Unix(1900000000, 0)
	blocked := candidate("p_a", "a", 90, 90, now)
	bucket := blocked.Cache.Snapshot.Buckets["codex"]
	reached := true
	bucket.SpendControlReached = &reached
	blocked.Cache.Snapshot.Buckets["codex"] = bucket

	stale := candidate("p_b", "b", 90, 90, now.Add(-10*time.Minute))
	ranked := Rank([]Candidate{blocked, stale}, "", time.Minute, now)
	if len(ranked) != 0 {
		t.Fatalf("blocked/stale profiles must not rank: %#v", ranked)
	}
}

func TestRankRejectsCacheIdentityMismatchAndKnownZeroRemaining(t *testing.T) {
	now := time.Unix(1900000000, 0)
	wrongGeneration := candidate("p_a", "a", 80, 80, now)
	wrongGeneration.Cache.AuthGeneration++
	zeroRemaining := candidate("p_b", "b", 0, 90, now)
	outOfRange := candidate("p_c", "c", 101, 90, now)

	if ranked := Rank([]Candidate{wrongGeneration, zeroRemaining, outOfRange}, "", time.Minute, now); len(ranked) != 0 {
		t.Fatalf("mismatched or unusable candidates must fail closed: %#v", ranked)
	}
}

func candidate(id, alias string, fiveLeft, weeklyLeft float64, fetchedAt time.Time) Candidate {
	fiveDuration := usage.FiveHourMinutes
	weekDuration := usage.WeeklyMinutes
	five := &usage.Window{
		UsedPercent:             100 - fiveLeft,
		RemainingPercentDerived: fiveLeft,
		WindowDurationMins:      &fiveDuration,
	}
	week := &usage.Window{
		UsedPercent:             100 - weeklyLeft,
		RemainingPercentDerived: weeklyLeft,
		WindowDurationMins:      &weekDuration,
	}
	return Candidate{
		Profile: profile.Profile{
			ID:             id,
			Alias:          alias,
			Lifecycle:      profile.LifecycleReady,
			AuthGeneration: 1,
		},
		Cache: usage.CacheRecord{
			ProfileID:      id,
			AuthGeneration: 1,
			Status:         usage.CacheFresh,
			FetchedAt:      fetchedAt,
			Snapshot: &usage.Snapshot{
				FetchedAt: fetchedAt,
				Buckets: map[string]usage.Bucket{
					"codex": {
						SourceKey: "codex",
						Primary:   five,
						Secondary: week,
					},
				},
			},
		},
	}
}
