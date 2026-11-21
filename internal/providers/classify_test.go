package providers

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestClassifyTable(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	httpDate := now.Add(20 * time.Second).UTC().Format(http.TimeFormat)

	tests := []struct {
		name      string
		cls       Classifier // nil = generic
		status    int
		h         http.Header
		body      string
		wantClass ErrorClass
		wantCode  string
		wantRA    time.Duration
	}{
		// generic rules
		{"quota code beats 429", nil, 429, nil, `{"error":{"message":"x","type":"insufficient_quota","code":"insufficient_quota"}}`, ErrorClassInsufficientCredit, "insufficient_quota", 0},
		{"credit code on 400 via type", nil, 400, nil, `{"error":{"message":"x","type":"billing_hard_limit_reached"}}`, ErrorClassInsufficientCredit, "billing_hard_limit_reached", 0},
		{"402", nil, 402, nil, `{"error":{"message":"pay"}}`, ErrorClassInsufficientCredit, "", 0},
		{"429 no hints", nil, 429, nil, `{"error":{"message":"slow","code":"rate_limit_exceeded"}}`, ErrorClassRateLimit, "rate_limit_exceeded", 30 * time.Second},
		{"429 retry-after secs", nil, 429, hdr("Retry-After", "7"), ``, ErrorClassRateLimit, "", 7 * time.Second},
		{"429 retry-after date", nil, 429, hdr("Retry-After", httpDate), ``, ErrorClassRateLimit, "", 20 * time.Second},
		{"429 retry-after huge clamps", nil, 429, hdr("Retry-After", "99999"), ``, ErrorClassRateLimit, "", time.Hour},
		{"429 retry-after 0 clamps to 1s", nil, 429, hdr("Retry-After", "0"), ``, ErrorClassRateLimit, "", time.Second},
		{"401", nil, 401, nil, `{"error":{"message":"bad key","code":"invalid_api_key"}}`, ErrorClassAuth, "invalid_api_key", 0},
		{"403", nil, 403, nil, ``, ErrorClassAuth, "", 0},
		{"404", nil, 404, nil, `{"error":{"message":"no model"}}`, ErrorClassModelUnavailable, "", 0},
		{"400 model_not_found", nil, 400, nil, `{"error":{"message":"no","code":"model_not_found"}}`, ErrorClassModelUnavailable, "model_not_found", 0},
		{"408", nil, 408, nil, ``, ErrorClassTransient, "", 0},
		{"409", nil, 409, nil, ``, ErrorClassTransient, "", 0},
		{"425", nil, 425, nil, ``, ErrorClassTransient, "", 0},
		{"500", nil, 500, nil, `{"error":{"message":"boom"}}`, ErrorClassTransient, "", 0},
		{"503 html body", nil, 503, nil, `<html>unavailable</html>`, ErrorClassTransient, "", 0},
		{"400", nil, 400, nil, `{"error":{"message":"bad param","type":"invalid_request_error"}}`, ErrorClassBadRequest, "invalid_request_error", 0},
		{"413", nil, 413, nil, ``, ErrorClassBadRequest, "", 0},
		{"422", nil, 422, nil, ``, ErrorClassBadRequest, "", 0},
		{"unknown 418", nil, 418, nil, ``, ErrorClassTransient, "", 0},
		{"array wrapped body", nil, 400, nil, `[{"error":{"message":"m","code":"insufficient_quota"}}]`, ErrorClassInsufficientCredit, "insufficient_quota", 0},
		{"string error", nil, 400, nil, `{"error":"just text"}`, ErrorClassBadRequest, "", 0},

		// Groq reset headers
		{"groq reset requests exhausted", &GroqProvider{}, 429, hdr("X-Ratelimit-Remaining-Requests", "0", "X-Ratelimit-Reset-Requests", "2m3.5s", "X-Ratelimit-Remaining-Tokens", "500", "X-Ratelimit-Reset-Tokens", "1s"), `{"error":{"code":"rate_limit_exceeded"}}`, ErrorClassRateLimit, "rate_limit_exceeded", 124 * time.Second},
		{"groq reset tokens exhausted", &GroqProvider{}, 429, hdr("X-Ratelimit-Remaining-Requests", "5", "X-Ratelimit-Reset-Requests", "2m3.5s", "X-Ratelimit-Remaining-Tokens", "0", "X-Ratelimit-Reset-Tokens", "7.66s"), ``, ErrorClassRateLimit, "", 8 * time.Second},
		{"groq reset neither exhausted -> max", &GroqProvider{}, 429, hdr("X-Ratelimit-Reset-Requests", "3s", "X-Ratelimit-Reset-Tokens", "9s"), ``, ErrorClassRateLimit, "", 9 * time.Second},
		{"groq retry-after wins over reset", &GroqProvider{}, 429, hdr("Retry-After", "5", "X-Ratelimit-Reset-Requests", "50s"), ``, ErrorClassRateLimit, "", 5 * time.Second},
		{"groq 498", &GroqProvider{}, 498, nil, `{"error":{"message":"flex capacity"}}`, ErrorClassTransient, "", 0},
		{"groq 413", &GroqProvider{}, 413, nil, `{"error":{"message":"too big","code":"request_too_large"}}`, ErrorClassBadRequest, "request_too_large", 0},
		{"groq org restricted", &GroqProvider{}, 400, nil, `{"error":{"message":"restricted","code":"organization_restricted"}}`, ErrorClassAuth, "organization_restricted", 0},
		{"groq 400 plain", &GroqProvider{}, 400, nil, `{"error":{"message":"x","type":"invalid_request_error"}}`, ErrorClassBadRequest, "invalid_request_error", 0},

		// OpenRouter
		{"or 402", &OpenRouterProvider{}, 402, nil, `{"error":{"message":"Insufficient credits","code":402}}`, ErrorClassInsufficientCredit, "402", 0},
		{"or 403 moderation", &OpenRouterProvider{}, 403, nil, `{"error":{"message":"flagged","code":403,"metadata":{"reasons":["x"]}}}`, ErrorClassBadRequest, "403", 0},
		{"or 502", &OpenRouterProvider{}, 502, nil, ``, ErrorClassTransient, "", 0},
		{"or 503", &OpenRouterProvider{}, 503, nil, ``, ErrorClassTransient, "", 0},
		{"or 401", &OpenRouterProvider{}, 401, nil, `{"error":{"message":"No auth","code":401}}`, ErrorClassAuth, "401", 0},
		{"or 200 w/ error 429", &OpenRouterProvider{}, 200, hdr("X-RateLimit-Reset", "1767268815000"), `{"error":{"message":"rate","code":429}}`, ErrorClassRateLimit, "429", 15 * time.Second},
		{"or 200 w/ error 402", &OpenRouterProvider{}, 200, nil, `{"error":{"message":"credits","code":402}}`, ErrorClassInsufficientCredit, "402", 0},
		{"or 200 w/ error 502", &OpenRouterProvider{}, 200, nil, `{"error":{"message":"up","code":502}}`, ErrorClassTransient, "502", 0},
		{"or 429 reset ms", &OpenRouterProvider{}, 429, hdr("X-RateLimit-Reset", "1767268830000"), `{"error":{"code":429}}`, ErrorClassRateLimit, "429", 30 * time.Second},
	}
	// 1767268800000 ms = 2026-01-01T12:00:00Z (now)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pe *ProviderError
			if tt.cls == nil {
				pe = classifyAt(tt.status, tt.h, []byte(tt.body), now)
			} else {
				pe = classifyVia(tt.cls, tt.status, tt.h, []byte(tt.body), now)
			}
			if pe.Class != tt.wantClass {
				t.Errorf("class = %s, want %s", pe.Class, tt.wantClass)
			}
			if pe.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", pe.Code, tt.wantCode)
			}
			if pe.RetryAfter != tt.wantRA {
				t.Errorf("retryAfter = %v, want %v", pe.RetryAfter, tt.wantRA)
			}
		})
	}
}

