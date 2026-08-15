package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/routing"
)

type AdminHandler struct {
	pgPool      *pgxpool.Pool
	routingPool *routing.Pool
}

func NewAdminHandler(pg *pgxpool.Pool, routingPool *routing.Pool) *AdminHandler {
	return &AdminHandler{pgPool: pg, routingPool: routingPool}
}

func (h *AdminHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	adminID := auth.UserIDFromCtx(r.Context())
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_request", "email is required")
		return
	}

	var token string
	var expiresAt time.Time
	err := h.pgPool.QueryRow(r.Context(),
		`INSERT INTO invites (email, token, created_by, invited_by, expires_at)
		 VALUES ($1, gen_random_uuid()::text, $2, $2, NOW() + INTERVAL '7 days')
		 RETURNING token, expires_at`,
		req.Email, adminID,
	).Scan(&token, &expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to create invite")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"token":      token,
		"email":      req.Email,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pgPool.Query(r.Context(),
		`SELECT id, email, role, status, created_at FROM users ORDER BY created_at DESC`,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to list users")
		return
	}
	defer rows.Close()

	type userJSON struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		Role      string `json:"role"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
	}
	var users []userJSON
	for rows.Next() {
		var u userJSON
		var createdAt time.Time
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.Status, &createdAt); err != nil {
			continue
		}
		u.CreatedAt = createdAt.Format(time.RFC3339)
		users = append(users, u)
	}
	if users == nil {
		users = []userJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})
}

func (h *AdminHandler) GetPoolForUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "user_id")
	active, common, err := h.routingPool.LoadPool(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load pool")
		return
	}
	type comboJSON struct {
		Provider      string `json:"provider"`
		Model         string `json:"model"`
		ProviderKeyID string `json:"provider_key_id"`
		Pool          string `json:"pool"`
	}
	toJSON := func(combos []routing.Combo) []comboJSON {
		out := make([]comboJSON, 0, len(combos))
		for _, c := range combos {
			out = append(out, comboJSON{c.Provider, c.Model, c.ProviderKeyID, c.Pool})
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active": toJSON(active),
		"common": toJSON(common),
	})
}
