package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zer0-router/zer0/internal/auth"
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

// Update: PATCH /api/tokens/{id} (owner/admin) {"monthly_budget_usd": 25 | null}.
// Only live tokens can be updated (404 otherwise, including other orgs' tokens).
func (h *TokensHandler) Update(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	v, ok := raw["monthly_budget_usd"]
	for field := range raw {
		if field != "monthly_budget_usd" {
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "unknown_field", "Unknown field: "+field)
			return
		}
	}
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_fields", "Nothing to update: send monthly_budget_usd")
		return
	}
	budget, msg := parseBudget(v)
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_budget", msg)
		return
	}
	orgID, id := auth.OrgIDFromCtx(r.Context()), chi.URLParam(r, "id")
	switch err := h.repo.SetBudget(r.Context(), orgID, id, budget); {
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
