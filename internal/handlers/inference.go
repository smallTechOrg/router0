package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/catalog"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/metadata"
	"github.com/zer0-router/zer0/internal/metrics"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
	"github.com/zer0-router/zer0/internal/tokens"
)

const (
	maxRequestBody   = 10 << 20
	providersHeader  = "X-Router0-Providers"
	statusClientGone = 499 // logged only; nothing is written to a departed client
)

// ProviderOrderer is satisfied by *keys.PriorityStore.
type ProviderOrderer interface {
	ProviderOrder(ctx context.Context, orgID string) ([]string, error)
}

// ActiveKeyLister is satisfied by *keys.Repository.
type ActiveKeyLister interface {
	ActiveByOrgProvider(ctx context.Context, orgID, provider string) ([]*keys.ProviderKey, error)
}

// InferenceConfig holds the explicit dependencies of the inference handler.
type InferenceConfig struct {
	Resolver catalog.Resolver // *catalog.CatalogResolver
	Priority ProviderOrderer  // *keys.PriorityStore
	Keys     ActiveKeyLister  // *keys.Repository
	Executor *routing.Executor
	Recorder *metadata.Recorder // optional (nil = no request log)
	// FirstByteTimeout: streaming time allowed until the first upstream chunk.
	// Zero reads ROUTER0_STREAM_FIRST_BYTE_TIMEOUT_SECONDS (20).
	FirstByteTimeout time.Duration
	// Decrypt turns a stored key into plaintext; nil uses keys.Decrypt.
	Decrypt func(*keys.ProviderKey) (string, error)
	// Pricing is the catalog price fallback for cost estimates (catalog.Repository);
	// optional. Used when the provider reports no cost and the static table has none.
	Pricing Pricer
	// TokenBudgets enforces per-service-token monthly budgets (tokens.BudgetGuard);
	// optional (nil = no token budgets).
	TokenBudgets TokenBudgetChecker
}

// TokenBudgetChecker reports a service token's budget position this month.
type TokenBudgetChecker interface {
	Check(ctx context.Context, tokenID string) tokens.BudgetStatus
}

// Pricer returns USD-per-million-token prices for an org's provider model.
type Pricer interface {
	Pricing(ctx context.Context, orgID, provider, providerModelID string) (in, out float64, ok bool)
}

// InferenceHandler serves POST /v1/chat/completions.
type InferenceHandler struct {
	resolver catalog.Resolver
	priority ProviderOrderer
	keys     ActiveKeyLister
	exec     *routing.Executor
	streamEx *routing.Executor
	recorder *metadata.Recorder
	firstB   time.Duration
	decrypt  func(*keys.ProviderKey) (string, error)
	pricing  Pricer
	budgets  TokenBudgetChecker
}

// NewInferenceHandler wires the handler. The executor's per-attempt timeout
// applies to non-streaming calls; streaming uses a copy without it and enforces
// the first-byte timeout itself.
func NewInferenceHandler(cfg InferenceConfig) *InferenceHandler {
	fb := cfg.FirstByteTimeout
	if fb <= 0 {
		fb = 20 * time.Second
		if v, err := strconv.Atoi(os.Getenv("ROUTER0_STREAM_FIRST_BYTE_TIMEOUT_SECONDS")); err == nil && v > 0 {
			fb = time.Duration(v) * time.Second
		}
	}
	dec := cfg.Decrypt
	if dec == nil {
		dec = func(k *keys.ProviderKey) (string, error) { return keys.Decrypt(k.Ciphertext, k.Nonce) }
	}
	return &InferenceHandler{
		resolver: cfg.Resolver, priority: cfg.Priority, keys: cfg.Keys,
		exec: cfg.Executor, streamEx: cfg.Executor.WithAttemptTimeout(0),
		recorder: cfg.Recorder, firstB: fb, decrypt: dec, pricing: cfg.Pricing, budgets: cfg.TokenBudgets,
	}
}

// ---- request parsing ----

