package routing

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/zer0-router/zer0/internal/keystate"
	"github.com/zer0-router/zer0/internal/metrics"
	"github.com/zer0-router/zer0/internal/providers"
)

// AttemptFunc performs one upstream call. nil = success; a *providers.ProviderError
// = classified failure. Any other error is treated as transient, unless the
// caller's context ended (then the loop stops and nothing is marked).
type AttemptFunc func(ctx context.Context, c Candidate, apiKey string) error

// AuthMarker flags a key as invalid (keys.Repository.MarkAuthError).
type AuthMarker interface {
	MarkAuthError(ctx context.Context, keyID string) error
}

// UnavailableMarker flags a catalog row unavailable (catalog.Repository.MarkUnavailable).
type UnavailableMarker interface {
	MarkUnavailable(ctx context.Context, orgID, provider, providerModelID string) error
}

// Config configures an Executor. Zero MaxAttempts/AttemptTimeout read
// ROUTER0_MAX_ATTEMPTS (8) and ROUTER0_ATTEMPT_TIMEOUT_SECONDS (120).
type Config struct {
	States         keystate.Store
	Auth           AuthMarker        // optional
	Catalog        UnavailableMarker // optional
	MaxAttempts    int
	AttemptTimeout time.Duration // < 0 disables the per-attempt timeout
	Now            func() time.Time
}

// Executor runs a plan sequentially with failover. It is safe for concurrent
// use; per-request data is bound with ForRequest.
type Executor struct {
	cfg    Config
	orgID  string
	secret func(Candidate) (string, error)
	model  string
}

// NewExecutor applies env defaults and returns an Executor.
func NewExecutor(cfg Config) *Executor {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = envInt("ROUTER0_MAX_ATTEMPTS", 8)
	}
	if cfg.AttemptTimeout == 0 {
		cfg.AttemptTimeout = time.Duration(envInt("ROUTER0_ATTEMPT_TIMEOUT_SECONDS", 120)) * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Executor{cfg: cfg}
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ForRequest returns a copy bound to one request: the org (for catalog side
// effects), a secret resolver that decrypts the candidate's key (called inside
// the attempt, the plaintext is never retained), and the requested model name
// used in error messages.
func (e *Executor) ForRequest(orgID, model string, secret func(Candidate) (string, error)) *Executor {
	c := *e
	c.orgID, c.model, c.secret = orgID, model, secret
	return &c
}

// WithAttemptTimeout returns a copy with a different per-attempt timeout
// (<= 0 disables it). The streaming path disables it and enforces its own
// first-byte timeout, because the stream must outlive the attempt call.
func (e *Executor) WithAttemptTimeout(d time.Duration) *Executor {
	c := *e
	if d <= 0 {
		d = -1
	}
	c.cfg.AttemptTimeout = d
	return &c
}

// Run tries plan candidates in order until one succeeds, the plan is exhausted,
// MaxAttempts candidates were attempted, or ctx ends.
func (e *Executor) Run(ctx context.Context, plan []Candidate, fn AttemptFunc) Result {
	res := Result{Model: e.model}
	if len(plan) == 0 {
		return res
	}
	states := e.checkAll(ctx, plan)
	plan = deprioritizeTransient(plan, states)

	attempted := 0
	for _, c := range plan {
		if ctx.Err() != nil {
			res.Cancelled = true
			return res
		}
		st := states[keystate.KeyModel{KeyID: c.KeyID, ProviderModelID: c.ProviderModelID}]
		if st.Blocked && st.Reason != keystate.ReasonTransient {
			rec := AttemptRecord{Candidate: c, Order: len(res.Attempts), Outcome: OutcomeSkipped,
				SkipReason: st.Reason, RetryAfter: st.RetryAfter}
			switch st.Reason {
			case keystate.ReasonRateLimited:
				rec.Class = string(providers.ErrorClassRateLimit)
			case keystate.ReasonCreditsExhausted, keystate.ReasonBudgetExhausted:
				rec.Class = string(providers.ErrorClassInsufficientCredit)
			}
			res.Attempts = append(res.Attempts, rec)
			metrics.AttemptsTotal.WithLabelValues(c.Provider, OutcomeSkipped, rec.Class).Inc()
			metrics.KeySkipsTotal.WithLabelValues(c.Provider, st.Reason).Inc()
			continue
		}
		if attempted >= e.cfg.MaxAttempts {
			break
		}
		attempted++

		rec := AttemptRecord{Candidate: c, Order: len(res.Attempts)}
		start := e.cfg.Now()
		err := e.attempt(ctx, c, fn)
		rec.LatencyMs = e.cfg.Now().Sub(start).Milliseconds()

		if err == nil {
			rec.Outcome = OutcomeSuccess
			res.Attempts = append(res.Attempts, rec)
			w := c
			res.Winner = &w
			metrics.AttemptsTotal.WithLabelValues(c.Provider, OutcomeSuccess, "").Inc()
			return res
		}

		rec.Outcome = OutcomeError
		var pe *providers.ProviderError
		switch {
		case ctx.Err() != nil:
			// The caller went away: not a provider failure, never marked or classified.
			rec.Code = CodeClientCancelled
			res.Attempts = append(res.Attempts, rec)
			res.Cancelled = true
			metrics.AttemptsTotal.WithLabelValues(c.Provider, OutcomeError, CodeClientCancelled).Inc()
			return res
		case errors.As(err, &pe):
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
			pe = &providers.ProviderError{Class: providers.ErrorClassTransient, Code: CodeAttemptTimeout, Message: "attempt timed out"}
		default:
			pe = &providers.ProviderError{Class: providers.ErrorClassTransient, Code: CodeAttemptError, Message: "attempt failed"}
		}
		rec.Class, rec.HTTP, rec.Code, rec.Message = string(pe.Class), pe.HTTP, truncate(pe.Code, 200), pe.Message
		rec.RetryAfter = pe.RetryAfter
		if pe.Class == providers.ErrorClassRateLimit {
			if rec.RetryAfter <= 0 {
				rec.RetryAfter = providers.DefaultRateLimitCooldown()
			}
			rec.RetryAfter = providers.ClampCooldown(rec.RetryAfter)
			pe.RetryAfter = rec.RetryAfter
		}
		res.Attempts = append(res.Attempts, rec)
		metrics.AttemptsTotal.WithLabelValues(c.Provider, OutcomeError, rec.Class).Inc()
		slog.Warn("attempt_failed", "provider", c.Provider, "key_label", c.KeyLabel,
			"class", rec.Class, "http_status", rec.HTTP, "code", rec.Code)
		e.MarkFailure(ctx, c, pe)
	}
	return res
}

func (e *Executor) attempt(ctx context.Context, c Candidate, fn AttemptFunc) error {
	apiKey := ""
	if e.secret != nil {
		k, err := e.secret(c)
		if err != nil {
			return &providers.ProviderError{Class: providers.ErrorClassTransient, Code: CodeKeyDecryptFailed, Message: "stored provider key could not be decrypted"}
		}
		apiKey = k
	}
	if e.cfg.AttemptTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.cfg.AttemptTimeout)
		defer cancel()
	}
	return fn(ctx, c, apiKey)
}

