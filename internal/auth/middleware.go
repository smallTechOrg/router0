package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

type contextKey string

const (
	ContextUserID    contextKey = "user_id"
	ContextUserRole  contextKey = "user_role"
	ContextUserEmail contextKey = "user_email"
	ContextOrgID     contextKey = "org_id"
	ContextOrgRole   contextKey = "org_role"
	contextPrincipal contextKey = "principal"
)

// Sentinel errors shared between auth, tokens and orgs (kept here so that
// auth never imports its implementers). tokens.ErrInvalid / tokens.ErrRevoked
// and orgs.ErrNoOrg / orgs.ErrUserDisabled are the same values.
var (
	ErrInvalidToken = errors.New("invalid router0 token")
	ErrRevokedToken = errors.New("revoked router0 token")
	ErrNoOrg        = errors.New("user has no org")
	ErrUserDisabled = errors.New("user disabled")
)

// Principal is the authenticated caller of an inference (/v1) request.
type Principal struct{ OrgID, TokenID, TokenName string }

// TokenVerifier verifies a raw service token. It returns ErrInvalidToken or
// ErrRevokedToken (tokens.ErrInvalid / tokens.ErrRevoked) for bad tokens;
// any other error is treated as an infrastructure failure (503).
type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (Principal, error)
}

// OrgLookup resolves a user's org and org role (implemented by orgs.Repository).
type OrgLookup interface {
	OrgForUser(ctx context.Context, userID string) (orgID, role string, err error)
}

type User struct {
	ID     string
	Email  string
	Role   string
	Status string
}

// ServiceTokenMiddleware authenticates /v1/* requests with an r0-sk- (or legacy
// zer0:sk-) bearer token and stores the Principal in the request context.
// Failures use the OpenAI error shape; unknown, malformed and revoked tokens
// get the same response.
func ServiceTokenMiddleware(v TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Invalid or revoked router0 token")
				return
			}
			p, err := v.Verify(r.Context(), raw)
			if err != nil {
				if errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrRevokedToken) {
					writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "Invalid or revoked router0 token")
					return
				}
				slog.Error("token verification unavailable", "error", err)
				writeOpenAIError(w, http.StatusServiceUnavailable, "upstream_error", "auth_unavailable", "Authentication is temporarily unavailable")
				return
			}
			ctx := context.WithValue(r.Context(), contextPrincipal, p)
			ctx = context.WithValue(ctx, ContextOrgID, p.OrgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PrincipalFromCtx returns the principal set by ServiceTokenMiddleware.
func PrincipalFromCtx(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextPrincipal).(Principal)
	return p, ok
}

// OrgIDFromCtx returns the caller's org id (set by JWTMiddleware or ServiceTokenMiddleware).
func OrgIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ContextOrgID).(string)
	return v
}

// OrgRoleFromCtx returns the caller's org role (owner|admin|member), set by JWTMiddleware.
func OrgRoleFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ContextOrgRole).(string)
	return v
}

// JWTMiddleware authenticates dashboard requests with a JWT access token and
// puts user id, email, platform role, org id and org role in the context.
// The org is always resolved through lookup (so role changes and disabled
// users take effect immediately); the org_id claim is only used when lookup is nil.
func JWTMiddleware(jwtSecret string, lookup OrgLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "auth_error", "missing_token", "Missing access token")
				return
			}
			claims, err := ValidateAccessToken(jwtSecret, strings.TrimPrefix(authHeader, "Bearer "))
			if err != nil {
				writeError(w, http.StatusUnauthorized, "auth_error", "invalid_token", "Invalid or expired access token")
				return
			}
			orgID, orgRole := claims.OrgID, ""
			if lookup != nil {
				orgID, orgRole, err = lookup.OrgForUser(r.Context(), claims.UserID)
				switch {
				case errors.Is(err, ErrNoOrg):
					writeError(w, http.StatusForbidden, "forbidden", "no_org", "User does not belong to an org")
					return
				case errors.Is(err, ErrUserDisabled):
					writeError(w, http.StatusForbidden, "forbidden", "account_disabled", "Account is disabled")
					return
				case err != nil:
					slog.Error("org lookup failed", "error", err)
					writeError(w, http.StatusInternalServerError, "internal_error", "db_error", "Failed to resolve org")
					return
				}
			}
			if orgID == "" {
				writeError(w, http.StatusForbidden, "forbidden", "no_org", "User does not belong to an org")
				return
			}
			ctx := context.WithValue(r.Context(), ContextUserID, claims.UserID)
			ctx = context.WithValue(ctx, ContextUserRole, claims.Role)
			ctx = context.WithValue(ctx, ContextUserEmail, claims.Email)
			ctx = context.WithValue(ctx, ContextOrgID, orgID)
			ctx = context.WithValue(ctx, ContextOrgRole, orgRole)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireOrgRole allows only callers whose org role is one of roles
// (e.g. RequireOrgRole("owner","admin") for write routes). Must run after JWTMiddleware.
func RequireOrgRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := allowed[OrgRoleFromCtx(r.Context())]; !ok {
				writeError(w, http.StatusForbidden, "forbidden", "org_role_required", "Your org role does not permit this action")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AdminOnly requires platform role=admin.
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

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 8 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	t := strings.TrimSpace(h[7:])
	return t, t != ""
}

func writeError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"message": message, "type": errType, "code": code},
	})
}

func writeOpenAIError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{"message": message, "type": errType, "param": nil, "code": code},
	})
}
