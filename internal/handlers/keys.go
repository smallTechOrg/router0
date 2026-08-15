package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
)

type KeysHandler struct {
	keyRepo  *keys.Repository
	pool     *routing.Pool
	pgPool   *pgxpool.Pool
}

func NewKeysHandler(pg *pgxpool.Pool, routingPool *routing.Pool) *KeysHandler {
	return &KeysHandler{
		keyRepo: keys.NewRepository(pg),
		pool:    routingPool,
		pgPool:  pg,
	}
}

type addKeyReq struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	ModelID  string `json:"model_id"`
}

func (h *KeysHandler) AddKey(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	var req addKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	if req.Key == "" || req.Provider == "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_fields", "provider and key are required")
		return
	}

	valid := false
	for _, p := range providers.ValidProviders() {
		if p == req.Provider {
			valid = true
			break
		}
	}
	if !valid {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_provider", "Invalid provider")
		return
	}
	if req.Provider == "baseten" && req.ModelID == "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "missing_model_id", "model_id is required for baseten")
		return
	}

	hint := req.Key
	if len(hint) > 8 {
		hint = hint[:8]
	}
	hint += "…"

	if req.Label == "" {
		req.Label = req.Provider + " key"
	}

	ciphertext, nonce, err := keys.Encrypt(req.Key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "encrypt_failed", "Failed to encrypt key")
		return
	}

	k, err := h.keyRepo.Create(r.Context(), userID, req.Provider, req.Label, hint, req.ModelID, ciphertext, nonce)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusConflict, "conflict", "key_exists", "A key with this provider and label already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to save key")
		return
	}

	// Add combos to pool for the provider's default models
	prov, _ := providers.Get(req.Provider)
	if prov != nil {
		ctx := context.Background()
		for _, model := range prov.DefaultModels() {
			_ = h.pool.AddCombo(ctx, userID, req.Provider, model, k.ID)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":           k.ID,
		"provider":     k.Provider,
		"label":        k.Label,
		"key_hint":     k.KeyHint,
		"status":       k.Status,
		"created_at":   k.CreatedAt.Format(time.RFC3339),
	})
}

func (h *KeysHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	ks, err := h.keyRepo.ListByUser(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list keys")
		return
	}

	type keyJSON struct {
		ID             string  `json:"id"`
		Provider       string  `json:"provider"`
		Label          string  `json:"label"`
		KeyHint        string  `json:"key_hint"`
		Status         string  `json:"status"`
		LastTestedAt   *string `json:"last_tested_at"`
		LastTestResult *string `json:"last_test_result"`
		CreatedAt      string  `json:"created_at"`
	}
	result := make([]keyJSON, 0, len(ks))
	for _, k := range ks {
		kj := keyJSON{
			ID:        k.ID,
			Provider:  k.Provider,
			Label:     k.Label,
			KeyHint:   k.KeyHint,
			Status:    k.Status,
			CreatedAt: k.CreatedAt.Format(time.RFC3339),
		}
		if k.LastTestedAt != nil {
			s := k.LastTestedAt.Format(time.RFC3339)
			kj.LastTestedAt = &s
		}
		if k.LastTestResult != "" {
			kj.LastTestResult = &k.LastTestResult
		}
		result = append(result, kj)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"keys": result})
}

func (h *KeysHandler) DeleteKey(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	keyID := chi.URLParam(r, "id")

	k, err := h.keyRepo.GetByID(r.Context(), keyID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
		return
	}

	if err := h.keyRepo.Disable(r.Context(), keyID, userID); err != nil {
		if errors.Is(err, keys.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to delete key")
		return
	}

	// Remove from pool
	prov, _ := providers.Get(k.Provider)
	if prov != nil {
		for _, model := range prov.DefaultModels() {
			_ = h.pool.RemoveCombo(r.Context(), userID, k.Provider, model, keyID)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *KeysHandler) TestKey(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	keyID := chi.URLParam(r, "id")

	k, err := h.keyRepo.GetByID(r.Context(), keyID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "key_not_found", "Key not found")
		return
	}

	decrypted, err := keys.Decrypt(k.Ciphertext, k.Nonce)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "decrypt_failed", "Failed to decrypt key")
		return
	}

	prov, err := providers.Get(k.Provider)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "unknown_provider", "Unknown provider")
		return
	}

	result, err := prov.HealthCheck(r.Context(), decrypted)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "error", "message": err.Error()})
		return
	}

	testResult := "ok"
	if !result.OK {
		testResult = "error:" + result.Error
	}
	_ = h.keyRepo.UpdateTestResult(r.Context(), keyID, testResult)

	if result.OK {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "latency_ms": result.LatencyMs})
	} else {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "error", "message": result.Error})
	}
}