// MarkFailure applies the side effects of a classified failure: key state for
// rate_limit / insufficient_credits / transient, provider_keys.status='error' for
// auth, catalog row unavailable for model_unavailable. Errors are logged and
// swallowed. Exported so the streaming handler can mark a mid-stream failure.
func (e *Executor) MarkFailure(ctx context.Context, c Candidate, pe *providers.ProviderError) {
	if pe == nil {
		return
	}
	// Detached from the request: the mark must survive a client disconnect, but not hang.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	switch pe.Class {
	case providers.ErrorClassRateLimit, providers.ErrorClassInsufficientCredit, providers.ErrorClassTransient:
		metrics.KeyStateMarksTotal.WithLabelValues(c.Provider, markReason(pe.Class)).Inc()
		if e.cfg.States != nil {
			if err := e.cfg.States.Mark(mctx, c.KeyID, c.ProviderModelID, pe); err != nil {
				slog.Warn("keystate mark failed", "provider", c.Provider, "key_label", c.KeyLabel, "error", err.Error())
			}
		}
	case providers.ErrorClassAuth:
		metrics.KeyStateMarksTotal.WithLabelValues(c.Provider, "auth_error").Inc()
		if e.cfg.Auth != nil {
			if err := e.cfg.Auth.MarkAuthError(mctx, c.KeyID); err != nil {
				slog.Warn("mark key auth error failed", "provider", c.Provider, "key_label", c.KeyLabel, "error", err.Error())
			}
		}
	case providers.ErrorClassModelUnavailable:
		if e.cfg.Catalog != nil && e.orgID != "" {
			if err := e.cfg.Catalog.MarkUnavailable(mctx, e.orgID, c.Provider, c.ProviderModelID); err != nil {
				slog.Warn("catalog mark unavailable failed", "provider", c.Provider, "error", err.Error())
			}
		}
	}
}

func markReason(c providers.ErrorClass) string {
	switch c {
	case providers.ErrorClassRateLimit:
		return keystate.ReasonRateLimited
	case providers.ErrorClassInsufficientCredit:
		return keystate.ReasonCreditsExhausted
	}
	return keystate.ReasonTransient
}

// checkAll does the single CheckMany for the request; errors fail open.
func (e *Executor) checkAll(ctx context.Context, plan []Candidate) map[keystate.KeyModel]keystate.State {
	if e.cfg.States == nil {
		return nil
	}
	pairs := make([]keystate.KeyModel, 0, len(plan))
	seen := map[keystate.KeyModel]bool{}
	for _, c := range plan {
		km := keystate.KeyModel{KeyID: c.KeyID, ProviderModelID: c.ProviderModelID}
		if !seen[km] {
			seen[km] = true
			pairs = append(pairs, km)
		}
	}
	m, err := e.cfg.States.CheckMany(ctx, pairs)
	if err != nil {
		slog.Warn("keystate check failed, failing open", "error", err.Error())
		return nil
	}
	return m
}

// deprioritizeTransient moves candidates whose key is in transient cooldown to
// the end, keeping relative order (they are not skipped).
func deprioritizeTransient(plan []Candidate, states map[keystate.KeyModel]keystate.State) []Candidate {
	if len(states) == 0 {
		return plan
	}
	front := make([]Candidate, 0, len(plan))
	var back []Candidate
	for _, c := range plan {
		st := states[keystate.KeyModel{KeyID: c.KeyID, ProviderModelID: c.ProviderModelID}]
		if st.Blocked && st.Reason == keystate.ReasonTransient {
			back = append(back, c)
		} else {
			front = append(front, c)
		}
	}
	return append(front, back...)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
