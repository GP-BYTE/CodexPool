package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"codexpool/internal/apprpc"
)

var (
	ErrNotAuthenticated     = errors.New("Codex profile is not authenticated")
	ErrNotChatGPT           = errors.New("Codex profile is not authenticated with ChatGPT")
	ErrIdentityChanged      = errors.New("Codex profile identity changed outside CodexPool lifecycle")
	ErrProtocolIncompatible = errors.New("Codex app-server protocol is incompatible with this CodexPool build")
)

type QueryResult struct {
	Account  GetAccountResponse `json:"account"`
	Snapshot Snapshot           `json:"snapshot"`
}

func Query(ctx context.Context, client *apprpc.Client, clientVersion string) (QueryResult, error) {
	if err := client.Initialize(ctx, "codexpool", "CodexPool", clientVersion); err != nil {
		return QueryResult{}, err
	}
	account, err := ReadAccountInitialized(ctx, client)
	if err != nil {
		return QueryResult{}, err
	}
	limits, err := ReadRateLimitsInitialized(ctx, client)
	if err != nil {
		return QueryResult{}, err
	}
	return QueryResult{Account: account, Snapshot: Normalize(limits, time.Now())}, nil
}

func ReadAccount(ctx context.Context, client *apprpc.Client, clientVersion string) (GetAccountResponse, error) {
	if err := client.Initialize(ctx, "codexpool", "CodexPool", clientVersion); err != nil {
		return GetAccountResponse{}, err
	}
	return ReadAccountInitialized(ctx, client)
}

func ReadAccountInitialized(ctx context.Context, client *apprpc.Client) (GetAccountResponse, error) {
	var raw json.RawMessage
	if err := client.Call(ctx, "account/read", map[string]any{"refreshToken": false}, &raw); err != nil {
		return GetAccountResponse{}, fmt.Errorf("read account: %w", err)
	}
	if err := validateAccountPayload(raw); err != nil {
		return GetAccountResponse{}, fmt.Errorf("%w: invalid account/read response: %v", ErrProtocolIncompatible, err)
	}
	var account GetAccountResponse
	if err := json.Unmarshal(raw, &account); err != nil {
		return GetAccountResponse{}, fmt.Errorf("decode account/read response: %w", err)
	}
	if account.Account == nil {
		return GetAccountResponse{}, ErrNotAuthenticated
	}
	if account.Account.Type != "chatgpt" {
		return GetAccountResponse{}, fmt.Errorf("%w: got %q", ErrNotChatGPT, account.Account.Type)
	}
	return account, nil
}

func ReadRateLimitsInitialized(ctx context.Context, client *apprpc.Client) (GetAccountRateLimitsResponse, error) {
	var raw json.RawMessage
	if err := client.Call(ctx, "account/rateLimits/read", nil, &raw); err != nil {
		return GetAccountRateLimitsResponse{}, fmt.Errorf("read rate limits: %w", err)
	}
	if err := validateRateLimitsPayload(raw); err != nil {
		return GetAccountRateLimitsResponse{}, fmt.Errorf("%w: invalid account/rateLimits/read response: %v", ErrProtocolIncompatible, err)
	}
	var limits GetAccountRateLimitsResponse
	if err := json.Unmarshal(raw, &limits); err != nil {
		return GetAccountRateLimitsResponse{}, fmt.Errorf("decode account/rateLimits/read response: %w", err)
	}
	return limits, nil
}

