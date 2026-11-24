package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/catalog"
	"github.com/zer0-router/zer0/internal/tokens"
)

// TokensHandler serves /api/tokens (org-scoped service tokens).
type TokensHandler struct {
	repo    *tokens.Repository
	budgets *tokens.BudgetGuard // optional; evicted on budget changes
}

func NewTokensHandler(repo *tokens.Repository) *TokensHandler {
	return &TokensHandler{repo: repo}
}

// WithBudgetGuard makes budget changes take effect immediately on this instance.
func (h *TokensHandler) WithBudgetGuard(g *tokens.BudgetGuard) *TokensHandler {
	h.budgets = g
	return h
}

// Update: PATCH /api/tokens/{id} (owner/admin)
// {"monthly_budget_usd": 25 | null, "allowed_models": ["llama-3.3-70b", "fast"] | null}.
// Either field may be sent alone. Only live tokens can be updated (404
// otherwise, including other orgs' tokens).
func (h *TokensHandler) Update(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	for field := range raw {
		if field != "monthly_budget_usd" && field != "allowed_models" {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "unknown_field", "Unknown field: "+field)
			return
		}
	}
	budgetRaw, setBudget := raw["monthly_budget_usd"]
	modelsRaw, setModels := raw["allowed_models"]
	if !setBudget && !setModels {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_fields", "Nothing to update: send monthly_budget_usd and/or allowed_models")
		return
	}
	var budget *float64
	if setBudget {
		var msg string
		if budget, msg = parseBudget(budgetRaw); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_budget", msg)
			return
		}
	}
	var models []string
	if setModels {
		var msg string
		if models, msg = parseAllowedModels(modelsRaw); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_allowed_models", msg)
			return
		}
	}
	orgID, id := auth.OrgIDFromCtx(r.Context()), chi.URLParam(r, "id")
	var err error
	if setBudget {
		err = h.repo.SetBudget(r.Context(), orgID, id, budget)
	}
	if err == nil && setModels {
		err = h.repo.SetAllowedModels(r.Context(), orgID, id, models)
	}
	switch {
	case errors.Is(err, tokens.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "token_not_found", "Token not found")
		return
	case err != nil:
		slog.Error("update token failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to update token")
		return
	}
	if h.budgets != nil {
		h.budgets.Evict(id)
	}
	list, err := h.repo.List(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load token")
		return
	}
	for _, t := range list {
		if t.ID == id {
			writeJSON(w, http.StatusOK, t)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "token_not_found", "Token not found")
}

// parseAllowedModels validates an allowed_models JSON value: null (every
// model) or a non-empty array of up to 200 model or alias names. Names are
// trimmed, canonicalised (so "llama-3.3-70b-versatile" allows "llama-3.3-70b"
// on every provider) and de-duplicated. A non-empty msg is the 422 message.
func parseAllowedModels(v json.RawMessage) (models []string, msg string) {
	if string(v) == "null" {
		return nil, ""
	}
	var in []string
	if err := json.Unmarshal(v, &in); err != nil {
		return nil, "allowed_models must be an array of model names, or null for every model"
	}
	if len(in) == 0 {
		return nil, "allowed_models must not be empty; use null to allow every model, or revoke the token"
	}
	if len(in) > tokens.MaxAllowedModels {
		return nil, "allowed_models may list at most 200 models"
	}
	seen := map[string]bool{}
	out := []string{}
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > 200 {
			return nil, "each allowed model must be 1 to 200 characters"
		}
		c := catalog.Canonicalize("", m)
		if c == "" {
			c = strings.ToLower(m)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, ""
}

// List: GET /api/tokens. Never includes secrets.
func (h *TokensHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.repo.List(r.Context(), auth.OrgIDFromCtx(r.Context()))
	if err != nil {
		slog.Error("list tokens failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"tokens": list})
}

// Create: POST /api/tokens (owner/admin). The full token is returned only here.
func (h *TokensHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	if !tokens.ValidName(req.Name) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_name", "name must be 1-64 characters of [A-Za-z0-9._-]")
		return
	}
	tok, full, err := h.repo.Create(r.Context(), auth.OrgIDFromCtx(r.Context()), auth.UserIDFromCtx(r.Context()), req.Name)
	switch {
	case errors.Is(err, tokens.ErrNameExists):
		writeError(w, http.StatusConflict, "conflict", "token_name_exists", "A live token with this name already exists")
		return
	case errors.Is(err, tokens.ErrInvalidName):
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_name", "name must be 1-64 characters of [A-Za-z0-9._-]")
		return
	case err != nil:
		slog.Error("create token failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to create token")
		return
	}
	slog.Info("service token created", "token_id", tok.ID, "org_id", auth.OrgIDFromCtx(r.Context()))
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":         tok.ID,
		"name":       tok.Name,
		"token":      full,
		"prefix":     tok.Prefix,
		"created_at": tok.CreatedAt.Format(time.RFC3339),
	})
}

// Revoke: DELETE /api/tokens/{id} (owner/admin). 204; idempotent; 404 for other orgs' tokens.
func (h *TokensHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	err := h.repo.Revoke(r.Context(), auth.OrgIDFromCtx(r.Context()), id, auth.UserIDFromCtx(r.Context()))
	switch {
	case errors.Is(err, tokens.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "token_not_found", "Token not found")
		return
	case err != nil:
		slog.Error("revoke token failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to revoke token")
		return
	}
	slog.Info("service token revoked", "token_id", id, "org_id", auth.OrgIDFromCtx(r.Context()))
	w.WriteHeader(http.StatusNoContent)
}
