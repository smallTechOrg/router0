package providers

import (
	"context"
	"encoding/json"
	"strings"
)

// OpenRouterBalance is a key's balance as reported by OpenRouter.
// Remaining is nil when OpenRouter reports no limit and the account credit
// endpoint gave no answer (unknown / unlimited).
type OpenRouterBalance struct {
	LimitUSD     *float64 // per-key limit (GET /key data.limit), nil = no key limit
	UsageUSD     float64  // per-key usage (GET /key data.usage)
	RemainingUSD *float64
}

// FetchOpenRouterBalance reads GET {OPENROUTER_BASE_URL}/key and, when the key
// has no limit of its own, GET /credits. Remaining is limit_remaining when the
// key has a limit, else total_credits − total_usage (credit-tracking.md).
// A /key failure is returned as an error (*ProviderError where classified); a
// /credits failure only leaves Remaining nil.
func FetchOpenRouterBalance(ctx context.Context, apiKey string) (*OpenRouterBalance, error) {
	p := &OpenRouterProvider{}
	base := strings.TrimRight(p.baseURL(), "/")
	hdr := bearer(apiKey, p.headers())

	b, err := doGetJSON(ctx, base+"/key", hdr, p)
	if err != nil {
		return nil, err
	}
	var key struct {
		Data struct {
			Limit          *float64 `json:"limit"`
			Usage          float64  `json:"usage"`
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &key); err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, Code: "invalid_response", Message: "unparseable /key response"}
	}
	out := &OpenRouterBalance{LimitUSD: key.Data.Limit, UsageUSD: key.Data.Usage}
	if key.Data.Limit != nil {
		rem := *key.Data.Limit - key.Data.Usage
		if key.Data.LimitRemaining != nil {
			rem = *key.Data.LimitRemaining
		}
		out.RemainingUSD = &rem
		return out, nil
	}

	cb, cerr := doGetJSON(ctx, base+"/credits", hdr, p)
	if cerr != nil {
		return out, nil
	}
	var cr struct {
		Data struct {
			TotalCredits *float64 `json:"total_credits"`
			TotalUsage   float64  `json:"total_usage"`
		} `json:"data"`
	}
	if json.Unmarshal(cb, &cr) == nil && cr.Data.TotalCredits != nil {
		rem := *cr.Data.TotalCredits - cr.Data.TotalUsage
		out.RemainingUSD = &rem
	}
	return out, nil
}