func validateAccountPayload(raw json.RawMessage) error {
	object, err := requiredObject(raw, "account/read", "account", "requiresOpenaiAuth")
	if err != nil {
		return err
	}
	var requires bool
	if rawIsNull(object["requiresOpenaiAuth"]) || json.Unmarshal(object["requiresOpenaiAuth"], &requires) != nil {
		return errors.New("requiresOpenaiAuth must be a boolean")
	}
	accountRaw := object["account"]
	if rawIsNull(accountRaw) {
		return nil
	}
	accountObject, err := requiredObject(accountRaw, "account", "type")
	if err != nil {
		return err
	}
	var accountType string
	if rawIsNull(accountObject["type"]) || json.Unmarshal(accountObject["type"], &accountType) != nil || accountType == "" {
		return errors.New("account.type must be a non-empty string")
	}
	if accountType == "chatgpt" {
		if _, err := requireKeys(accountObject, "chatgpt account", "email", "planType"); err != nil {
			return err
		}
		var plan string
		if rawIsNull(accountObject["planType"]) || json.Unmarshal(accountObject["planType"], &plan) != nil || plan == "" {
			return errors.New("chatgpt account.planType must be a non-empty string")
		}
	}
	return nil
}

func validateRateLimitsPayload(raw json.RawMessage) error {
	object, err := requiredObject(raw, "rate limit response", "ordinaryUsageAllowed", "rateLimits", "rateLimitsByLimitId", "rateLimitResetCredits", "accountId", "rateLimitUpsell")
	if err != nil {
		return err
	}
	if rawIsNull(object["rateLimits"]) {
		return errors.New("rateLimits must not be null")
	}
	if !rawIsNull(object["ordinaryUsageAllowed"]) {
		var allowed bool
		if err := json.Unmarshal(object["ordinaryUsageAllowed"], &allowed); err != nil {
			return fmt.Errorf("ordinaryUsageAllowed must be a boolean or null: %w", err)
		}
	}
	if err := validateRateLimitSnapshot(object["rateLimits"], "rateLimits"); err != nil {
		return err
	}
	if !rawIsNull(object["rateLimitsByLimitId"]) {
		var buckets map[string]json.RawMessage
		if err := json.Unmarshal(object["rateLimitsByLimitId"], &buckets); err != nil {
			return fmt.Errorf("rateLimitsByLimitId must be an object or null: %w", err)
		}
		for key, bucket := range buckets {
			if rawIsNull(bucket) {
				return fmt.Errorf("rateLimitsByLimitId[%q] must not be null", key)
			}
			if err := validateRateLimitSnapshot(bucket, "rateLimitsByLimitId["+key+"]"); err != nil {
				return err
			}
		}
	}
	if !rawIsNull(object["rateLimitResetCredits"]) {
		reset, err := requiredObject(object["rateLimitResetCredits"], "rateLimitResetCredits", "availableCount", "credits")
		if err != nil {
			return err
		}
		var count int64
		if rawIsNull(reset["availableCount"]) || json.Unmarshal(reset["availableCount"], &count) != nil || count < 0 {
			return errors.New("rateLimitResetCredits.availableCount must be a non-negative integer")
		}
		if !rawIsNull(reset["credits"]) {
			var credits []json.RawMessage
			if err := json.Unmarshal(reset["credits"], &credits); err != nil {
				return fmt.Errorf("rateLimitResetCredits.credits must be an array or null: %w", err)
			}
			for i, credit := range credits {
				if err := validateResetCredit(credit, fmt.Sprintf("rateLimitResetCredits.credits[%d]", i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateRateLimitSnapshot(raw json.RawMessage, name string) error {
	object, err := requiredObject(raw, name, "limitId", "limitName", "normalModelSlug", "primary", "secondary", "credits", "individualLimit", "spendControlReached", "planType", "rateLimitReachedType")
	if err != nil {
		return err
	}
	if !rawIsNull(object["normalModelSlug"]) {
		var slug string
		if err := json.Unmarshal(object["normalModelSlug"], &slug); err != nil {
			return fmt.Errorf("%s.normalModelSlug must be a string or null", name)
		}
	}
	for _, field := range []string{"primary", "secondary"} {
		if rawIsNull(object[field]) {
			continue
		}
		window, err := requiredObject(object[field], name+"."+field, "usedPercent", "windowDurationMins", "resetsAt")
		if err != nil {
			return err
		}
		var used float64
		if rawIsNull(window["usedPercent"]) || json.Unmarshal(window["usedPercent"], &used) != nil {
			return fmt.Errorf("%s.%s.usedPercent must be a number", name, field)
		}
	}
	if !rawIsNull(object["credits"]) {
		credits, err := requiredObject(object["credits"], name+".credits", "hasCredits", "unlimited", "balance")
		if err != nil {
			return err
		}
		for _, field := range []string{"hasCredits", "unlimited"} {
			var value bool
			if rawIsNull(credits[field]) || json.Unmarshal(credits[field], &value) != nil {
				return fmt.Errorf("%s.credits.%s must be a boolean", name, field)
			}
		}
	}
	if !rawIsNull(object["individualLimit"]) {
		individual, err := requiredObject(object["individualLimit"], name+".individualLimit", "limit", "used", "remainingPercent", "resetsAt")
		if err != nil {
			return err
		}
		for _, field := range []string{"limit", "used"} {
			var value string
			if rawIsNull(individual[field]) || json.Unmarshal(individual[field], &value) != nil {
				return fmt.Errorf("%s.individualLimit.%s must be a string", name, field)
			}
		}
		var remaining float64
		if rawIsNull(individual["remainingPercent"]) || json.Unmarshal(individual["remainingPercent"], &remaining) != nil {
			return fmt.Errorf("%s.individualLimit.remainingPercent must be a number", name)
		}
		var resetsAt int64
		if rawIsNull(individual["resetsAt"]) || json.Unmarshal(individual["resetsAt"], &resetsAt) != nil {
			return fmt.Errorf("%s.individualLimit.resetsAt must be an integer", name)
		}
	}
	if !rawIsNull(object["spendControlReached"]) {
		var value bool
		if json.Unmarshal(object["spendControlReached"], &value) != nil {
			return fmt.Errorf("%s.spendControlReached must be a boolean or null", name)
		}
	}
	return nil
}

func validateResetCredit(raw json.RawMessage, name string) error {
	object, err := requiredObject(raw, name, "id", "resetType", "status", "grantedAt", "expiresAt", "title", "description")
	if err != nil {
		return err
	}
	for _, field := range []string{"id", "resetType", "status"} {
		var value string
		if rawIsNull(object[field]) || json.Unmarshal(object[field], &value) != nil {
			return fmt.Errorf("%s.%s must be a string", name, field)
		}
	}
	var grantedAt int64
	if rawIsNull(object["grantedAt"]) || json.Unmarshal(object["grantedAt"], &grantedAt) != nil {
		return fmt.Errorf("%s.grantedAt must be an integer", name)
	}
	if !rawIsNull(object["expiresAt"]) {
		var expiresAt int64
		if json.Unmarshal(object["expiresAt"], &expiresAt) != nil {
			return fmt.Errorf("%s.expiresAt must be an integer or null", name)
		}
	}
	for _, field := range []string{"title", "description"} {
		if rawIsNull(object[field]) {
			continue
		}
		var value string
		if json.Unmarshal(object[field], &value) != nil {
			return fmt.Errorf("%s.%s must be a string or null", name, field)
		}
	}
	return nil
}

func requiredObject(raw json.RawMessage, name string, keys ...string) (map[string]json.RawMessage, error) {
	if rawIsNull(raw) {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		if err == nil {
			err = errors.New("not an object")
		}
		return nil, fmt.Errorf("%s must be an object: %w", name, err)
	}
	if _, err := requireKeys(object, name, keys...); err != nil {
		return nil, err
	}
	return object, nil
}

func requireKeys(object map[string]json.RawMessage, name string, keys ...string) (map[string]json.RawMessage, error) {
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return nil, fmt.Errorf("%s is missing required field %q", name, key)
		}
	}
	return object, nil
}

func rawIsNull(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return text == "" || text == "null"
}
