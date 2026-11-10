package usage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizePrefersMultiBucketAndPreservesCredits(t *testing.T) {
	const payload = `{
	  "rateLimits": {"limitId":"legacy","limitName":null,"primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},
	  "rateLimitsByLimitId": {
	    "codex": {
	      "limitId":"codex","limitName":"Codex","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1900000000},
	      "secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1900001000},
	      "credits":{"hasCredits":true,"unlimited":false,"balance":"240.50"},
	      "individualLimit":null,"spendControlReached":false,"planType":"plus","rateLimitReachedType":null
	    }
	  },
	  "rateLimitResetCredits":{"availableCount":3,"credits":null},
	  "accountId":"acct_synthetic",
	  "rateLimitUpsell":null
	}`
	var wire GetAccountRateLimitsResponse
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		t.Fatal(err)
	}
	got := Normalize(wire, time.Unix(1700000000, 0))
	if len(got.Buckets) != 1 {
		t.Fatalf("expected one multi bucket, got %d", len(got.Buckets))
	}
	b := got.Buckets["codex"]
	if b.Primary == nil || b.Primary.RemainingPercentDerived != 75 {
		t.Fatalf("unexpected primary: %#v", b.Primary)
	}
	if b.Secondary == nil || b.Secondary.RemainingPercentDerived != 60 {
		t.Fatalf("unexpected secondary: %#v", b.Secondary)
	}
	if b.Credits == nil || b.Credits.Balance == nil || *b.Credits.Balance != "240.50" {
		t.Fatalf("credits not preserved: %#v", b.Credits)
	}
	if got.ResetCredits == nil || got.ResetCredits.AvailableCount != 3 || got.ResetCredits.Credits != nil {
		t.Fatalf("reset credits null/detail semantics lost: %#v", got.ResetCredits)
	}
}

func TestNormalizeUnknownDoesNotBecomeFullQuota(t *testing.T) {
	got := Normalize(GetAccountRateLimitsResponse{}, time.Unix(0, 0))
	b := got.Buckets["legacy"]
	if b.Primary != nil || b.Secondary != nil || b.Credits != nil {
		t.Fatalf("missing values must remain unavailable: %#v", b)
	}
}

func TestNormalizeClampsDerivedRemainingWithoutChangingRawUsed(t *testing.T) {
	duration := int64(300)
	in := GetAccountRateLimitsResponse{RateLimits: RateLimitSnapshot{
		Primary: &RateLimitWindow{UsedPercent: 140, WindowDurationMins: &duration},
	}}
	got := Normalize(in, time.Unix(0, 0)).Buckets["legacy"].Primary
	if got == nil || got.RemainingPercentDerived != 0 || got.UsedPercent != 140 {
		t.Fatalf("unexpected normalization: %#v", got)
	}
}