// classifyVia runs an adapter Classifier with the package clock pinned to now.
func classifyVia(c Classifier, status int, h http.Header, body []byte, now time.Time) *ProviderError {
	old := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = old }()
	return c.Classify(status, h, body)
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if d, ok := ParseRetryAfter("7", now); !ok || d != 7*time.Second {
		t.Fatalf("secs: %v %v", d, ok)
	}
	d, ok := ParseRetryAfter(now.Add(20*time.Second).Format(http.TimeFormat), now)
	if !ok || d < 19*time.Second || d > 21*time.Second {
		t.Fatalf("date: %v %v", d, ok)
	}
	for _, bad := range []string{"", "soon", "-5"} {
		if _, ok := ParseRetryAfter(bad, now); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestDefaultCooldownEnv(t *testing.T) {
	t.Setenv("ROUTER0_DEFAULT_RATE_LIMIT_COOLDOWN_SECONDS", "45")
	pe := Classify(429, nil, nil)
	if pe.RetryAfter != 45*time.Second {
		t.Fatalf("got %v", pe.RetryAfter)
	}
}

func TestMessageAndCodeBounded(t *testing.T) {
	long := strings.Repeat("a", 5000)
	pe := Classify(400, nil, []byte(`{"error":{"message":"`+long+`","code":"`+long+`"}}`))
	if len(pe.Code) > 200 || len(pe.Message) > 1000 {
		t.Fatalf("not truncated: %d %d", len(pe.Code), len(pe.Message))
	}
}

func TestNonJSONBodyNotLeaked(t *testing.T) {
	pe := Classify(400, nil, []byte("SENTINEL-BODY raw text"))
	if strings.Contains(pe.Message, "SENTINEL-BODY") || strings.Contains(pe.Error(), "SENTINEL-BODY") {
		t.Fatalf("raw body leaked: %q / %q", pe.Message, pe.Error())
	}
}

func TestErrorStringOmitsMessage(t *testing.T) {
	pe := &ProviderError{Class: ErrorClassBadRequest, HTTP: 400, Code: "c", Message: "secret prompt text"}
	if strings.Contains(pe.Error(), "secret") {
		t.Fatal("Error() must not include Message")
	}
}