type inMessage struct {
	Role       string               `json:"role"`
	Content    json.RawMessage      `json:"content"`
	ToolCalls  []providers.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	Name       string               `json:"name,omitempty"`
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []inMessage     `json:"messages"`
	MaxTokens           int             `json:"max_tokens"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Stop                json.RawMessage `json:"stop"`
	Stream              bool            `json:"stream"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools          []providers.Tool `json:"tools"`
	ToolChoice     any              `json:"tool_choice"`
	ResponseFormat any              `json:"response_format"`
	Seed           *int             `json:"seed"`
	User           string           `json:"user"`
}

var errUnsupportedContent = fmt.Errorf("unsupported content part")

// flattenContent turns a string or an array of text parts into a string.
func flattenContent(raw json.RawMessage) (string, error) {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return "", nil
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", err
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Type != "text" {
			return "", errUnsupportedContent
		}
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, "\n"), nil
}

func parseStop(raw json.RawMessage) []string {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return nil
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			return []string{s}
		}
		return nil
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	return nil
}

func (cr *chatRequest) toProvider() (*providers.ChatRequest, error) {
	msgs := make([]providers.ChatMessage, 0, len(cr.Messages))
	for _, m := range cr.Messages {
		c, err := flattenContent(m.Content)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, providers.ChatMessage{Role: m.Role, Content: c, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Name: m.Name})
	}
	mt := cr.MaxTokens
	if mt == 0 {
		mt = cr.MaxCompletionTokens
	}
	return &providers.ChatRequest{
		Messages: msgs, MaxTokens: mt, Temperature: cr.Temperature, TopP: cr.TopP, Seed: cr.Seed,
		User: cr.User, Stream: cr.Stream, Stop: parseStop(cr.Stop), Tools: cr.Tools,
		ToolChoice: cr.ToolChoice, ResponseFormat: cr.ResponseFormat,
	}, nil
}

// ---- per-request state ----

type reqState struct {
	id, orgID, tokenID, requested, canonical string
	stream                                   bool
	start                                    time.Time
}

func (h *InferenceHandler) setBaseHeaders(w http.ResponseWriter, rs *reqState, attempts int) {
	w.Header().Set("X-Router0-Request-Id", rs.id)
	if rs.canonical != "" {
		w.Header().Set("X-Router0-Canonical-Model", rs.canonical)
	}
	w.Header().Set("X-Router0-Attempts", strconv.Itoa(attempts))
}

func toMetaAttempts(in []routing.AttemptRecord) []metadata.Attempt {
	out := make([]metadata.Attempt, len(in))
	for i, a := range in {
		out[i] = metadata.Attempt{
			Order: a.Order, Provider: a.Provider, Model: a.ProviderModelID, KeyID: a.KeyID, KeyLabel: a.KeyLabel,
			Outcome: a.Outcome, ErrorClass: a.Class, SkipReason: a.SkipReason, ProviderErrorCode: a.Code,
			HTTPStatus: a.HTTP, RetryAfterS: retryAfterSeconds(a.RetryAfter), LatencyMs: a.LatencyMs,
		}
	}
	return out
}

func retryAfterSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

// trailJSON renders attempts per api.md (never provider bodies).
func trailJSON(attempts []routing.AttemptRecord) []map[string]any {
	out := make([]map[string]any, 0, len(attempts))
	for _, a := range attempts {
		m := map[string]any{"provider": a.Provider, "key": a.KeyLabel, "outcome": a.Outcome}
		if a.Class != "" {
			m["class"] = a.Class
		}
		if a.SkipReason != "" {
			m["skip_reason"] = a.SkipReason
		}
		if s := retryAfterSeconds(a.RetryAfter); s > 0 {
			m["retry_after_s"] = s
		}
		out = append(out, m)
	}
	return out
}

type outcome struct {
	provider, providerModel, keyID, keyLabel string
	status                                   string // success | error
	httpStatus                               int
	errorCode                                string
	in, out                                  int
	cost                                     float64
	attempts                                 []routing.AttemptRecord
}

