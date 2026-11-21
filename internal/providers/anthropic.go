package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	anthropicVersion        = "2023-06-01"
	anthropicDefaultBaseURL = "https://api.anthropic.com/v1"
	anthropicMaxModelPages  = 50
)

// AnthropicProvider is the native Anthropic Messages API adapter (provider id "anthropic").
type AnthropicProvider struct{}

func init() { Register(&AnthropicProvider{}) }

func (a *AnthropicProvider) Name() string { return "anthropic" }

func (a *AnthropicProvider) DefaultModels() []string {
	return []string{"claude-sonnet-4-5", "claude-haiku-4-5"}
}

func (a *AnthropicProvider) baseURL() string {
	return strings.TrimRight(envOrDef("ANTHROPIC_BASE_URL", anthropicDefaultBaseURL), "/")
}

// anthropicStatusForType maps an Anthropic error type to the HTTP status it is
// served with; used to classify mid-stream error events that carry no status.
func anthropicStatusForType(t string) int {
	switch t {
	case "invalid_request_error":
		return 400
	case "authentication_error":
		return 401
	case "billing_error":
		return 402
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "request_too_large":
		return 413
	case "rate_limit_error":
		return 429
	case "overloaded_error":
		return 529
	default: // api_error and unknown
		return 500
	}
}

var anthropicResetHeaders = []struct{ reset, remaining string }{
	{"Anthropic-Ratelimit-Requests-Reset", "Anthropic-Ratelimit-Requests-Remaining"},
	{"Anthropic-Ratelimit-Tokens-Reset", "Anthropic-Ratelimit-Tokens-Remaining"},
	{"Anthropic-Ratelimit-Input-Tokens-Reset", "Anthropic-Ratelimit-Input-Tokens-Remaining"},
	{"Anthropic-Ratelimit-Output-Tokens-Reset", "Anthropic-Ratelimit-Output-Tokens-Remaining"},
}

// anthropicResetCooldown reads anthropic-ratelimit-*-reset (RFC 3339). It uses the
// exhausted dimensions (remaining == 0) when any, else all present ones, taking the max.
func anthropicResetCooldown(h http.Header, now time.Time) (time.Duration, bool) {
	var all, exhausted []time.Duration
	for _, p := range anthropicResetHeaders {
		v := strings.TrimSpace(h.Get(p.reset))
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			continue
		}
		d := t.Sub(now)
		all = append(all, d)
		if rem := strings.TrimSpace(h.Get(p.remaining)); rem != "" && atoi(rem) == 0 {
			exhausted = append(exhausted, d)
		}
	}
	pick := all
	if len(exhausted) > 0 {
		pick = exhausted
	}
	if len(pick) == 0 {
		return 0, false
	}
	m := pick[0]
	for _, d := range pick[1:] {
		m = maxDur(m, d)
	}
	return m, true
}

// Classify applies Anthropic-specific rules on top of the generic ones.
func (a *AnthropicProvider) Classify(status int, h http.Header, body []byte) *ProviderError {
	pe := Classify(status, h, body)
	eb := parseErrBody(body)
	typ := eb.Type
	switch {
	case (status == 400 || typ == "invalid_request_error" || typ == "billing_error") &&
		strings.Contains(strings.ToLower(eb.Message), "credit balance is too low"):
		pe.Class = ErrorClassInsufficientCredit
	case typ == "billing_error":
		pe.Class = ErrorClassInsufficientCredit
	case status == 401 || status == 403 || typ == "authentication_error" || typ == "permission_error":
		pe.Class = ErrorClassAuth
	case status == 404 || typ == "not_found_error":
		pe.Class = ErrorClassModelUnavailable
	case status == 413 || typ == "request_too_large":
		pe.Class = ErrorClassBadRequest
	case status == 429 || typ == "rate_limit_error":
		pe.Class = ErrorClassRateLimit
	case status == 529 || status == 500 || typ == "overloaded_error" || typ == "api_error":
		pe.Class = ErrorClassTransient
	case status == 400 && typ == "invalid_request_error":
		pe.Class = ErrorClassBadRequest
	}
	if pe.Class == ErrorClassRateLimit && pe.RetryAfter == 0 {
		pe.RetryAfter = DefaultRateLimitCooldown()
	}
	if pe.Class == ErrorClassRateLimit && h != nil && status != http.StatusOK {
		now := nowFunc()
		if _, ok := ParseRetryAfter(h.Get("Retry-After"), now); !ok {
			if d, ok := anthropicResetCooldown(h, now); ok {
				pe.RetryAfter = ClampCooldown(d)
			}
		}
	}
	return pe
}

func (a *AnthropicProvider) newRequest(ctx context.Context, method, path string, apiKey string, body []byte, stream bool) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	r, err := http.NewRequestWithContext(ctx, method, a.baseURL()+path, rdr)
	if err != nil {
		return nil, err
	}
	r.Header.Set("x-api-key", apiKey)
	r.Header.Set("anthropic-version", anthropicVersion)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	}
	return r, nil
}

