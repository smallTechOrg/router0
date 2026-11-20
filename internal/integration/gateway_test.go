//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/db"
	"github.com/zer0-router/zer0/internal/handlers"
	"github.com/zer0-router/zer0/internal/routing"
)

func buildRouter(t *testing.T, pool *pgxpool.Pool, redisAddr string) http.Handler {
	t.Helper()
	jwtSecret := mustTestEnv(t, "JWT_SECRET")

	rdb := db.NewClient(redisAddr, "", 1) // use db 1 for tests
	t.Cleanup(func() { rdb.Close() })

	routingPool := routing.NewPool(rdb)
	authHandler := handlers.NewAuthHandler(pool, jwtSecret)
	keysHandler := handlers.NewKeysHandler(pool, routingPool)
	poolHandler := handlers.NewPoolHandler(pool, routingPool)
	adminHandler := handlers.NewAdminHandler(pool, routingPool)
	inferenceHandler := handlers.NewInferenceHandler(pool, rdb)

	jwtMiddleware := auth.JWTMiddleware(jwtSecret)
	zer0KeyMiddleware := auth.Zer0KeyMiddleware(pool)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	r.Post("/auth/register", authHandler.Register)
	r.Post("/auth/login", authHandler.Login)
	r.With(jwtMiddleware).Get("/auth/me", authHandler.Me)

	r.With(zer0KeyMiddleware).Post("/v1/chat/completions", inferenceHandler.ChatCompletions)

	r.Group(func(r chi.Router) {
		r.Use(jwtMiddleware)
		r.Get("/api/keys", keysHandler.ListKeys)
		r.Post("/api/keys", keysHandler.AddKey)
		r.Get("/api/pool", poolHandler.GetPool)
		r.Get("/api/requests", poolHandler.GetRequests)
	})
	r.Group(func(r chi.Router) {
		r.Use(jwtMiddleware)
		r.Use(auth.AdminOnly)
		r.Post("/admin/invites", adminHandler.CreateInvite)
		r.Get("/admin/pool/{user_id}", adminHandler.GetPoolForUser)
	})

	return r
}

