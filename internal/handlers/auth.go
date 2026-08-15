package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/auth"
)

type AuthHandler struct {
	pool      *pgxpool.Pool
	jwtSecret string
}

func NewAuthHandler(pool *pgxpool.Pool, jwtSecret string) *AuthHandler {
	return &AuthHandler{pool: pool, jwtSecret: jwtSecret}
}

type registerReq struct {
	InviteToken string `json:"invite_token"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" || req.InviteToken == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "missing_fields", "invite_token, email, and password are required")
		return
	}
	if len(req.Password) < 12 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "password_too_short", "Password must be at least 12 characters")
		return
	}

	ctx := r.Context()
	// Validate invite
	var inviteID, inviteEmail string
	var acceptedAt *time.Time
	var expiresAt time.Time
	err := h.pool.QueryRow(ctx,
		`SELECT id, email, accepted_at, expires_at FROM invites WHERE token = $1`,
		req.InviteToken,
	).Scan(&inviteID, &inviteEmail, &acceptedAt, &expiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_invite", "Invalid invite token")
		return
	}
	if acceptedAt != nil {
		writeError(w, http.StatusConflict, "conflict", "invite_used", "Invite already used")
		return
	}
	if time.Now().After(expiresAt) {
		writeError(w, http.StatusGone, "gone", "invite_expired", "Invite has expired")
		return
	}
	_ = inviteEmail

	// Hash password
	pwHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "hash_failed", "Failed to hash password")
		return
	}

	// Generate Zer0 key
	fullKey, prefix, err := auth.GenerateZer0Key()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "keygen_failed", "Failed to generate key")
		return
	}
	keyHash, err := bcrypt.GenerateFromPassword([]byte(fullKey), 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "hash_failed", "Failed to hash key")
		return
	}

	// Create user
	var userID, role, status string
	var createdAt time.Time
	err = h.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, zer0_key_hash, zer0_key_prefix, role, status)
		 VALUES ($1, $2, $3, $4, 'user', 'active')
		 RETURNING id, role, status, created_at`,
		req.Email, string(pwHash), string(keyHash), prefix,
	).Scan(&userID, &role, &status, &createdAt)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusConflict, "conflict", "email_exists", "Email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to create user")
		return
	}

	// Mark invite as used
	h.pool.Exec(ctx, `UPDATE invites SET accepted_at = NOW() WHERE id = $1`, inviteID)

	// Issue JWT
	accessToken, err := auth.IssueAccessToken(h.jwtSecret, userID, req.Email, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "jwt_failed", "Failed to issue token")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"user": map[string]interface{}{
			"id":         userID,
			"email":      req.Email,
			"role":       role,
			"status":     status,
			"created_at": createdAt.Format(time.RFC3339),
		},
		"zer0_key":     fullKey,
		"access_token": accessToken,
	})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	ctx := r.Context()
	var userID, passwordHash, role, status string
	err := h.pool.QueryRow(ctx,
		`SELECT id, password_hash, role, status FROM users WHERE email = $1`,
		req.Email,
	).Scan(&userID, &passwordHash, &role, &status)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "auth_error", "invalid_credentials", "Invalid email or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "auth_error", "invalid_credentials", "Invalid email or password")
		return
	}
	if status == "disabled" {
		writeError(w, http.StatusForbidden, "forbidden", "account_disabled", "Account is disabled")
		return
	}

	accessToken, err := auth.IssueAccessToken(h.jwtSecret, userID, req.Email, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "jwt_failed", "Failed to issue token")
		return
	}
	refreshToken, err := auth.IssueRefreshToken(h.jwtSecret, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "jwt_failed", "Failed to issue refresh token")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    refreshToken,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Path:     "/auth/refresh",
		MaxAge:   7 * 24 * 3600,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"access_token": accessToken,
		"user": map[string]interface{}{
			"id":     userID,
			"email":  req.Email,
			"role":   role,
			"status": status,
		},
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		HttpOnly: true,
		MaxAge:   -1,
		Path:     "/auth/refresh",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "auth_error", "missing_refresh_token", "Missing refresh token")
		return
	}
	userID, err := auth.ValidateRefreshToken(h.jwtSecret, cookie.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "auth_error", "invalid_refresh_token", "Invalid or expired refresh token")
		return
	}
	var email, role string
	err = h.pool.QueryRow(r.Context(), `SELECT email, role FROM users WHERE id = $1`, userID).Scan(&email, &role)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "auth_error", "user_not_found", "User not found")
		return
	}
	accessToken, err := auth.IssueAccessToken(h.jwtSecret, userID, email, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "jwt_failed", "Failed to issue token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"access_token": accessToken})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	var email, role, status, keyPrefix string
	var createdAt time.Time
	err := h.pool.QueryRow(r.Context(),
		`SELECT email, role, status, zer0_key_prefix, created_at FROM users WHERE id = $1`,
		userID,
	).Scan(&email, &role, &status, &keyPrefix, &createdAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to fetch user")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":              userID,
		"email":           email,
		"role":            role,
		"status":          status,
		"zer0_key_prefix": keyPrefix,
		"created_at":      createdAt.Format(time.RFC3339),
	})
}
