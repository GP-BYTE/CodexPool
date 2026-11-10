package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"codexpool/internal/apprpc"
)

func TestQueryUsesOnlyOfficialReadMethodsWithSyntheticServer(t *testing.T) {
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	defer clientRead.Close()
	defer serverWrite.Close()
	defer serverRead.Close()
	defer clientWrite.Close()

	client := apprpc.NewClient(clientRead, clientWrite, 128*1024)
	methods := make(chan []string, 1)
	accountParams := make(chan json.RawMessage, 1)
	go func() {
		scanner := bufio.NewScanner(serverRead)
		seen := make([]string, 0, 4)
		for scanner.Scan() {
			var request struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				return
			}
			seen = append(seen, request.Method)
			switch request.Method {
			case "initialize":
				_, _ = io.WriteString(serverWrite, `{"id":1,"result":{}}`+"\n")
			case "initialized":
			case "account/read":
				accountParams <- append(json.RawMessage(nil), request.Params...)
				_, _ = io.WriteString(serverWrite, `{"id":2,"result":{"account":{"type":"chatgpt","email":"synthetic@example.invalid","planType":"plus"},"requiresOpenaiAuth":true}}`+"\n")
			case "account/rateLimits/read":
				_, _ = io.WriteString(serverWrite, `{"id":3,"result":{"ordinaryUsageAllowed":null,"rateLimits":{"limitId":"codex","limitName":"Codex","normalModelSlug":null,"primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1900000000},"secondary":{"usedPercent":34,"windowDurationMins":10080,"resetsAt":1900001000},"credits":{"hasCredits":true,"unlimited":false,"balance":"synthetic-credit"},"individualLimit":null,"spendControlReached":false,"planType":"plus","rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":{"availableCount":2,"credits":null},"accountId":"synthetic-account","rateLimitUpsell":null}}`+"\n")
				methods <- seen
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := Query(ctx, client, "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if result.Account.Account == nil || result.Account.Account.Type != "chatgpt" {
		t.Fatalf("unexpected account: %#v", result.Account.Account)
	}
	if result.Snapshot.AccountID == nil || *result.Snapshot.AccountID != "synthetic-account" {
		t.Fatalf("unexpected account id: %#v", result.Snapshot.AccountID)
	}
	var params struct {
		RefreshToken bool `json:"refreshToken"`
	}
	if err := json.Unmarshal(<-accountParams, &params); err != nil {
		t.Fatal(err)
	}
	if params.RefreshToken {
		t.Fatal("account/read must use refreshToken=false so quota reads do not trigger token refresh")
	}
	seen := <-methods
	want := []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}
	if len(seen) != len(want) {
		t.Fatalf("unexpected methods: %#v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("method %d: got %q want %q", i, seen[i], want[i])
		}
	}
}

func TestAccountPayloadAcceptsChatGPTWithRequiresOpenAIAuthTrue(t *testing.T) {
	raw := json.RawMessage(`{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}`)
	if err := validateAccountPayload(raw); err != nil {
		t.Fatalf("logged-in ChatGPT payload with requiresOpenaiAuth=true must be valid: %v", err)
	}
}

func TestRateLimitPayloadRejectsMissingRequiredFieldsInsteadOfInventingQuota(t *testing.T) {
	base := `{"ordinaryUsageAllowed":null,"rateLimits":{"limitId":"codex","limitName":"Codex","normalModelSlug":null,"primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1900000000},"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":{"availableCount":2,"credits":null},"accountId":null,"rateLimitUpsell":null}`
	if err := validateRateLimitsPayload(json.RawMessage(base)); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	if err := validateRateLimitsPayload(json.RawMessage(`{"ordinaryUsageAllowed":"yes","rateLimits":{"limitId":"codex","limitName":"Codex","normalModelSlug":null,"primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":null,"accountId":null,"rateLimitUpsell":null}`)); err == nil {
		t.Fatal("ordinaryUsageAllowed must reject non-boolean values")
	}
	if err := validateRateLimitsPayload(json.RawMessage(`{"ordinaryUsageAllowed":null,"rateLimits":{"limitId":"codex","limitName":"Codex","normalModelSlug":12,"primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":null,"accountId":null,"rateLimitUpsell":null}`)); err == nil {
		t.Fatal("normalModelSlug must reject non-string values")
	}

	missingUsed := `{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":{"windowDurationMins":300,"resetsAt":1900000000},"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":null,"accountId":null,"rateLimitUpsell":null}`
	if err := validateRateLimitsPayload(json.RawMessage(missingUsed)); err == nil {
		t.Fatal("missing usedPercent must be unavailable instead of derived as 100% remaining")
	}

	missingCount := `{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":{"credits":null},"accountId":null,"rateLimitUpsell":null}`
	if err := validateRateLimitsPayload(json.RawMessage(missingCount)); err == nil {
		t.Fatal("missing reset availableCount must be rejected")
	}

	missingTop := `{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":null,"accountId":null}`
	if err := validateRateLimitsPayload(json.RawMessage(missingTop)); err == nil {
		t.Fatal("missing required top-level field must be rejected")
	}

	nullIndividualScalar := `{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":null,"secondary":null,"credits":null,"individualLimit":{"limit":null,"used":"1","remainingPercent":50,"resetsAt":1900000000},"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":null,"accountId":null,"rateLimitUpsell":null}`
	if err := validateRateLimitsPayload(json.RawMessage(nullIndividualScalar)); err == nil {
		t.Fatal("null individualLimit scalar must not decode into a synthetic zero value")
	}

	badResetCredit := `{"rateLimits":{"limitId":"codex","limitName":"Codex","primary":null,"secondary":null,"credits":null,"individualLimit":null,"spendControlReached":null,"planType":null,"rateLimitReachedType":null},"rateLimitsByLimitId":null,"rateLimitResetCredits":{"availableCount":1,"credits":[{"id":"c1","resetType":"weekly","status":"available","grantedAt":null,"expiresAt":null,"title":null,"description":null}]},"accountId":null,"rateLimitUpsell":null}`
	if err := validateRateLimitsPayload(json.RawMessage(badResetCredit)); err == nil {
		t.Fatal("null required reset-credit scalar must be rejected")
	}
}