func TestGatewayEndToEnd(t *testing.T) {
	groqKey := os.Getenv("TEST_GROQ_KEY")
	if groqKey == "" {
		t.Skip("TEST_GROQ_KEY not set — skipping integration test")
	}

	databaseURL := mustTestEnv(t, "DATABASE_URL")
	jwtSecret := mustTestEnv(t, "JWT_SECRET")

	ctx := context.Background()

	// Setup DB
	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		t.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	// Run migrations
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	if err := db.RunMigrations(databaseURL, migrationsPath); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	// Clean up test data
	testEmail := fmt.Sprintf("test-%d@example.com", time.Now().UnixNano())
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, testEmail)
	})

	// Bootstrap admin user directly
	adminEmail := fmt.Sprintf("admin-%d@example.com", time.Now().UnixNano())
	var adminID string
	err = pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, zer0_key_hash, zer0_key_prefix, role, status)
		 VALUES ($1, '$2a$12$dummy', '$2a$12$dummy', 'dummyprefix', 'admin', 'active')
		 RETURNING id`,
		adminEmail,
	).Scan(&adminID)
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, adminID) })

	adminToken, err := auth.IssueAccessToken(jwtSecret, adminID, adminEmail, "admin")
	if err != nil {
		t.Fatalf("issue admin token: %v", err)
	}

	// Start test server
	srv := httptest.NewServer(buildRouter(t, pool, "localhost:6379"))
	defer srv.Close()
	base := srv.URL

	do := func(method, path, body, bearerToken string) *http.Response {
		t.Helper()
		var bodyR io.Reader
		if body != "" {
			bodyR = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, base+path, bodyR)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+bearerToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// Step 3: POST /admin/invites → get invite token
	inviteResp := do("POST", "/admin/invites", `{"email":"`+testEmail+`"}`, adminToken)
	if inviteResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(inviteResp.Body)
		t.Fatalf("POST /admin/invites: got %d, body=%s", inviteResp.StatusCode, string(b))
	}
	var inviteBody struct {
		Token string `json:"token"`
	}
	json.NewDecoder(inviteResp.Body).Decode(&inviteBody)
	inviteResp.Body.Close()
	t.Logf("invite token: %s", inviteBody.Token)

	// Step 4: POST /auth/register
	regPayload := fmt.Sprintf(`{"invite_token":"%s","email":"%s","password":"testpassword123"}`, inviteBody.Token, testEmail)
	regResp := do("POST", "/auth/register", regPayload, "")
	if regResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(regResp.Body)
		t.Fatalf("POST /auth/register: got %d, body=%s", regResp.StatusCode, string(b))
	}
	var regBody struct {
		Zer0Key     string `json:"zer0_key"`
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(regResp.Body).Decode(&regBody)
	regResp.Body.Close()
	if regBody.Zer0Key == "" {
		t.Fatal("expected zer0_key in register response")
	}
	if !strings.HasPrefix(regBody.Zer0Key, "zer0:sk-") {
		t.Fatalf("zer0_key has wrong format: %s", regBody.Zer0Key)
	}
	t.Logf("zer0_key: %s", regBody.Zer0Key[:12]+"...")
	userToken := regBody.AccessToken

	// Step 5: POST /api/keys with TEST_GROQ_KEY
	keyPayload := fmt.Sprintf(`{"provider":"groq","key":"%s","label":"Test Groq"}`, groqKey)
	keyResp := do("POST", "/api/keys", keyPayload, userToken)
	if keyResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(keyResp.Body)
		t.Fatalf("POST /api/keys: got %d, body=%s", keyResp.StatusCode, string(b))
	}
	var keyBody struct {
		ID string `json:"id"`
	}
	json.NewDecoder(keyResp.Body).Decode(&keyBody)
	keyResp.Body.Close()
	t.Logf("key id: %s", keyBody.ID)

	// Step 6: POST /v1/chat/completions with stream:true
	streamPayload := `{"model":"llama-3.1-70b-versatile","messages":[{"role":"user","content":"count to 3"}],"stream":true}`
	streamReq, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(streamPayload))
	streamReq.Header.Set("Content-Type", "application/json")
	streamReq.Header.Set("Authorization", "Bearer "+regBody.Zer0Key)

	// Need a client with a longer timeout
	client := &http.Client{Timeout: 60 * time.Second}
	streamResp, err := client.Do(streamReq)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("POST /v1/chat/completions: got %d, body=%s", streamResp.StatusCode, string(b))
	}

	providerHeader := streamResp.Header.Get("X-Zer0-Provider")
	if providerHeader != "groq" {
		t.Fatalf("expected X-Zer0-Provider: groq, got: %s", providerHeader)
	}
	latencyHeader := streamResp.Header.Get("X-Zer0-Latency-Ms")
	if latencyHeader == "" || latencyHeader == "0" {
		t.Fatalf("expected X-Zer0-Latency-Ms > 0, got: %s", latencyHeader)
	}

	// Count SSE chunks
	chunkCount := 0
	scanner := bufio.NewScanner(streamResp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			chunkCount++
		}
	}
	if chunkCount < 3 {
		t.Fatalf("expected ≥3 SSE chunks, got %d", chunkCount)
	}
	t.Logf("SSE chunks received: %d", chunkCount)

	// Wait a moment for async metadata recording
	time.Sleep(500 * time.Millisecond)

	// Step 7: GET /api/requests
	reqsResp := do("GET", "/api/requests", "", userToken)
	if reqsResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(reqsResp.Body)
		t.Fatalf("GET /api/requests: got %d, body=%s", reqsResp.StatusCode, string(b))
	}
	var reqsBody struct {
		Requests []struct {
			InputTokens int    `json:"input_tokens"`
			Status      string `json:"status"`
		} `json:"requests"`
	}
	json.NewDecoder(reqsResp.Body).Decode(&reqsBody)
	reqsResp.Body.Close()
	if len(reqsBody.Requests) == 0 {
		t.Fatal("expected at least one request in feed")
	}
	if reqsBody.Requests[0].Status != "success" {
		t.Fatalf("expected status=success, got %s", reqsBody.Requests[0].Status)
	}
	if reqsBody.Requests[0].InputTokens <= 0 {
		t.Fatalf("expected input_tokens > 0, got %d", reqsBody.Requests[0].InputTokens)
	}

	// Step 8: GET /metrics
	metricsResp := do("GET", "/metrics", "", "")
	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: got %d", metricsResp.StatusCode)
	}
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	metricsResp.Body.Close()
	if !strings.Contains(string(metricsBody), "zer0_requests_total") {
		t.Fatal("expected zer0_requests_total in metrics")
	}
	t.Log("All integration assertions passed!")
}

func mustTestEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("env var %s not set", key)
	}
	return v
}

// ensure net and bytes are used
var _ = net.Listen
var _ = bytes.NewReader
