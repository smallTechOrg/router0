package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const (
	ContextUserID    contextKey = "user_id"
	ContextUserRole  contextKey = "user_role"
	ContextUserEmail contextKey = "user_email"
)

type User struct {
	ID     string
	Email  string
	Role   string
	Status string
}

// Zer0KeyMiddleware authenticates requests using a zer0:sk-... key.
func Zer0KeyMiddleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer zer0:sk-") {
				writeError(w, http.StatusUnauthorized, "auth_error", "missing_zer0_key", "Missing or invalid Zer0 API key")
				return
			}
			fullKey := strings.TrimPrefix(authHeader, "Bearer ")
			// fullKey = "zer0:sk-<32 chars>", prefix is chars 8..19 (12 chars after "zer0:sk-")
			if len(fullKey) < 20 {
				writeError(w, http.StatusUnauthorized, "auth_error", "invalid_zer0_key", "Invalid Zer0 API key format")
				return
			}
			suffix := fullKey[8:] // after "zer0:sk-"
			if len(suffix) < 12 {
				writeError(w, http.StatusUnauthorized, "auth_error", "invalid_zer0_key", "Invalid Zer0 API key format")
				return
			}
			prefix := suffix[:12]

			var userID, email, role, status, keyHash string
			err := pool.QueryRow(r.Context(),
				`SELECT id, email, role, status, zer0_key_hash FROM users WHERE zer0_key_prefix = $1 AND status != 'disabled'`,
				prefix,
			).Scan(&userID, &email, &role, &status, &keyHash)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "auth_error", "invalid_zer0_key", "Invalid Zer0 API key")
				return
			}
			if err := bcrypt.CompareHashAndPassword([]byte(keyHash), []byte(fullKey)); err != nil {
				writeError(w, http.StatusUnauthorized, "auth_error", "invalid_zer0_key", "Invalid Zer0 API key")
				return
			}
			if status == "disabled" {
				writeError(w, http.StatusForbidden, "forbidden", "account_disabled", "Account is disabled")
				return
			}
			ctx := context.WithValue(r.Context(), ContextUserID, userID)
			ctx = context.WithValue(ctx, ContextUserRole, role)
			ctx = context.WithValue(ctx, ContextUserEmail, email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// JWTMiddleware authenticates requests using a JWT access token.
func JWTMiddleware(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "auth_error", "missing_token", "Missing access token")
				return
			}
			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err := ValidateAccessToken(jwtSecret, tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "auth_error", "invalid_token", "Invalid or expired access token")
				return
			}
			ctx := context.WithValue(r.Context(), ContextUserID, claims.UserID)
			ctx = context.WithValue(ctx, ContextUserRole, claims.Role)
			ctx = context.WithValue(ctx, ContextUserEmail, claims.Email)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AdminOnly requires role=admin.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(ContextUserRole).(string)
		if role != "admin" {
			writeError(w, http.StatusForbidden, "forbidden", "admin_required", "Admin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func UserIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ContextUserID).(string)
	return v
}

func UserRoleFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ContextUserRole).(string)
	return v
}

func writeError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}
