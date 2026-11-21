package routing

import (
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/zer0-router/zer0/internal/keystate"
	"github.com/zer0-router/zer0/internal/providers"
)

// Attempt outcomes.
const (
	OutcomeSuccess = "success"
	OutcomeError   = "error"
	OutcomeSkipped = "skipped"
)

// Codes used on attempt records besides the provider's own structured codes.
const (
	CodeClientCancelled  = "client_cancelled"
	CodeKeyDecryptFailed = "key_decrypt_failed"
	CodeAttemptTimeout   = "attempt_timeout"
	CodeAttemptError     = "attempt_error"
)

// AttemptRecord is one considered candidate (attempted or skipped). It never
// contains provider response bodies; Message is the provider's own error message
// kept in memory only (for a 400 upstream_rejected_request) and must not be
// persisted or logged.
type AttemptRecord struct {
	Candidate
	Order      int
	Outcome    string // success | error | skipped
	Class      string // providers.ErrorClass value; for skipped: derived from SkipReason
	HTTP       int
	RetryAfter time.Duration
	LatencyMs  int64

	SkipReason string // skipped only: keystate reason
	Code       string // provider's structured error code (<= 200 chars)
	Message    string // in-memory only
}

// Result is the outcome of Executor.Run.
type Result struct {
	Winner    *Candidate
	Attempts  []AttemptRecord
	Cancelled bool   // the caller's context ended; the loop stopped
	Model     string // optional requested model name, used only in messages
}

// GatewayError is the single error returned to the caller after exhaustion.
type GatewayError struct {
	HTTP              int
	Type              string
	Code              string
	Param             string
	Message           string
	RetryAfterSeconds int // > 0 only for 429
	Attempts          []AttemptRecord
}

// Error type / code strings (api.md).
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeRateLimit      = "rate_limit_error"
	TypeCredits        = "insufficient_credits"
	TypeUpstream       = "upstream_error"
)

// effectiveClass returns the class FinalError uses for a record, or "" when the
// record takes no part (success, or a cancelled attempt).
func effectiveClass(a AttemptRecord) providers.ErrorClass {
	switch a.Outcome {
	case OutcomeSkipped:
		switch a.SkipReason {
		case keystate.ReasonRateLimited:
			return providers.ErrorClassRateLimit
		case keystate.ReasonCreditsExhausted, keystate.ReasonBudgetExhausted:
			return providers.ErrorClassInsufficientCredit
		}
		return ""
	case OutcomeError:
		return providers.ErrorClass(a.Class)
	}
	return ""
}

// FinalError applies the precedence 404(no plan) > 429 > 402 > 404 > 400 > 503.
func FinalError(r Result) *GatewayError {
	model := r.Model
	if model == "" {
		model = "the requested model"
	} else {
		model = "'" + model + "'"
	}
	ge := &GatewayError{Attempts: r.Attempts}

	var considered []AttemptRecord
	for _, a := range r.Attempts {
		if effectiveClass(a) != "" {
			considered = append(considered, a)
		}
	}
	if len(considered) == 0 {
		if len(r.Attempts) == 0 {
			ge.HTTP, ge.Type, ge.Code, ge.Param = http.StatusNotFound, TypeInvalidRequest, "model_not_found", "model"
			ge.Message = fmt.Sprintf("The model %s is not available for this organization.", model)
			return ge
		}
		ge.HTTP, ge.Type, ge.Code = http.StatusServiceUnavailable, TypeUpstream, "all_providers_failed"
		ge.Message = fmt.Sprintf("All providers failed for %s.", model)
		return ge
	}

	var rl, credits, unavailable, bad int
	var minRetry time.Duration
	var firstBad *AttemptRecord
	for i, a := range considered {
		switch effectiveClass(a) {
		case providers.ErrorClassRateLimit:
			rl++
			if a.RetryAfter > 0 && (minRetry == 0 || a.RetryAfter < minRetry) {
				minRetry = a.RetryAfter
			}
		case providers.ErrorClassInsufficientCredit:
			credits++
		case providers.ErrorClassModelUnavailable:
			unavailable++
		case providers.ErrorClassBadRequest:
			bad++
			if firstBad == nil {
				firstBad = &considered[i]
			}
		}
	}
	n := len(considered)
	switch {
	case rl > 0:
		secs := int(math.Ceil(minRetry.Seconds()))
		if secs < 1 {
			secs = 1
		}
		ge.HTTP, ge.Type, ge.Code, ge.RetryAfterSeconds = http.StatusTooManyRequests, TypeRateLimit, "rate_limited", secs
		ge.Message = fmt.Sprintf("Keys serving %s are rate limited (%d of %d candidates). Retry after %ds.", model, rl, n, secs)
	case credits > 0:
		ge.HTTP, ge.Type, ge.Code = http.StatusPaymentRequired, TypeCredits, "insufficient_credits"
		ge.Message = fmt.Sprintf("Provider keys serving %s are out of credits or over budget.", model)
	case unavailable == n:
		ge.HTTP, ge.Type, ge.Code, ge.Param = http.StatusNotFound, TypeInvalidRequest, "model_not_found", "model"
		ge.Message = fmt.Sprintf("No provider could serve %s: model not found.", model)
	case bad > 0 && bad+unavailable == n:
		ge.HTTP, ge.Type, ge.Code = http.StatusBadRequest, TypeInvalidRequest, "upstream_rejected_request"
		msg := firstBad.Message
		if msg == "" {
			msg = "request rejected"
		}
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		ge.Message = firstBad.Provider + ": " + msg
	default:
		ge.HTTP, ge.Type, ge.Code = http.StatusServiceUnavailable, TypeUpstream, "all_providers_failed"
		ge.Message = fmt.Sprintf("All %d candidate keys for %s failed.", n, model)
	}
	return ge
}
