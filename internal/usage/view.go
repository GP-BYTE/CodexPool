package usage

import "sort"

const (
	FiveHourMinutes = int64(300)
	WeeklyMinutes   = int64(10080)
)

func WindowByDuration(bucket Bucket, durationMins int64) *Window {
	for _, window := range []*Window{bucket.Primary, bucket.Secondary} {
		if window != nil && window.WindowDurationMins != nil && *window.WindowDurationMins == durationMins {
			return window
		}
	}
	return nil
}

func ComparableBucket(snapshot *Snapshot) (string, Bucket, bool, bool) {
	if snapshot == nil || len(snapshot.Buckets) == 0 {
		return "", Bucket{}, false, false
	}
	keys := make([]string, 0, len(snapshot.Buckets))
	for key := range snapshot.Buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var selectedKey string
	var selected Bucket
	count := 0
	for _, key := range keys {
		bucket := snapshot.Buckets[key]
		if WindowByDuration(bucket, FiveHourMinutes) != nil && WindowByDuration(bucket, WeeklyMinutes) != nil {
			selectedKey = key
			selected = bucket
			count++
		}
	}
	switch {
	case count == 0:
		return "", Bucket{}, false, false
	case count == 1:
		return selectedKey, selected, true, false
	default:
		return "", Bucket{}, false, true
	}
}

func CreditText(bucket Bucket) string {
	if bucket.Credits == nil {
		return "—"
	}
	if bucket.Credits.Unlimited {
		return "unlimited"
	}
	if bucket.Credits.Balance != nil {
		return *bucket.Credits.Balance
	}
	if bucket.Credits.HasCredits {
		return "available"
	}
	return "none"
}

func SnapshotCreditText(snapshot *Snapshot) (string, bool, bool) {
	if snapshot == nil || len(snapshot.Buckets) == 0 {
		return "—", false, false
	}
	keys := make([]string, 0, len(snapshot.Buckets))
	for key := range snapshot.Buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var selected string
	found := false
	for _, key := range keys {
		bucket := snapshot.Buckets[key]
		if bucket.Credits == nil {
			continue
		}
		value := CreditText(bucket)
		if !found {
			selected = value
			found = true
			continue
		}
		if value != selected {
			return "—", false, true
		}
	}
	if !found {
		return "—", false, false
	}
	return selected, true, false
}

type SnapshotWindow struct {
	SourceKey string
	LimitName string
	Window    Window
}

func SnapshotWindows(snapshot *Snapshot) []SnapshotWindow {
	if snapshot == nil || len(snapshot.Buckets) == 0 {
		return nil
	}
	keys := make([]string, 0, len(snapshot.Buckets))
	for key := range snapshot.Buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	windows := make([]SnapshotWindow, 0, len(keys)*2)
	for _, key := range keys {
		bucket := snapshot.Buckets[key]
		name := ""
		if bucket.LimitName != nil {
			name = *bucket.LimitName
		}
		for _, window := range []*Window{bucket.Primary, bucket.Secondary} {
			if window == nil {
				continue
			}
			windows = append(windows, SnapshotWindow{SourceKey: key, LimitName: name, Window: *window})
		}
	}
	return windows
}

func SnapshotWindowByDuration(snapshot *Snapshot, durationMins int64) (Window, bool, bool) {
	var selected Window
	count := 0
	for _, candidate := range SnapshotWindows(snapshot) {
		if candidate.Window.WindowDurationMins == nil || *candidate.Window.WindowDurationMins != durationMins {
			continue
		}
		selected = candidate.Window
		count++
	}
	switch {
	case count == 0:
		return Window{}, false, false
	case count == 1:
		return selected, true, false
	default:
		return Window{}, false, true
	}
}

func ResetCreditText(record CacheRecord) string {
	if record.Snapshot == nil || record.Snapshot.ResetCredits == nil {
		return "—"
	}
	return formatInt64(record.Snapshot.ResetCredits.AvailableCount)
}

func formatInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}
