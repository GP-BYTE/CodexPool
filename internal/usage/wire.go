package usage

type Account struct {
	Type                        string  `json:"type"`
	Email                       *string `json:"email,omitempty"`
	PlanType                    string  `json:"planType,omitempty"`
	UsesCodexManagedCredentials *bool   `json:"usesCodexManagedCredentials,omitempty"`
}

type GetAccountResponse struct {
	Account            *Account `json:"account"`
	RequiresOpenaiAuth bool     `json:"requiresOpenaiAuth"`
}

type CreditsSnapshot struct {
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type RateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type SpendControlLimitSnapshot struct {
	Limit            string  `json:"limit"`
	Used             string  `json:"used"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetsAt         int64   `json:"resetsAt"`
}

type RateLimitSnapshot struct {
	LimitID              *string                    `json:"limitId"`
	LimitName            *string                    `json:"limitName"`
	NormalModelSlug      *string                    `json:"normalModelSlug"`
	Primary              *RateLimitWindow           `json:"primary"`
	Secondary            *RateLimitWindow           `json:"secondary"`
	Credits              *CreditsSnapshot           `json:"credits"`
	IndividualLimit      *SpendControlLimitSnapshot `json:"individualLimit"`
	SpendControlReached  *bool                      `json:"spendControlReached"`
	PlanType             *string                    `json:"planType"`
	RateLimitReachedType *string                    `json:"rateLimitReachedType"`
}

type RateLimitResetCredit struct {
	ID          string  `json:"id"`
	ResetType   string  `json:"resetType"`
	Status      string  `json:"status"`
	GrantedAt   int64   `json:"grantedAt"`
	ExpiresAt   *int64  `json:"expiresAt"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

type RateLimitResetCreditsSummary struct {
	AvailableCount int64                   `json:"availableCount"`
	Credits        *[]RateLimitResetCredit `json:"credits"`
}

type GetAccountRateLimitsResponse struct {
	OrdinaryUsageAllowed  *bool                         `json:"ordinaryUsageAllowed"`
	RateLimits            RateLimitSnapshot             `json:"rateLimits"`
	RateLimitsByLimitID   map[string]RateLimitSnapshot  `json:"rateLimitsByLimitId"`
	RateLimitResetCredits *RateLimitResetCreditsSummary `json:"rateLimitResetCredits"`
	AccountID             *string                       `json:"accountId"`
	RateLimitUpsell       any                           `json:"rateLimitUpsell"`
}