// finish records the request log row, metrics and the request_completed line.
func (h *InferenceHandler) finish(rs *reqState, o outcome) {
	lat := time.Since(rs.start)
	provider := o.provider
	if provider == "" {
		provider = "none"
	}
	code := o.errorCode
	if o.status == "success" {
		code = "ok"
	}
	metrics.RequestsTotal.WithLabelValues(provider, o.status, code).Inc()
	metrics.RequestDuration.WithLabelValues(provider, strconv.FormatBool(rs.stream)).Observe(lat.Seconds())
	h.recorder.Record(metadata.Request{
		RequestID: rs.id, OrgID: rs.orgID, TokenID: rs.tokenID, CanonicalModel: rs.canonical,
		Provider: provider, Model: o.providerModel, KeyID: o.keyID, KeyLabel: o.keyLabel,
		InputTokens: o.in, OutputTokens: o.out, LatencyMs: lat.Milliseconds(), CostUSD: o.cost,
		Status: o.status, HTTPStatus: o.httpStatus, ErrorCode: o.errorCode, Stream: rs.stream,
		Attempts: toMetaAttempts(o.attempts),
	})
	slog.Info("request_completed", "request_id", rs.id, "org_id", rs.orgID, "token_id", rs.tokenID,
		"canonical_model", rs.canonical, "provider", provider, "provider_model_id", o.providerModel,
		"key_label", o.keyLabel, "attempts", len(o.attempts), "status", o.status, "error_code", o.errorCode,
		"latency_ms", lat.Milliseconds(), "input_tokens", o.in, "output_tokens", o.out)
}

func (h *InferenceHandler) failEarly(w http.ResponseWriter, rs *reqState, status int, errType, code, message, param string) {
	h.setBaseHeaders(w, rs, 0)
	writeOpenAIError(w, status, errType, code, message, param, map[string]any{"request_id": rs.id, "attempts": []any{}})
	h.finish(rs, outcome{status: "error", httpStatus: status, errorCode: code})
}

// ---- handler ----

// ChatCompletions handles POST /v1/chat/completions.
func (h *InferenceHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rs := &reqState{id: "r0-" + strings.ReplaceAll(uuid.NewString(), "-", ""), start: time.Now()}
	w.Header().Set("X-Router0-Request-Id", rs.id)

	p, ok := auth.PrincipalFromCtx(ctx)
	if !ok {
		writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Missing or invalid router0 token", "", nil)
		return
	}
	rs.orgID, rs.tokenID = p.OrgID, p.TokenID

	var cr chatRequest
	body := http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(body).Decode(&cr); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "Request body is not valid JSON", "", nil)
		return
	}
	cr.Model = strings.TrimSpace(cr.Model)
	if cr.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "missing_model", "The 'model' field is required", "model", nil)
		return
	}
	base, err := cr.toProvider()
	if err != nil {
		msg := "Message content must be a string or an array of text parts"
		code := "invalid_json"
		if err == errUnsupportedContent {
			msg, code = "Only text content parts are supported", "unsupported_content"
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", code, msg, "messages", nil)
		return
	}
	rs.requested, rs.stream = cr.Model, cr.Stream
	rs.canonical = cr.Model

	if h.budgets != nil {
		if b := h.budgets.Check(ctx, rs.tokenID); b.Exceeded() {
			h.failEarly(w, rs, http.StatusPaymentRequired, "insufficient_credits", "token_budget_exceeded",
				fmt.Sprintf("This service token reached its monthly budget of $%.4f (estimated spend $%.6f this month). "+
					"It resets at 00:00 UTC on the 1st, or an org owner/admin can raise it on the Tokens page.", b.BudgetUSD, b.SpendUSD), "")
			return
		}
	}

	override, unknown := routing.ParseProviderOverride(r.Header.Get(providersHeader))
	if unknown != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_provider_override",
			fmt.Sprintf("Unknown provider %q in %s", unknown, providersHeader), "", nil)
		return
	}

	entries, err := h.resolver.Resolve(ctx, rs.orgID, cr.Model)
	if err != nil {
		slog.Error("catalog resolve failed", "request_id", rs.id, "error", err.Error())
		h.failEarly(w, rs, http.StatusServiceUnavailable, "upstream_error", "router_unavailable", "Routing is temporarily unavailable", "")
		return
	}
	if len(entries) == 0 {
		h.failEarly(w, rs, http.StatusNotFound, "invalid_request_error", "model_not_found",
			fmt.Sprintf("The model '%s' is not available for this organization.", cr.Model), "model")
		return
	}
	rs.canonical = entries[0].Canonical

	order, err := h.priority.ProviderOrder(ctx, rs.orgID)
	if err != nil {
		slog.Error("provider order failed", "request_id", rs.id, "error", err.Error())
		h.failEarly(w, rs, http.StatusServiceUnavailable, "upstream_error", "router_unavailable", "Routing is temporarily unavailable", "")
		return
	}
	keysBy := map[string][]*keys.ProviderKey{}
	keyByID := map[string]*keys.ProviderKey{}
	for _, e := range entries {
		prov := providers.CanonicalName(e.Provider)
		if _, done := keysBy[prov]; done {
			continue
		}
		ks, err := h.keys.ActiveByOrgProvider(ctx, rs.orgID, prov)
		if err != nil {
			slog.Error("load keys failed", "request_id", rs.id, "provider", prov, "error", err.Error())
			h.failEarly(w, rs, http.StatusServiceUnavailable, "upstream_error", "router_unavailable", "Routing is temporarily unavailable", "")
			return
		}
		keysBy[prov] = ks
		for _, k := range ks {
			keyByID[k.ID] = k
		}
	}

	plan := routing.BuildPlan(entries, order, override, keysBy)
	if len(plan) == 0 {
		msg := fmt.Sprintf("No provider key in this organization serves the model '%s'.", cr.Model)
		if len(override) > 0 {
			msg = fmt.Sprintf("The model '%s' is not offered by providers in %s (%s)", cr.Model, providersHeader, strings.Join(override, ","))
		}
		h.failEarly(w, rs, http.StatusNotFound, "invalid_request_error", "model_not_found", msg, "model")
		return
	}

	secret := func(c routing.Candidate) (string, error) {
		k := keyByID[c.KeyID]
		if k == nil {
			return "", fmt.Errorf("key not loaded")
		}
		return h.decrypt(k)
	}
	if cr.Stream {
		h.serveStream(w, r, rs, base, plan, secret, cr.StreamOptions != nil && cr.StreamOptions.IncludeUsage)
		return
	}
	h.serveBlocking(w, r, rs, base, plan, secret)
}

