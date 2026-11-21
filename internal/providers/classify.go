package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxErrorBodyBytes bounds how much of a failed provider response is read.
	MaxErrorBodyBytes = 64 << 10
	maxCodeLen        = 200
	maxMessageLen     = 1000

	minCooldown = time.Second
	maxCooldown = time.Hour
)

var nowFunc = time.Now

// creditCodes are body error.code / error.type values meaning "out of money".
var creditCodes = map[string]bool{
	"insufficient_quota":         true,
	"insufficient_credits":       true,
	"billing_hard_limit_reached": true,
	"credit_limit_exceeded":      true,
	"payment_required":           true,
	"quota_exceeded":             true,
}

// errBody is the parsed structured error of a provider response body.
type errBody struct {
	Present bool
	Code    string // error.code (string or number rendered as string)
	CodeNum int    // numeric error.code, 0 if not numeric
	Type    string
	Message string
}

// parseErrBody extracts {"error":{message,type,code}} (also accepts a JSON array
// wrapper [{"error":...}] and {"error":"text"}).
func parseErrBody(body []byte) errBody {
	b := trimSpace(body)
	if len(b) == 0 {
		return errBody{}
	}
	if b[0] == '[' {
		var arr []json.RawMessage
		if json.Unmarshal(b, &arr) != nil || len(arr) == 0 {
			return errBody{}
		}
		b = arr[0]
	}
	var top struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &top) != nil || len(top.Error) == 0 || string(top.Error) == "null" {
		return errBody{}
	}
	out := errBody{Present: true}
	var s string
	if json.Unmarshal(top.Error, &s) == nil {
		out.Message = s
		return out
	}
	var obj struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
		Status  string          `json:"status"`
	}
	if json.Unmarshal(top.Error, &obj) != nil {
		return out
	}
	out.Message, out.Type = obj.Message, obj.Type
	if len(obj.Code) > 0 && string(obj.Code) != "null" {
		var cs string
		if json.Unmarshal(obj.Code, &cs) == nil {
			out.Code = cs
		} else {
			var cf float64
			if json.Unmarshal(obj.Code, &cf) == nil {
				out.CodeNum = int(cf)
				out.Code = strconv.Itoa(int(cf))
			}
		}
	}
	if out.Code == "" {
		out.Code = obj.Status
	}
	return out
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Classify applies the generic OpenAI-style rules (error-classification.md).
func Classify(status int, h http.Header, body []byte) *ProviderError {
	return classifyAt(status, h, body, nowFunc())
}

func classifyAt(status int, h http.Header, body []byte, now time.Time) *ProviderError {
	if len(body) > MaxErrorBodyBytes {
		body = body[:MaxErrorBodyBytes]
	}
	eb := parseErrBody(body)
	code := eb.Code
	if code == "" {
		code = eb.Type
	}
	pe := &ProviderError{
		HTTP: status,
		Code: truncate(code, maxCodeLen),
	}
	pe.Message = truncate(eb.Message, maxMessageLen)
	if pe.Message == "" {
		pe.Message = http.StatusText(status)
		if pe.Message == "" {
			pe.Message = "provider error"
		}
	}

	switch {
	case creditCodes[eb.Code] || creditCodes[eb.Type]:
		pe.Class = ErrorClassInsufficientCredit
	case status == http.StatusPaymentRequired:
		pe.Class = ErrorClassInsufficientCredit
	case status == http.StatusTooManyRequests:
		pe.Class = ErrorClassRateLimit
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		pe.Class = ErrorClassAuth
	case status == http.StatusNotFound, eb.Code == "model_not_found":
		pe.Class = ErrorClassModelUnavailable
	case status == 408, status == 409, status == 425, status >= 500 && status <= 599:
		pe.Class = ErrorClassTransient
	case status == 400, status == 413, status == 422:
		pe.Class = ErrorClassBadRequest
	default:
		pe.Class = ErrorClassTransient
	}
	if pe.Class == ErrorClassRateLimit {
		pe.RetryAfter = rateLimitCooldown(h, now)
	}
	return pe
}

