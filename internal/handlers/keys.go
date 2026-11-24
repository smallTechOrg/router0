package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/balance"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/keystate"
	"github.com/zer0-router/zer0/internal/providers"
)

// Discoverer triggers catalog discovery for an (org, provider). Satisfied by
// catalog.Discoverer (internal/catalog).
type Discoverer interface {
	RefreshProvider(ctx context.Context, orgID, provider string) (int, error)
}

// ModelLister lists models for a provider with a raw key (no tokens spent).
// Used by POST /api/keys/{id}/test. Errors may be *providers.ProviderError.
type ModelLister func(ctx context.Context, provider, apiKey string) (modelCount int, err error)

// KeysDeps are the injected collaborators of KeysHandler.
type KeysDeps struct {
	// ValidProviders returns registered provider ids (providers.ValidProviders). Required.
	ValidProviders func() []string
	// States reads per-key live state. Nil defaults to keys.NewPGStateReader(pg).
	States keys.StateReader
	// Discoverer is optional; nil disables the post-add / post-test catalog refresh.
	Discoverer Discoverer
	// ListModels is optional; nil defaults to the registry adapter's ListModels.
	ListModels ModelLister
	// KeyState resets and invalidates cached key state (keystate.PGStore).
	// Nil disables POST /api/keys/{id}/reset-state (501).
	KeyState KeyStateAdmin
	// Balances polls provider balances on demand (balance.Poller). Nil disables
	// POST /api/keys/poll-balances (501).
	Balances BalancePoller
	// BalanceSupported reports whether a provider's balance can be polled.
	// Nil means none can.
	BalanceSupported func(provider string) bool
}

// KeyStateAdmin is the keystate subset the keys handler needs.
type KeyStateAdmin interface {
	Reset(ctx context.Context, keyID string) error
	Invalidate(keyID string)
}

// BalancePoller polls the balances of an org's keys now (balance.Poller).
// TryPollNow returns balance.ErrBusy when a poll is already running.
type BalancePoller interface {
	TryPollNow(ctx context.Context, orgID string) (int, error)
}

type KeysHandler struct {
	pool     *pgxpool.Pool
	repo     *keys.Repository
	priority *keys.PriorityStore
	deps     KeysDeps
}

func NewKeysHandler(pg *pgxpool.Pool, deps KeysDeps) *KeysHandler {
	if deps.ValidProviders == nil {
		deps.ValidProviders = providers.ValidProviders
	}
	if deps.States == nil {
		deps.States = keys.NewPGStateReader(pg)
	}
	if deps.ListModels == nil {
		deps.ListModels = func(ctx context.Context, provider, apiKey string) (int, error) {
			p, err := providers.Get(provider)
			if err != nil {
				return 0, err
			}
			ms, err := p.ListModels(ctx, apiKey)
			return len(ms), err
		}
	}
	if deps.BalanceSupported == nil {
		deps.BalanceSupported = func(string) bool { return false }
	}
	return &KeysHandler{
		pool:     pg,
		repo:     keys.NewRepository(pg),
		priority: keys.NewPriorityStore(pg, deps.ValidProviders),
		deps:     deps,
	}
}

// Priority returns the PriorityStore backing this handler so wiring can share it.
func (h *KeysHandler) Priority() *keys.PriorityStore { return h.priority }

// providerLabels is the display-name map for GET /api/providers; unknown ids fall back to the id.
var providerLabels = map[string]string{
	"groq":       "Groq",
	"openrouter": "OpenRouter",
	"google":     "Gemini",
	"nvidia":     "NVIDIA",
	"together":   "Together",
	"baseten":    "Baseten",
	"anthropic":  "Anthropic",
	"meshapi":    "MeshAPI",
}

func providerLabel(id string) string {
	if l, ok := providerLabels[id]; ok {
		return l
	}
	return id
}

func (h *KeysHandler) isValidProvider(p string) bool {
	for _, v := range h.deps.ValidProviders() {
		if v == p {
			return true
		}
	}
	return false
}

type addKeyReq struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	ModelID  string `json:"model_id"`
}

type keyJSON struct {
	ID             string     `json:"id"`
	Provider       string     `json:"provider"`
	Label          string     `json:"label"`
	KeyHint        string     `json:"key_hint"`
	Position       int        `json:"position"`
	Status         string     `json:"status"`
	LastTestedAt   *string    `json:"last_tested_at"`
	LastTestResult *string    `json:"last_test_result"`
	State          *stateJSON `json:"state"`
	CreatedAt      string     `json:"created_at"`
	// Phase 3: budget, estimated month-to-date spend (UTC month) and balance.
	MonthlyBudgetUSD *float64     `json:"monthly_budget_usd"`
	MonthSpendUSD    *float64     `json:"month_spend_usd"`
	Balance          *balanceJSON `json:"balance"`
}

