package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/keys"
)

// PriorityHandler serves /api/routing/priority and /api/providers.
type PriorityHandler struct {
	store *keys.PriorityStore
	valid func() []string
}

// NewPriorityHandler: store is keys.NewPriorityStore(...) (or KeysHandler.Priority());
// valid returns registered provider ids (providers.ValidProviders).
func NewPriorityHandler(store *keys.PriorityStore, valid func() []string) *PriorityHandler {
	return &PriorityHandler{store: store, valid: valid}
}

type priorityKeyJSON struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Position int    `json:"position"`
}

type priorityProviderJSON struct {
	Provider string            `json:"provider"`
	Position int               `json:"position"`
	Keys     []priorityKeyJSON `json:"keys"`
}

func (h *PriorityHandler) body(r *http.Request, orgID string) (map[string]interface{}, error) {
	view, err := h.store.View(r.Context(), orgID)
	if err != nil {
		return nil, err
	}
	out := make([]priorityProviderJSON, 0, len(view))
	for _, p := range view {
		pj := priorityProviderJSON{Provider: p.Provider, Position: p.Position, Keys: make([]priorityKeyJSON, 0, len(p.Keys))}
		for _, k := range p.Keys {
			pj.Keys = append(pj.Keys, priorityKeyJSON{ID: k.ID, Label: k.Label, Position: k.Position})
		}
		out = append(out, pj)
	}
	return map[string]interface{}{"providers": out}, nil
}

func (h *PriorityHandler) GetPriority(w http.ResponseWriter, r *http.Request) {
	b, err := h.body(r, auth.OrgIDFromCtx(r.Context()))
	if err != nil {
		slog.Error("priority: get failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load priority")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

type putPriorityReq struct {
	Providers []string            `json:"providers"`
	KeyOrder  map[string][]string `json:"key_order"`
}

func (h *PriorityHandler) PutPriority(w http.ResponseWriter, r *http.Request) {
	orgID := auth.OrgIDFromCtx(r.Context())
	var req putPriorityReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	if req.Providers == nil {
		req.Providers = []string{}
	}
	if err := h.store.Replace(r.Context(), orgID, req.Providers, req.KeyOrder); err != nil {
		switch {
		case errors.Is(err, keys.ErrInvalidPriority):
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_priority",
				"providers must be a permutation of the providers returned by GET /api/routing/priority")
		case errors.Is(err, keys.ErrInvalidKeyOrder):
			writeError(w, http.StatusUnprocessableEntity, "validation_error", "invalid_key_order",
				"key_order must be a permutation of that provider's key ids")
		default:
			slog.Error("priority: replace failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to save priority")
		}
		return
	}
	b, err := h.body(r, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load priority")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// ListProviders returns registered adapters for the add-key form.
func (h *PriorityHandler) ListProviders(w http.ResponseWriter, r *http.Request) {
	type pj struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	ids := h.valid()
	out := make([]pj, 0, len(ids))
	for _, id := range ids {
		out = append(out, pj{ID: id, Label: providerLabel(id)})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"providers": out})
}