func (a *AnthropicProvider) open(ctx context.Context, req *ChatRequest, apiKey string, stream bool) (*http.Response, error) {
	body, err := buildAnthropicBody(req, stream)
	if err != nil {
		return nil, err
	}
	httpReq, err := a.newRequest(ctx, http.MethodPost, "/messages", apiKey, body, stream)
	if err != nil {
		return nil, err
	}
	resp, err := doRequest(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, failureFromResponse(resp, a)
	}
	return resp, nil
}

func (a *AnthropicProvider) Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error) {
	resp, err := a.open(ctx, req, apiKey, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		if pe, cancelled := TransportError(ctx, err); !cancelled {
			return nil, pe
		}
		return nil, context.Canceled
	}
	out, err := parseAnthropicResponse(raw)
	if err != nil {
		return nil, &ProviderError{Class: ErrorClassTransient, HTTP: resp.StatusCode, Code: "invalid_response", Message: "provider returned an unparseable response"}
	}
	out.RateLimit = parseAnthropicRateLimit(resp.Header)
	return out, nil
}

func parseAnthropicRateLimit(h http.Header) *RateLimitHeaders {
	rl := &RateLimitHeaders{
		LimitRPM:     atoi(h.Get("Anthropic-Ratelimit-Requests-Limit")),
		RemainingRPM: atoi(h.Get("Anthropic-Ratelimit-Requests-Remaining")),
		LimitTPM:     atoi(h.Get("Anthropic-Ratelimit-Tokens-Limit")),
		RemainingTPM: atoi(h.Get("Anthropic-Ratelimit-Tokens-Remaining")),
		ResetAt:      h.Get("Anthropic-Ratelimit-Requests-Reset"),
	}
	if rl.ResetAt == "" {
		rl.ResetAt = h.Get("Anthropic-Ratelimit-Tokens-Reset")
	}
	return rl
}

func (a *AnthropicProvider) Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error) {
	resp, err := a.open(ctx, req, apiKey, true)
	if err != nil {
		return nil, err
	}
	ch := make(chan ChatChunk, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		streamAnthropic(ctx, resp.Body, a, resp.Header, ch)
	}()
	return ch, nil
}

func (a *AnthropicProvider) HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error) {
	return a.ProbeModel(ctx, apiKey, a.DefaultModels()[len(a.DefaultModels())-1])
}

// ProbeModel sends a 1-token completion to the given model.
func (a *AnthropicProvider) ProbeModel(ctx context.Context, apiKey, model string) (*HealthResult, error) {
	start := time.Now()
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := a.Complete(hctx, &ChatRequest{
		Model:     model,
		Messages:  []ChatMessage{{Role: "user", Content: "1"}},
		MaxTokens: 1,
	}, apiKey)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if pe, ok := err.(*ProviderError); ok {
			return &HealthResult{OK: false, LatencyMs: latency, Error: string(pe.Class)}, nil
		}
		return &HealthResult{OK: false, LatencyMs: latency, Error: "request_failed"}, nil
	}
	return &HealthResult{OK: true, LatencyMs: latency, RateLimit: resp.RateLimit}, nil
}

func (a *AnthropicProvider) ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error) {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out []ModelInfo
	afterID := ""
	for page := 0; page < anthropicMaxModelPages; page++ {
		q := url.Values{}
		q.Set("limit", "1000")
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		httpReq, err := a.newRequest(lctx, http.MethodGet, "/models?"+q.Encode(), apiKey, nil, false)
		if err != nil {
			return nil, err
		}
		resp, err := doRequest(lctx, httpReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, failureFromResponse(resp, a)
		}
		var body struct {
			Data []struct {
				ID             string `json:"id"`
				DisplayName    string `json:"display_name"`
				MaxInputTokens int    `json:"max_input_tokens"`
				MaxTokens      int    `json:"max_tokens"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			if pe, cancelled := TransportError(ctx, err); cancelled {
				return nil, context.Canceled
			} else if ctx.Err() != nil {
				return nil, pe
			}
			return nil, &ProviderError{Class: ErrorClassTransient, HTTP: resp.StatusCode, Code: "invalid_response", Message: "provider returned an unparseable response"}
		}
		for _, m := range body.Data {
			if m.ID == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = m.ID
			}
			out = append(out, ModelInfo{
				ID:                m.ID,
				DisplayName:       name,
				ContextWindow:     m.MaxInputTokens,
				MaxOutputTokens:   m.MaxTokens,
				SupportsStreaming: true,
			})
		}
		if !body.HasMore || body.LastID == "" || body.LastID == afterID {
			break
		}
		afterID = body.LastID
	}
	return out, nil
}