// TransportError classifies a network-level failure. A cancelled caller context
// is not a provider failure and is returned (second value true) so the caller can
// propagate it unclassified. Deadline expiry and everything else is transient.
func TransportError(ctx context.Context, err error) (*ProviderError, bool) {
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return nil, true
	}
	return &ProviderError{Class: ErrorClassTransient, Code: "network_error", Message: "network error contacting provider"}, false
}

// ParseRetryAfter parses a Retry-After value: integer seconds or an HTTP-date.
func ParseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		if n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return time.Duration(math.Ceil(n)) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now), true
	}
	return 0, false
}

// ClampCooldown clamps d into [1s, 3600s].
func ClampCooldown(d time.Duration) time.Duration {
	if d < minCooldown {
		return minCooldown
	}
	if d > maxCooldown {
		return maxCooldown
	}
	return d
}

// DefaultRateLimitCooldown reads ROUTER0_DEFAULT_RATE_LIMIT_COOLDOWN_SECONDS (30).
func DefaultRateLimitCooldown() time.Duration {
	if v := os.Getenv("ROUTER0_DEFAULT_RATE_LIMIT_COOLDOWN_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return ClampCooldown(time.Duration(n) * time.Second)
		}
	}
	return 30 * time.Second
}

// rateLimitCooldown: Retry-After, then provider reset headers, then default; clamped.
func rateLimitCooldown(h http.Header, now time.Time) time.Duration {
	if h != nil {
		if d, ok := ParseRetryAfter(h.Get("Retry-After"), now); ok {
			return ClampCooldown(d)
		}
		if d, ok := resetHeaderCooldown(h, now); ok {
			return ClampCooldown(d)
		}
	}
	return DefaultRateLimitCooldown()
}

func resetHeaderCooldown(h http.Header, now time.Time) (time.Duration, bool) {
	// Groq: duration strings per dimension; use the exhausted one, else the max.
	reqReset, reqOK := parseDurationHeader(h.Get("X-Ratelimit-Reset-Requests"))
	tokReset, tokOK := parseDurationHeader(h.Get("X-Ratelimit-Reset-Tokens"))
	if reqOK || tokOK {
		reqExhausted := reqOK && h.Get("X-Ratelimit-Remaining-Requests") != "" && atoi(h.Get("X-Ratelimit-Remaining-Requests")) == 0
		tokExhausted := tokOK && h.Get("X-Ratelimit-Remaining-Tokens") != "" && atoi(h.Get("X-Ratelimit-Remaining-Tokens")) == 0
		switch {
		case reqExhausted && tokExhausted:
			return maxDur(reqReset, tokReset), true
		case reqExhausted:
			return reqReset, true
		case tokExhausted:
			return tokReset, true
		default:
			return maxDur(reqReset, tokReset), true
		}
	}
	// OpenRouter: X-RateLimit-Reset as unix epoch milliseconds (seconds tolerated).
	if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			var t time.Time
			if n > 1e11 {
				t = time.UnixMilli(n)
			} else {
				t = time.Unix(n, 0)
			}
			return t.Sub(now), true
		}
	}
	return 0, false
}

func parseDurationHeader(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(v); err == nil {
		return ceilSecond(d), true
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
		return ceilSecond(time.Duration(f * float64(time.Second))), true
	}
	return 0, false
}

func ceilSecond(d time.Duration) time.Duration {
	return time.Duration(math.Ceil(d.Seconds())) * time.Second
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// classifyWith runs the adapter's Classifier if given, else the generic rules.
func classifyWith(cls Classifier, status int, h http.Header, body []byte) *ProviderError {
	if cls != nil {
		return cls.Classify(status, h, body)
	}
	return Classify(status, h, body)
}

// classify200 detects an error payload inside a 2xx body (no "choices" and an
// "error" object) and classifies it; nil means the body is a normal response.
func classify200(cls Classifier, h http.Header, body []byte) *ProviderError {
	eb := parseErrBody(body)
	if !eb.Present {
		return nil
	}
	var probe struct {
		Choices json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(trimSpace(body), &probe) == nil && len(probe.Choices) > 0 {
		return nil
	}
	return classifyWith(cls, http.StatusOK, h, body)
}