func unknownProvider(name string) *providers.ProviderError {
	return &providers.ProviderError{Class: providers.ErrorClassTransient, Code: "unknown_provider", Message: "provider adapter not registered: " + name}
}

// failFinal writes the exhausted-plan error and records it.
func (h *InferenceHandler) failFinal(w http.ResponseWriter, rs *reqState, res routing.Result) {
	if res.Cancelled {
		h.finish(rs, outcome{status: "error", httpStatus: statusClientGone, errorCode: routing.CodeClientCancelled, attempts: res.Attempts})
		return
	}
	ge := routing.FinalError(res)
	h.setBaseHeaders(w, rs, len(res.Attempts))
	if ge.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ge.RetryAfterSeconds))
	}
	writeOpenAIError(w, ge.HTTP, ge.Type, ge.Code, ge.Message, ge.Param,
		map[string]any{"request_id": rs.id, "attempts": trailJSON(res.Attempts)})
	h.finish(rs, outcome{status: "error", httpStatus: ge.HTTP, errorCode: ge.Code, attempts: res.Attempts})
}

func setWinnerHeaders(w http.ResponseWriter, c *routing.Candidate, lat time.Duration) {
	w.Header().Set("X-Router0-Provider", c.Provider)
	w.Header().Set("X-Router0-Model", c.ProviderModelID)
	w.Header().Set("X-Router0-Key", c.KeyLabel)
	w.Header().Set("X-Router0-Latency-Ms", strconv.FormatInt(lat.Milliseconds(), 10))
}

// ---- non-streaming ----

