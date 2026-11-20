package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/orgs"
)

type AuthHandler struct {
	pool      *pgxpool.Pool
	orgs      *orgs.Repository
	jwtSecret string
}

func NewAuthHandler(pool *pgxpool.Pool, jwtSecret string) *AuthHandler {
	return &AuthHandler{pool: pool, orgs: orgs.NewRepository(pool), jwtSecret: jwtSecret}
}

type registerReq struct {
	InviteToken string `json:"invite_token"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	OrgName     string `json:"org_name"`
}

type orgJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// regError aborts registration with an HTTP error (the transaction is rolled back).
type regError struct {
	status         int
	typ, code, msg string
}

func (e *regError) Error() string { return e.code }

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.OrgName = strings.TrimSpace(req.OrgName)
	if req.Email == "" || req.Password == "" || req.InviteToken == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "missing_fields", "invite_token, email, and password are required")
		return
	}
	if len(req.Password) < 12 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "password_too_short", "Password must be at least 12 characters")
		return
	}
	if utf8.RuneCountInString(req.OrgName) > 80 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "org_name_too_long", "org_name must be at most 80 characters")
		return
	}

	pwHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "hash_failed", "Failed to hash password")
		return
	}

	ctx := r.Context()
	var userID, role, status string
	var createdAt time.Time
	var org orgJSON

	// User, org, membership and invite consumption are one transaction.
	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		var inviteID string
		var inviteOrgID, inviteOrgRole *string
		var acceptedAt, usedAt *time.Time
		var expiresAt time.Time
		err := tx.QueryRow(ctx,
			`SELECT id::text, org_id::text, org_role, accepted_at, used_at, expires_at
			   FROM invites WHERE token = $1 FOR UPDATE`,
			req.InviteToken,
		).Scan(&inviteID, &inviteOrgID, &inviteOrgRole, &acceptedAt, &usedAt, &expiresAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &regError{http.StatusBadRequest, "validation_error", "invalid_invite", "Invalid invite token"}
			}
			return err
		}
		if acceptedAt != nil || usedAt != nil {
			return &regError{http.StatusConflict, "conflict", "invite_used", "Invite already used"}
		}
		if time.Now().After(expiresAt) {
			return &regError{http.StatusGone, "gone", "invite_expired", "Invite has expired"}
		}

		err = tx.QueryRow(ctx,
			`INSERT INTO users (email, password_hash, role, status)
			 VALUES ($1, $2, 'user', 'active')
			 RETURNING id::text, role, status, created_at`,
			req.Email, string(pwHash),
		).Scan(&userID, &role, &status, &createdAt)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return &regError{http.StatusConflict, "conflict", "email_exists", "Email already registered"}
			}
			return err
		}

		if inviteOrgID == nil {
			name := req.OrgName
			if name == "" {
				name = req.Email
			}
			orgID, err := orgs.CreateWithOwner(ctx, tx, name, userID)
			if err != nil {
				return err
			}
			org = orgJSON{ID: orgID, Name: name, Role: orgs.RoleOwner}
		} else {
			memberRole := orgs.RoleMember
			if inviteOrgRole != nil && *inviteOrgRole != "" {
				memberRole = *inviteOrgRole
			}
			if err := orgs.AddMember(ctx, tx, *inviteOrgID, userID, memberRole); err != nil {
				return err
			}
			org = orgJSON{ID: *inviteOrgID, Role: memberRole}
			if err := tx.QueryRow(ctx, `SELECT name FROM orgs WHERE id = $1`, *inviteOrgID).Scan(&org.Name); err != nil {
				return err
			}
		}

		_, err = tx.Exec(ctx, `UPDATE invites SET accepted_at = NOW(), used_at = NOW(), used_by = $2 WHERE id = $1`, inviteID, userID)
		return err
	})
	if err != nil {
		var re *regError
		if errors.As(err, &re) {
			writeError(w, re.status, re.typ, re.code, re.msg)
			return
		}
		slog.Error("register failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to create user")
		return
	}

	accessToken, err := auth.IssueAccessToken(h.jwtSecret, userID, req.Email, role, org.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "jwt_failed", "Failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user": map[string]interface{}{
			"id":         userID,
			"email":      req.Email,
			"role":       role,
			"status":     status,
			"created_at": createdAt.Format(time.RFC3339),
		},
		"org":          org,
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

	var orgOut *orgJSON
	orgID := ""
	if o, err := h.orgs.GetForUser(ctx, userID); err == nil {
		orgOut = &orgJSON{ID: o.ID, Name: o.Name, Role: o.Role}
		orgID = o.ID
	} else if !errors.Is(err, orgs.ErrNoOrg) {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load org")
		return
	}

	accessToken, err := auth.IssueAccessToken(h.jwtSecret, userID, req.Email, role, orgID)
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
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"user": map[string]interface{}{
			"id":     userID,
			"email":  req.Email,
			"role":   role,
			"status": status,
		},
		"org": orgOut,
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
	var email, role, status string
	err = h.pool.QueryRow(r.Context(), `SELECT email, role, status FROM users WHERE id = $1`, userID).Scan(&email, &role, &status)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "auth_error", "user_not_found", "User not found")
		return
	}
	if status == "disabled" {
		writeError(w, http.StatusForbidden, "forbidden", "account_disabled", "Account is disabled")
		return
	}
	orgID := ""
	if o, err := h.orgs.GetForUser(r.Context(), userID); err == nil {
		orgID = o.ID
	}
	accessToken, err := auth.IssueAccessToken(h.jwtSecret, userID, email, role, orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "jwt_failed", "Failed to issue token")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"access_token": accessToken})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromCtx(r.Context())
	var email, role, status string
	err := h.pool.QueryRow(r.Context(),
		`SELECT email, role, status FROM users WHERE id = $1`, userID,
	).Scan(&email, &role, &status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to fetch user")
		return
	}
	var orgOut *orgJSON
	if o, err := h.orgs.GetForUser(r.Context(), userID); err == nil {
		orgOut = &orgJSON{ID: o.ID, Name: o.Name, Role: o.Role}
	} else if !errors.Is(err, orgs.ErrNoOrg) {
		writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to load org")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":     userID,
		"email":  email,
		"role":   role,
		"status": status,
		"org":    orgOut,
	})
}