type balanceJSON struct {
	Supported    bool     `json:"supported"`
	Status       string   `json:"status"` // ok | exhausted | error | unsupported | pending
	RemainingUSD *float64 `json:"remaining_usd"`
	LimitUSD     *float64 `json:"limit_usd"`
	UsageUSD     *float64 `json:"usage_usd"`
	PolledAt     *string  `json:"polled_at"`
}

type stateJSON struct {
	Blocked     bool     `json:"blocked"`
	Reason      string   `json:"reason,omitempty"`
	RetryAfterS int      `json:"retry_after_s,omitempty"`
	Models      []string `json:"models,omitempty"`
}

func toKeyJSON(k *keys.ProviderKey, st *keys.KeyState) keyJSON {
	kj := keyJSON{
		ID: k.ID, Provider: k.Provider, Label: k.Label, KeyHint: k.KeyHint,
		Position: k.Position, Status: k.Status, CreatedAt: k.CreatedAt.Format(time.RFC3339),
		MonthlyBudgetUSD: k.MonthlyBudgetUSD,
	}
	if k.LastTestedAt != nil {
		s := k.LastTestedAt.Format(time.RFC3339)
		kj.LastTestedAt = &s
	}
	if k.LastTestResult != "" {
		r := k.LastTestResult
		kj.LastTestResult = &r
	}
	if st != nil {
		secs := int((st.RetryAfter + time.Second - 1) / time.Second)
		kj.State = &stateJSON{Blocked: st.Blocked, Reason: st.Reason, RetryAfterS: secs, Models: st.Models}
	}
	return kj
}

// refreshAsync triggers catalog discovery without blocking the request; errors are non-fatal.
func (h *KeysHandler) refreshAsync(orgID, provider string) {
	d := h.deps.Discoverer
	if d == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := d.RefreshProvider(ctx, orgID, provider); err != nil {
			slog.Warn("keys: catalog refresh failed", "org_id", orgID, "provider", provider, "error", err)
		}
	}()
}

func (h *KeysHandler) AddKey(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	userID := auth.UserIDFromCtx(r.Context())
	var req addKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Key = strings.TrimSpace(req.Key)
	req.Label = strings.TrimSpace(req.Label)
	if req.Key == "" || req.Provider == "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_fields", "provider and key are required")
		return
	}
	if !h.isValidProvider(req.Provider) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_provider", "Unknown provider")
		return
	}
	if len(req.Label) > 64 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "label_too_long", "label must be at most 64 characters")
		return
	}
	if req.Provider == "baseten" && req.ModelID == "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_model_id", "model_id is required for baseten")
		return
	}

	hint := req.Key
	if r := []rune(hint); len(r) > 8 {
		hint = string(r[:8])
	}
	hint += "…"

	ciphertext, nonce, err := keys.Encrypt(req.Key)
	if err != nil {
		slog.Error("keys: encrypt failed")
		writeError(w, http.StatusInternalServerError, "internal_error", "encrypt_failed", "Failed to encrypt key")
		return
	}

	k, err := h.repo.Create(r.Context(), orgID, userID, req.Provider, req.Label, hint, req.ModelID, ciphertext, nonce)
	if err != nil {
		if errors.Is(err, keys.ErrDuplicateLabel) {
			writeError(w, http.StatusConflict, "conflict", "key_exists", "A key with this provider and label already exists")
			return
		}
		slog.Error("keys: failed to save key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to save key")
		return
	}

	h.refreshAsync(orgID, k.Provider)
	writeJSON(w, http.StatusCreated, toKeyJSON(k, nil))
}

