package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zer0-router/zer0/internal/catalog"
)

// AliasesHandler serves GET/POST /api/aliases and DELETE /api/aliases/{alias}.
type AliasesHandler struct {
	repo   *catalog.Repository
	orgID  func(*http.Request) string
	userID func(*http.Request) string
}

// NewAliasesHandler builds the handler. orgID and userID extract the caller's
// org and user ids from the request (auth.OrgIDFromCtx / auth.UserIDFromCtx);
// an empty orgID yields 401, userID may return "". Role gating (owner/admin
// on POST and DELETE) is applied by the router via auth.RequireOrgRole.
func NewAliasesHandler(repo *catalog.Repository, orgID, userID func(*http.Request) string) *AliasesHandler {
	return &AliasesHandler{repo: repo, orgID: orgID, userID: userID}
}

func aliasJSON(as []catalog.Alias) []map[string]any {
	out := make([]map[string]any, 0, len(as))
	for _, a := range as {
		out = append(out, map[string]any{"alias": a.Alias, "canonical_model": a.CanonicalModel, "created_at": a.CreatedAt})
	}
	return out
}

// List handles GET /api/aliases.
func (h *AliasesHandler) List(w http.ResponseWriter, r *http.Request) {
	org := h.orgID(r)
	if org == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "authentication required")
		return
	}
	as, err := h.repo.ListAliases(r.Context(), org)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to list aliases")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"aliases": aliasJSON(as)})
}

// Create handles POST /api/aliases (owner/admin; gated by the router).
func (h *AliasesHandler) Create(w http.ResponseWriter, r *http.Request) {
	org := h.orgID(r)
	if org == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "authentication required")
		return
	}
	var body struct {
		Alias          string `json:"alias"`
		CanonicalModel string `json:"canonical_model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", "invalid JSON body")
		return
	}
	uid := ""
	if h.userID != nil {
		uid = h.userID(r)
	}
	a, err := h.repo.CreateAlias(r.Context(), org, body.Alias, body.CanonicalModel, uid)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, aliasJSON([]catalog.Alias{*a})[0])
	case errors.Is(err, catalog.ErrAliasExists):
		writeError(w, http.StatusConflict, "invalid_request_error", "alias_conflict", "alias already exists")
	case errors.Is(err, catalog.ErrAliasShadows):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "alias_shadows_model", "alias must not equal an existing model name")
	case errors.Is(err, catalog.ErrAliasInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request_error", "invalid_alias",
			"alias must be 1-64 chars of a-z, 0-9, '.', '_', ':', '-', must not point at itself or at another alias, and canonical_model is required")
	default:
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to create alias")
	}
}

// Delete handles DELETE /api/aliases/{alias} (owner/admin; gated by the router).
func (h *AliasesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	org := h.orgID(r)
	if org == "" {
		writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_token", "authentication required")
		return
	}
	err := h.repo.DeleteAlias(r.Context(), org, chi.URLParam(r, "alias"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, catalog.ErrAliasNotFound):
		writeError(w, http.StatusNotFound, "invalid_request_error", "alias_not_found", "alias not found")
	default:
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "failed to delete alias")
	}
}
