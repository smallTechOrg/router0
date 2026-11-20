package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeVerifier struct{ err error }

func (f fakeVerifier) Verify(_ context.Context, raw string) (Principal, error) {
	if f.err != nil {
		return Principal{}, f.err
	}
	return Principal{OrgID: "o1", TokenID: "t1", TokenName: raw}, nil
}

func TestServiceTokenMiddleware(t *testing.T) {
	var got Principal
	h := ServiceTokenMiddleware(fakeVerifier{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = PrincipalFromCtx(r.Context())
		if OrgIDFromCtx(r.Context()) != "o1" {
			t.Error("org id not set")
		}
	}))
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer abc")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got.TokenID != "t1" || got.TokenName != "abc" {
		t.Fatalf("principal not propagated: %+v", got)
	}

	for _, c := range []struct {
		name   string
		err    error
		header string
		status int
		code   string
	}{
		{"missing", nil, "", 401, "invalid_api_key"},
		{"invalid", ErrInvalidToken, "Bearer x", 401, "invalid_api_key"},
		{"revoked", ErrRevokedToken, "Bearer x", 401, "invalid_api_key"},
		{"db down", errors.New("boom"), "Bearer x", 503, "auth_unavailable"},
	} {
		h := ServiceTokenMiddleware(fakeVerifier{err: c.err})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("next must not be called")
		}))
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"code":"`+c.code+`"`) {
			t.Errorf("%s: got %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
}

type fakeLookup struct {
	org, role string
	err       error
}

func (f fakeLookup) OrgForUser(context.Context, string) (string, string, error) {
	return f.org, f.role, f.err
}

func TestJWTMiddlewareAndRequireOrgRole(t *testing.T) {
	const secret = "s3cret-s3cret-s3cret"
	tok, _ := IssueAccessToken(secret, "u1", "a@b.co", "user", "")
	run := func(l OrgLookup, wrap func(http.Handler) http.Handler) *httptest.ResponseRecorder {
		var inner http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if OrgIDFromCtx(r.Context()) != "org1" {
				t.Error("org not in ctx")
			}
		})
		if wrap != nil {
			inner = wrap(inner)
		}
		req := httptest.NewRequest("GET", "/api/tokens", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		JWTMiddleware(secret, l)(inner).ServeHTTP(rec, req)
		return rec
	}
	// old JWT without org_id: org resolved from membership
	if rec := run(fakeLookup{org: "org1", role: "owner"}, RequireOrgRole("owner", "admin")); rec.Code != 200 {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body)
	}
	if rec := run(fakeLookup{org: "org1", role: "member"}, RequireOrgRole("owner", "admin")); rec.Code != 403 || !strings.Contains(rec.Body.String(), "org_role_required") {
		t.Fatalf("member: %d %s", rec.Code, rec.Body)
	}
	if rec := run(fakeLookup{err: ErrNoOrg}, nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "no_org") {
		t.Fatalf("no org: %d %s", rec.Code, rec.Body)
	}
	if rec := run(fakeLookup{err: ErrUserDisabled}, nil); rec.Code != 403 || !strings.Contains(rec.Body.String(), "account_disabled") {
		t.Fatalf("disabled: %d %s", rec.Code, rec.Body)
	}
}

func TestJWTOrgClaimRoundTrip(t *testing.T) {
	tok, err := IssueAccessToken("k", "u1", "e@x.co", "user", "org-9")
	if err != nil {
		t.Fatal(err)
	}
	c, err := ValidateAccessToken("k", tok)
	if err != nil || c.OrgID != "org-9" || c.UserID != "u1" || c.Subject != "u1" {
		t.Fatalf("claims: %+v err=%v", c, err)
	}
}
