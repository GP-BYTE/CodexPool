package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codexpool/internal/profile"
	"codexpool/internal/securefs"
)

type CacheStatus string

const (
	CacheFresh         CacheStatus = "fresh"
	CacheStale         CacheStatus = "stale"
	CacheUnavailable   CacheStatus = "unavailable"
	CacheRunningCached CacheStatus = "runningCached"
)

type CacheRecord struct {
	ProfileID      string      `json:"profileId"`
	AuthGeneration uint64      `json:"authGeneration"`
	CodexVersion   string      `json:"codexVersion,omitempty"`
	FetchedAt      time.Time   `json:"fetchedAt,omitempty"`
	LastAttemptAt  time.Time   `json:"lastAttemptAt"`
	Status         CacheStatus `json:"status"`
	Snapshot       *Snapshot   `json:"snapshot,omitempty"`
	ErrorCode      string      `json:"errorCode,omitempty"`
}

func CachePath(p profile.Profile) (string, error) {
	profileRoot := filepath.Dir(p.CodexHome)
	path, err := securefs.SafeChild(profileRoot, "cache", "usage.json")
	if err != nil {
		return "", err
	}
	if err := securefs.ValidateManagedPath(profileRoot, path); err != nil {
		return "", err
	}
	return path, nil
}

func ReadCache(p profile.Profile) (CacheRecord, error) {
	path, err := CachePath(p)
	if err != nil {
		return CacheRecord{}, err
	}
	b, err := securefs.ReadPrivateFile(path, 4<<20)
	if err != nil {
		return CacheRecord{}, err
	}
	var record CacheRecord
	if err := json.Unmarshal(b, &record); err != nil {
		return CacheRecord{}, fmt.Errorf("decode usage cache: %w", err)
	}
	if record.ProfileID != p.ID || record.AuthGeneration != p.AuthGeneration {
		return CacheRecord{}, errors.New("usage cache identity generation mismatch")
	}
	if err := validateCacheRecord(record); err != nil {
		return CacheRecord{}, fmt.Errorf("validate usage cache: %w", err)
	}
	knownAccountID := strings.TrimSpace(p.Identity.BackendAccountID)
	if knownAccountID != "" && record.Snapshot != nil && record.Snapshot.AccountID != nil {
		cachedAccountID := strings.TrimSpace(*record.Snapshot.AccountID)
		if cachedAccountID != "" && cachedAccountID != knownAccountID {
			return CacheRecord{}, errors.New("usage cache account identity mismatch")
		}
	}
	return record, nil
}

func validateCacheRecord(record CacheRecord) error {
	switch record.Status {
	case CacheFresh, CacheStale, CacheUnavailable, CacheRunningCached:
	default:
		return fmt.Errorf("unknown cache status %q", record.Status)
	}
	if record.LastAttemptAt.IsZero() {
		return errors.New("cache lastAttemptAt is missing")
	}
	if record.Status == CacheFresh && record.Snapshot == nil {
		return errors.New("fresh cache is missing snapshot")
	}
	if record.Snapshot == nil {
		if !record.FetchedAt.IsZero() {
			return errors.New("cache fetchedAt is present without snapshot")
		}
		return nil
	}
	if record.FetchedAt.IsZero() || record.Snapshot.FetchedAt.IsZero() || !record.FetchedAt.Equal(record.Snapshot.FetchedAt) {
		return errors.New("cache fetchedAt does not match snapshot fetchedAt")
	}
	for key, bucket := range record.Snapshot.Buckets {
		if bucket.SourceKey != "" && bucket.SourceKey != key {
			return fmt.Errorf("bucket %q has inconsistent sourceKey %q", key, bucket.SourceKey)
		}
		if err := validateCachedWindow(bucket.Primary); err != nil {
			return fmt.Errorf("bucket %q primary: %w", key, err)
		}
		if err := validateCachedWindow(bucket.Secondary); err != nil {
			return fmt.Errorf("bucket %q secondary: %w", key, err)
		}
	}
	return nil
}

func validateCachedWindow(window *Window) error {
	if window == nil {
		return nil
	}
	remaining := window.RemainingPercentDerived
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) || remaining < 0 || remaining > 100 {
		return fmt.Errorf("remaining percentage %v is outside [0,100]", remaining)
	}
	expected := 100 - window.UsedPercent
	if math.IsNaN(expected) || math.IsInf(expected, 0) {
		return errors.New("used percentage is not finite")
	}
	if expected < 0 {
		expected = 0
	}
	if expected > 100 {
		expected = 100
	}
	if math.Abs(expected-remaining) > 1e-9 {
		return fmt.Errorf("derived remaining percentage %v does not match used percentage %v", remaining, window.UsedPercent)
	}
	return nil
}

func WriteCache(p profile.Profile, record CacheRecord) error {
	path, err := CachePath(p)
	if err != nil {
		return err
	}
	record.ProfileID = p.ID
	record.AuthGeneration = p.AuthGeneration
	if record.LastAttemptAt.IsZero() {
		record.LastAttemptAt = time.Now().UTC()
	}
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return securefs.AtomicWriteFile(path, b)
}

func RemoveCache(p profile.Profile) error {
	path, err := CachePath(p)
	if err != nil {
		return err
	}
	if err := securefs.EnsurePrivateFile(path); err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func MarkCachedRunning(record CacheRecord) CacheRecord {
	record.Status = CacheRunningCached
	return record
}

func IsFresh(record CacheRecord, maxAge time.Duration, now time.Time) bool {
	if record.Status != CacheFresh || record.Snapshot == nil || record.FetchedAt.IsZero() {
		return false
	}
	if record.FetchedAt.After(now) {
		return false
	}
	if maxAge <= 0 {
		return true
	}
	return now.Sub(record.FetchedAt) <= maxAge
}