func (h *KeysHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	ks, err := h.repo.ListByOrg(r.Context(), orgID)
	if err != nil {
		slog.Error("keys: list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list keys")
		return
	}

	// Sort by provider priority, then position.
	rank := map[string]int{}
	if order, err := h.priority.ProviderOrder(r.Context(), orgID); err == nil {
		for i, p := range order {
			rank[p] = i
		}
	}
	sort.SliceStable(ks, func(i, j int) bool {
		ri, rj := rank[ks[i].Provider], rank[ks[j].Provider]
		if ri != rj {
			return ri < rj
		}
		if ks[i].Provider != ks[j].Provider {
			return ks[i].Provider < ks[j].Provider
		}
		return ks[i].Position < ks[j].Position
	})

	// A keystate failure degrades to state:null; the page still renders.
	var states map[string]keys.KeyState
	if len(ks) > 0 {
		ids := make([]string, len(ks))
		for i, k := range ks {
			ids[i] = k.ID
		}
		var serr error
		states, serr = h.deps.States.States(r.Context(), ids)
		if serr != nil {
			slog.Warn("keys: key state unavailable", "error", serr)
			states = nil
		}
	}

	// Spend and balances degrade to null the same way.
	var usage map[string]keys.Usage
	if len(ks) > 0 {
		ids := make([]string, len(ks))
		for i, k := range ks {
			ids[i] = k.ID
		}
		var uerr error
		if usage, uerr = keys.LoadUsage(r.Context(), h.pool, ids); uerr != nil {
			slog.Warn("keys: usage unavailable", "error", uerr)
			usage = nil
		}
	}

	result := make([]keyJSON, 0, len(ks))
	for _, k := range ks {
		var st *keys.KeyState
		if states != nil {
			s := states[k.ID] // zero value = not blocked
			st = &s
		}
		var u *keys.Usage
		if usage != nil {
			if x, ok := usage[k.ID]; ok {
				u = &x
			}
		}
		if st != nil && u != nil {
			applyBudgetState(k, st, u.MonthSpendMicroUSD, time.Now())
		}
		kj := toKeyJSON(k, st)
		h.addUsage(&kj, k, u)
		result = append(result, kj)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": result})
}

func (h *KeysHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	if err := h.repo.Disable(r.Context(), chi.URLParam(r, "id"), orgID); err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
			return
		}
		slog.Error("keys: delete failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to delete key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestKey calls the adapter's ListModels with the key (no tokens spent).
func (h *KeysHandler) TestKey(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	keyID := chi.URLParam(r, "id")

	k, err := h.repo.GetByID(r.Context(), keyID, orgID)
	if err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load key")
		return
	}
	plain, err := keys.Decrypt(k.Ciphertext, k.Nonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "decrypt_failed", "Failed to decrypt key")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	n, lerr := h.deps.ListModels(ctx, k.Provider, plain)
	latency := time.Since(start).Milliseconds()

	if lerr != nil {
		// Never echo err text: provider messages / transport errors could embed key material.
		class, msg := "transient", "Could not reach the provider or it returned an error"
		var pe *providers.ProviderError
		if errors.As(lerr, &pe) && pe.Class != "" {
			class = string(pe.Class)
			switch pe.Class {
			case providers.ErrorClassAuth:
				msg = "The provider rejected this key"
			case providers.ErrorClassRateLimit:
				msg = "The provider rate-limited the request"
			case providers.ErrorClassInsufficientCredit:
				msg = "The key has insufficient credits"
			default:
				msg = "The provider returned an error"
			}
		}
		isAuth := class == string(providers.ErrorClassAuth)
		if err := h.repo.RecordTest(r.Context(), keyID, orgID, "error:"+class, false, isAuth); err != nil {
			slog.Error("keys: record test failed", "error", err)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "error", "class": class, "message": msg})
		return
	}

	if err := h.repo.RecordTest(r.Context(), keyID, orgID, "ok", true, false); err != nil {
		slog.Error("keys: record test failed", "error", err)
	}
	h.refreshAsync(orgID, k.Provider)
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "latency_ms": latency, "model_count": n})
}

// applyBudgetState reports budget_exhausted for a key over its monthly budget
// unless a higher-precedence state (credits_exhausted) already applies. The
// budget is computed, not stored, so it is merged in here.
func applyBudgetState(k *keys.ProviderKey, st *keys.KeyState, spendMicro int64, now time.Time) {
	if k.MonthlyBudgetUSD == nil || spendMicro < keystate.MicroUSD(*k.MonthlyBudgetUSD) {
		return
	}
	if st.Blocked && st.Reason == keystate.ReasonCreditsExhausted {
		return
	}
	*st = keys.KeyState{Blocked: true, Reason: keystate.ReasonBudgetExhausted,
		RetryAfter: keystate.MonthStart(now).AddDate(0, 1, 0).Sub(now)}
}