func (h *InferenceHandler) serveBlocking(w http.ResponseWriter, r *http.Request, rs *reqState, base *providers.ChatRequest,
	plan []routing.Candidate, secret func(routing.Candidate) (string, error)) {
	ex := h.exec.ForRequest(rs.orgID, rs.requested, secret)
	var resp *providers.ChatResponse
	res := ex.Run(r.Context(), plan, func(ctx context.Context, c routing.Candidate, apiKey string) error {
		prov, err := providers.Get(c.Provider)
		if err != nil {
			return unknownProvider(c.Provider)
		}
		req := *base
		req.Model = c.ProviderModelID
		req.Stream = false
		out, err := prov.Complete(ctx, &req, apiKey)
		if err != nil {
			return err
		}
		resp = out
		return nil
	})
	if res.Winner == nil || resp == nil {
		h.failFinal(w, rs, res)
		return
	}
	lat := time.Since(rs.start)
	h.setBaseHeaders(w, rs, len(res.Attempts))
	setWinnerHeaders(w, res.Winner, lat)

	msg := map[string]any{"role": "assistant", "content": resp.Content}
	if len(resp.ToolCalls) > 0 {
		msg["tool_calls"] = resp.ToolCalls
		if resp.Content == "" {
			msg["content"] = nil
		}
	}
	finish := resp.FinishReason
	if finish == "" {
		finish = "stop"
		if len(resp.ToolCalls) > 0 {
			finish = "tool_calls"
		}
	}
	id := resp.ID
	if id == "" {
		id = "chatcmpl-" + strings.TrimPrefix(rs.id, "r0-")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": rs.canonical,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finish}},
		"usage": map[string]int{"prompt_tokens": resp.Usage.PromptTokens, "completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens": resp.Usage.TotalTokens},
	})
	cost := resp.Cost
	if cost <= 0 {
		cost = h.estimateCost(rs.orgID, res.Winner.Provider, res.Winner.ProviderModelID, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	}
	h.finish(rs, outcome{
		provider: res.Winner.Provider, providerModel: res.Winner.ProviderModelID, keyID: res.Winner.KeyID, keyLabel: res.Winner.KeyLabel,
		status: "success", httpStatus: http.StatusOK, in: resp.Usage.PromptTokens, out: resp.Usage.CompletionTokens,
		cost: cost, attempts: res.Attempts,
	})
}

// ---- streaming ----

type committedStream struct {
	ch     <-chan providers.ChatChunk
	first  providers.ChatChunk
	cancel context.CancelFunc
}

