package usage

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Window struct {
	UsedPercent             float64 `json:"usedPercent"`
	RemainingPercentDerived float64 `json:"remainingPercentDerived"`
	WindowDurationMins      *int64  `json:"windowDurationMins,omitempty"`
	ResetsAt                *int64  `json:"resetsAt,omitempty"`
}

type Bucket struct {
	SourceKey            string                     `json:"sourceKey"`
	LimitID              *string                    `json:"limitId,omitempty"`
	LimitName            *string                    `json:"limitName,omitempty"`
	Primary              *Window                    `json:"primary,omitempty"`
	Secondary            *Window                    `json:"secondary,omitempty"`
	Credits              *CreditsSnapshot           `json:"credits,omitempty"`
	IndividualLimit      *SpendControlLimitSnapshot `json:"individualLimit,omitempty"`
	SpendControlReached  *bool                      `json:"spendControlReached,omitempty"`
	PlanType             *string                    `json:"planType,omitempty"`
	RateLimitReachedType *string                    `json:"rateLimitReachedType,omitempty"`
}

type Snapshot struct {
	AccountID    *string                       `json:"accountId,omitempty"`
	FetchedAt    time.Time                     `json:"fetchedAt"`
	Buckets      map[string]Bucket             `json:"buckets"`
	ResetCredits *RateLimitResetCreditsSummary `json:"resetCredits,omitempty"`
	Warnings     []string                      `json:"warnings,omitempty"`
}

func Normalize(in GetAccountRateLimitsResponse, now time.Time) Snapshot {
	out := Snapshot{
		AccountID:    in.AccountID,
		FetchedAt:    now.UTC(),
		Buckets:      make(map[string]Bucket),
		ResetCredits: in.RateLimitResetCredits,
	}
	if len(in.RateLimitsByLimitID) > 0 {
		keys := make([]string, 0, len(in.RateLimitsByLimitID))
		for key := range in.RateLimitsByLimitID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			snapshot := in.RateLimitsByLimitID[key]
			if snapshot.LimitID != nil && *snapshot.LimitID != key {
				out.Warnings = append(out.Warnings, fmt.Sprintf("bucket key %q differs from limitId %q", key, *snapshot.LimitID))
			}
			out.Buckets[key] = normalizeBucket(key, snapshot)
		}
		return out
	}

	key := "legacy"
	if in.RateLimits.LimitID != nil && *in.RateLimits.LimitID != "" {
		key = *in.RateLimits.LimitID
	}
	out.Buckets[key] = normalizeBucket(key, in.RateLimits)
	return out
}

func normalizeBucket(key string, in RateLimitSnapshot) Bucket {
	return Bucket{
		SourceKey:            key,
		LimitID:              in.LimitID,
		LimitName:            in.LimitName,
		Primary:              normalizeWindow(in.Primary),
		Secondary:            normalizeWindow(in.Secondary),
		Credits:              in.Credits,
		IndividualLimit:      in.IndividualLimit,
		SpendControlReached:  in.SpendControlReached,
		PlanType:             in.PlanType,
		RateLimitReachedType: in.RateLimitReachedType,
	}
}

func normalizeWindow(in *RateLimitWindow) *Window {
	if in == nil {
		return nil
	}
	remaining := 100 - in.UsedPercent
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) {
		remaining = 0
	}
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return &Window{
		UsedPercent:             in.UsedPercent,
		RemainingPercentDerived: remaining,
		WindowDurationMins:      in.WindowDurationMins,
		ResetsAt:                in.ResetsAt,
	}
}