func (h *KeysHandler) addUsage(kj *keyJSON, k *keys.ProviderKey, u *keys.Usage) {
	supported := h.deps.BalanceSupported(k.Provider)
	bj := &balanceJSON{Supported: supported, Status: "unsupported"}
	if supported {
		bj.Status = "pending"
	}
	if u != nil {
		spend := float64(u.MonthSpendMicroUSD) / 1e6
		kj.MonthSpendUSD = &spend
		if supported && u.Balance != nil {
			b := u.Balance
			polled := b.PolledAt.Format(time.RFC3339)
			bj.Status, bj.RemainingUSD, bj.LimitUSD, bj.UsageUSD, bj.PolledAt = b.Status, b.RemainingUSD, b.LimitUSD, b.UsageUSD, &polled
		}
	}
	kj.Balance = bj
}

// maxBudgetUSD is the largest value provider_keys.monthly_budget_usd (NUMERIC(12,4)) holds.
const maxBudgetUSD = 99999999.9999

// UpdateKey serves PATCH /api/keys/{id}: {"label": "...", "monthly_budget_usd": 50 | null}.
func (h *KeysHandler) UpdateKey(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	keyID := chi.URLParam(r, "id")
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	var u keys.KeyUpdate
	for field, v := range raw {
		switch field {
		case "label":
			var label string
			if err := json.Unmarshal(v, &label); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_label", "label must be a string")
				return
			}
			label = strings.TrimSpace(label)
			if label == "" || len(label) > 64 {
				writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_label", "label must be 1 to 64 characters")
				return
			}
			u.Label = &label
		case "monthly_budget_usd":
			u.SetBudget = true
			if string(v) == "null" {
				continue
			}
			var b float64
			if err := json.Unmarshal(v, &b); err != nil || b < 0 || b > maxBudgetUSD {
				writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_budget",
					"monthly_budget_usd must be a number from 0 to 99999999.9999, or null")
				return
			}
			// The column is NUMERIC(12,4): store what will be kept, and refuse a
			// positive amount that would silently round to a $0 budget.
			rounded := math.Round(b*1e4) / 1e4
			if b > 0 && rounded == 0 {
				writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_budget",
					"monthly_budget_usd must be 0 or at least 0.0001")
				return
			}
			u.Budget = &rounded
		default:
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "unknown_field", "Unknown field: "+field)
			return
		}
	}
	if u.Label == nil && !u.SetBudget {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_fields", "Nothing to update: send label and/or monthly_budget_usd")
		return
	}
	k, err := h.repo.Update(r.Context(), keyID, orgID, u)
	switch {
	case errors.Is(err, keys.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
		return
	case errors.Is(err, keys.ErrDuplicateLabel):
		writeError(w, http.StatusConflict, "conflict", "key_exists", "A key with this provider and label already exists")
		return
	case err != nil:
		slog.Error("keys: update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to update key")
		return
	}
	if u.SetBudget && h.deps.KeyState != nil {
		h.deps.KeyState.Invalidate(k.ID) // a changed budget applies to the next request
	}
	kj := toKeyJSON(k, nil)
	if us, uerr := keys.LoadUsage(r.Context(), h.pool, []string{k.ID}); uerr == nil {
		if x, ok := us[k.ID]; ok {
			h.addUsage(&kj, k, &x)
		}
	}
	writeJSON(w, http.StatusOK, kj)
}

// ResetKeyState serves POST /api/keys/{id}/reset-state: deletes the key's
// provider_key_state rows (cooldowns, credits) and evicts its cached state.
func (h *KeysHandler) ResetKeyState(w http.ResponseWriter, r *http.Request) {
	if h.deps.KeyState == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "not_implemented", "Key state reset is not available")
		return
	}
	orgID := auth.OrgIDFromCtx(r.Context())
	k, err := h.repo.GetByID(r.Context(), chi.URLParam(r, "id"), orgID)
	if errors.Is(err, keys.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load key")
		return
	}
	if err := h.deps.KeyState.Reset(r.Context(), k.ID); err != nil {
		slog.Error("keys: reset state failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to reset key state")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PollBalances serves POST /api/keys/poll-balances: polls the org's
// balance-capable keys now and returns {"polled": n}.
func (h *KeysHandler) PollBalances(w http.ResponseWriter, r *http.Request) {
	if h.deps.Balances == nil {
		writeError(w, http.StatusNotImplemented, "not_implemented", "not_implemented", "Balance polling is not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	n, err := h.deps.Balances.TryPollNow(ctx, auth.OrgIDFromCtx(r.Context()))
	if errors.Is(err, balance.ErrBusy) {
		writeError(w, http.StatusConflict, "conflict", "poll_in_progress", "A balance poll is already running; try again in a minute")
		return
	}
	if err != nil {
		slog.Error("keys: poll balances failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "poll_failed", "Failed to poll balances")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"polled": n})
}