func (h *InferenceHandler) serveStream(w http.ResponseWriter, r *http.Request, rs *reqState, base *providers.ChatRequest,
	plan []routing.Candidate, secret func(routing.Candidate) (string, error), clientWantsUsage bool) {
	ctx := r.Context()
	ex := h.streamEx.ForRequest(rs.orgID, rs.requested, secret)
	var cs *committedStream
	defer func() {
		if cs != nil {
			cs.cancel()
		}
	}()

	res := ex.Run(ctx, plan, func(actx context.Context, c routing.Candidate, apiKey string) error {
		prov, err := providers.Get(c.Provider)
		if err != nil {
			return unknownProvider(c.Provider)
		}
		sctx, cancel := context.WithCancel(actx)
		var timedOut atomic.Bool
		timer := time.AfterFunc(h.firstB, func() { timedOut.Store(true); cancel() })
		fail := func(err error) error {
			timer.Stop()
			cancel()
			if timedOut.Load() && ctx.Err() == nil {
				return &providers.ProviderError{Class: providers.ErrorClassTransient, Code: "first_byte_timeout", Message: "no response from provider in time"}
			}
			return err
		}
		req := *base
		req.Model = c.ProviderModelID
		req.Stream = true
		ch, err := prov.Stream(sctx, &req, apiKey)
		if err != nil {
			return fail(err)
		}
		select {
		case chunk, open := <-ch:
			switch {
			case !open:
				return fail(&providers.ProviderError{Class: providers.ErrorClassTransient, Code: "stream_interrupted", Message: "upstream closed before any data"})
			case chunk.Err != nil:
				return fail(chunk.Err)
			case chunk.Done:
				return fail(&providers.ProviderError{Class: providers.ErrorClassTransient, Code: "empty_stream", Message: "upstream sent no data"})
			}
			if !timer.Stop() { // the first-byte timer already fired
				return fail(nil)
			}
			cs = &committedStream{ch: ch, first: chunk, cancel: cancel}
			return nil
		case <-sctx.Done():
			return fail(sctx.Err())
		}
	})
	if res.Winner == nil || cs == nil {
		h.failFinal(w, rs, res)
		return
	}

	// Commit: from here on there is no failover.
	win := res.Winner
	h.setBaseHeaders(w, rs, len(res.Attempts))
	setWinnerHeaders(w, win, time.Since(rs.start))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }

	chunkID := "chatcmpl-" + strings.TrimPrefix(rs.id, "r0-")
	created := time.Now().Unix()
	sentRole := false
	var usage *providers.Usage
	emit := func(c providers.ChatChunk) {
		if c.Usage != nil {
			u := *c.Usage
			usage = &u
		}
		usageOnly := c.Usage != nil && c.Content == "" && len(c.ToolCalls) == 0 && c.FinishReason == ""
		if !usageOnly {
			delta := map[string]any{}
			if !sentRole {
				delta["role"] = "assistant"
				sentRole = true
			}
			if c.Content != "" {
				delta["content"] = c.Content
			}
			if len(c.ToolCalls) > 0 {
				delta["tool_calls"] = c.ToolCalls
			}
			var fr any
			if c.FinishReason != "" {
				fr = c.FinishReason
			}
			writeSSE(w, map[string]any{"id": chunkID, "object": "chat.completion.chunk", "created": created, "model": rs.canonical,
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": fr}}})
		}
		if c.Usage != nil && clientWantsUsage {
			writeSSE(w, map[string]any{"id": chunkID, "object": "chat.completion.chunk", "created": created, "model": rs.canonical,
				"choices": []any{}, "usage": c.Usage})
		}
		flush()
	}

	emit(cs.first)
	var streamErr *providers.ProviderError
loop:
	for c := range cs.ch {
		switch {
		case c.Err != nil:
			streamErr = c.Err
			break loop
		case c.Done:
			break loop
		}
		emit(c)
	}

	o := outcome{provider: win.Provider, providerModel: win.ProviderModelID, keyID: win.KeyID, keyLabel: win.KeyLabel,
		status: "success", httpStatus: http.StatusOK, attempts: res.Attempts}
	switch {
	case streamErr != nil:
		_, _ = fmt.Fprintf(w, "data: %s\n\n", mustJSON(map[string]any{"error": map[string]any{
			"type": "upstream_error", "code": "stream_interrupted", "message": "The upstream provider interrupted the stream."}}))
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
		ex.MarkFailure(ctx, *win, streamErr)
		o.status, o.errorCode = "error", "stream_interrupted"
		if n := len(o.attempts); n > 0 {
			last := o.attempts[n-1]
			last.Outcome, last.Class, last.HTTP, last.Code = routing.OutcomeError, string(streamErr.Class), streamErr.HTTP, streamErr.Code
			o.attempts = append(append([]routing.AttemptRecord{}, o.attempts[:n-1]...), last)
		}
		slog.Warn("attempt_failed", "provider", win.Provider, "key_label", win.KeyLabel, "class", streamErr.Class, "code", streamErr.Code, "mid_stream", true)
	case ctx.Err() != nil:
		o.status, o.errorCode, o.httpStatus = "error", routing.CodeClientCancelled, statusClientGone
	default:
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
	}
	if usage != nil {
		o.in, o.out = usage.PromptTokens, usage.CompletionTokens
		o.cost = h.estimateCost(rs.orgID, win.Provider, win.ProviderModelID, o.in, o.out)
	}
	h.finish(rs, o)
}

func writeSSE(w http.ResponseWriter, v any) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", mustJSON(v))
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

// estimateCost prices a request from router0's static table, falling back to
// the org catalog's per-model prices (e.g. from the provider's model listing).
// Budgets are built on these estimates.
func (h *InferenceHandler) estimateCost(orgID, provider, model string, in, out int) float64 {
	if c := metadata.EstimateCost(provider, model, in, out); c > 0 || h.pricing == nil {
		return c
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pin, pout, ok := h.pricing.Pricing(ctx, orgID, provider, model)
	if !ok {
		return 0
	}
	return (float64(in)*pin + float64(out)*pout) / 1e6
}
