package selector

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"codexpool/internal/profile"
	"codexpool/internal/usage"
)

var ErrNoReliableCandidate = errors.New("no reliable Codex profile candidate")

type Candidate struct {
	Profile profile.Profile
	Cache   usage.CacheRecord
	Busy    bool
}

type Result struct {
	ProfileID        string   `json:"profileId"`
	Alias            string   `json:"alias"`
	BucketKey        string   `json:"bucketKey"`
	FiveHourLeft     float64  `json:"fiveHourLeft"`
	WeeklyLeft       float64  `json:"weeklyLeft"`
	MinimumLeft      float64  `json:"minimumLeft"`
	Uncertain        bool     `json:"uncertain"`
	Reasons          []string `json:"reasons,omitempty"`
	ActiveTieBreaker bool     `json:"activeTieBreaker,omitempty"`
}

func Rank(candidates []Candidate, activeProfileID string, maxAge time.Duration, now time.Time) []Result {
	results := make([]Result, 0, len(candidates))
	for _, candidate := range candidates {
		p := candidate.Profile
		if p.Lifecycle != profile.LifecycleReady || candidate.Busy {
			continue
		}
		if candidate.Cache.ProfileID != p.ID || candidate.Cache.AuthGeneration != p.AuthGeneration {
			continue
		}
		if !usage.IsFresh(candidate.Cache, maxAge, now) {
			continue
		}
		bucketKey, bucket, ok, ambiguous := usage.ComparableBucket(candidate.Cache.Snapshot)
		if !ok || ambiguous {
			continue
		}
		if bucket.SpendControlReached != nil && *bucket.SpendControlReached {
			continue
		}
		if bucket.RateLimitReachedType != nil && *bucket.RateLimitReachedType != "" {
			continue
		}
		five := usage.WindowByDuration(bucket, usage.FiveHourMinutes)
		week := usage.WindowByDuration(bucket, usage.WeeklyMinutes)
		if !selectableWindow(five) || !selectableWindow(week) {
			continue
		}
		minimum := five.RemainingPercentDerived
		if week.RemainingPercentDerived < minimum {
			minimum = week.RemainingPercentDerived
		}
		result := Result{
			ProfileID:        p.ID,
			Alias:            p.Alias,
			BucketKey:        bucketKey,
			FiveHourLeft:     five.RemainingPercentDerived,
			WeeklyLeft:       week.RemainingPercentDerived,
			MinimumLeft:      minimum,
			Uncertain:        true,
			ActiveTieBreaker: p.ID == activeProfileID,
			Reasons: []string{
				"CodexPool Preview policy keeps automatic account selection disabled; ordinary usage permission is not used for automation",
				"credits are displayed separately and are not converted into a ranking score",
			},
		}
		results = append(results, result)
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].MinimumLeft != results[j].MinimumLeft {
			return results[i].MinimumLeft > results[j].MinimumLeft
		}
		if results[i].WeeklyLeft != results[j].WeeklyLeft {
			return results[i].WeeklyLeft > results[j].WeeklyLeft
		}
		if results[i].ActiveTieBreaker != results[j].ActiveTieBreaker {
			return results[i].ActiveTieBreaker
		}
		return results[i].ProfileID < results[j].ProfileID
	})
	return results
}

func Best(candidates []Candidate, activeProfileID string, maxAge time.Duration, now time.Time) (Result, error) {
	ranked := Rank(candidates, activeProfileID, maxAge, now)
	if len(ranked) == 0 {
		return Result{}, ErrNoReliableCandidate
	}
	return ranked[0], nil
}

func Auto(candidates []Candidate, activeProfileID string, maxAge time.Duration, now time.Time, allowUncertain bool) (Result, error) {
	best, err := Best(candidates, activeProfileID, maxAge, now)
	if err != nil {
		return Result{}, err
	}
	if best.Uncertain && !allowUncertain {
		return Result{}, fmt.Errorf("%w: best candidate %q is uncertain because the installed protocol evidence cannot prove ordinary usage is allowed", ErrNoReliableCandidate, best.Alias)
	}
	return best, nil
}

func selectableWindow(window *usage.Window) bool {
	if window == nil {
		return false
	}
	remaining := window.RemainingPercentDerived
	return !math.IsNaN(remaining) && !math.IsInf(remaining, 0) && remaining > 0 && remaining <= 100
}
