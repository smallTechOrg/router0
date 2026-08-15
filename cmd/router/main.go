package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/db"
	"github.com/zer0-router/zer0/internal/handlers"
	"github.com/zer0-router/zer0/internal/health"
	"github.com/zer0-router/zer0/internal/routing"
)

func main() {
	// ── Structured logger ────────────────────────────────────────────────────
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	logHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	slog.SetDefault(slog.New(logHandler))

	// ── Config from env ──────────────────────────────────────────────────────
	port := envOrDefault("PORT", "8080")
	databaseURL := mustEnv("DATABASE_URL")
	redisURL := mustEnv("REDIS_URL")
	jwtSecret := mustEnv("JWT_SECRET")

	// ── Migrations path ──────────────────────────────────────────────────────
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	migrationsPath := filepath.Join(repoRoot, "migrations")

	// ── PostgreSQL ───────────────────────────────────────────────────────────
	ctx := context.Background()
	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("postgres: connect failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("postgres: connected")

	// ── Run migrations ───────────────────────────────────────────────────────
	if err := db.RunMigrations(databaseURL, migrationsPath); err != nil {
		slog.Error("migrations: failed", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations: up to date")

	// ── Redis ────────────────────────────────────────────────────────────────
	redisAddr, redisPassword, redisDB := parseRedisURL(redisURL)
	redisClient := db.NewClient(redisAddr, redisPassword, redisDB)
	defer redisClient.Close()

	if err := db.Ping(ctx, redisClient); err != nil {
		slog.Error("redis: connect failed", "error", err)
		os.Exit(1)
	}
	slog.Info("redis: connected")

	// ── Routing pool ─────────────────────────────────────────────────────────
	routingPool := routing.NewPool(redisClient)

	// ── Health scheduler ─────────────────────────────────────────────────────
	scheduler := health.NewScheduler(redisClient, pool, 4)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	// ── Handlers ─────────────────────────────────────────────────────────────
	authHandler := handlers.NewAuthHandler(pool, jwtSecret)
	keysHandler := handlers.NewKeysHandler(pool, routingPool)
	poolHandler := handlers.NewPoolHandler(pool, routingPool)
	adminHandler := handlers.NewAdminHandler(pool, routingPool)
	inferenceHandler := handlers.NewInferenceHandler(pool, redisClient)

	jwtMiddleware := auth.JWTMiddleware(jwtSecret)
	zer0KeyMiddleware := auth.Zer0KeyMiddleware(pool)

	// ── Router ───────────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", healthzHandler(pool, redisClient))
	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	// Auth routes
	r.Post("/auth/register", authHandler.Register)
	r.Post("/auth/login", authHandler.Login)
	r.Post("/auth/logout", authHandler.Logout)
	r.Post("/auth/refresh", authHandler.Refresh)
	r.With(jwtMiddleware).Get("/auth/me", authHandler.Me)

	// Inference (Zer0 key auth)
	r.With(zer0KeyMiddleware).Post("/v1/chat/completions", inferenceHandler.ChatCompletions)

	// API routes (JWT auth)
	r.Group(func(r chi.Router) {
		r.Use(jwtMiddleware)
		r.Get("/api/keys", keysHandler.ListKeys)
		r.Post("/api/keys", keysHandler.AddKey)
		r.Delete("/api/keys/{id}", keysHandler.DeleteKey)
		r.Post("/api/keys/{id}/test", keysHandler.TestKey)
		r.Get("/api/pool", poolHandler.GetPool)
		r.Get("/api/requests", poolHandler.GetRequests)
		r.Get("/api/costs", poolHandler.GetCosts)
	})

	// Admin routes (JWT auth + admin role)
	r.Group(func(r chi.Router) {
		r.Use(jwtMiddleware)
		r.Use(auth.AdminOnly)
		r.Post("/admin/invites", adminHandler.CreateInvite)
		r.Get("/admin/users", adminHandler.ListUsers)
		r.Get("/admin/pool/{user_id}", adminHandler.GetPoolForUser)
	})

	// ── HTTP server ──────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		slog.Info("server: starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server: listen error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("server: shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server: shutdown error", "error", err)
	}
	slog.Info("server: stopped")
}

// healthzHandler returns a handler that pings PostgreSQL and Redis and reports their status.
func healthzHandler(pool *pgxpool.Pool, redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type healthResponse struct {
			Status string `json:"status"`
			DB     string `json:"db"`
			Redis  string `json:"redis"`
		}
		resp := healthResponse{Status: "ok", DB: "ok", Redis: "ok"}
		if err := pool.Ping(r.Context()); err != nil {
			resp.DB = "error"
			resp.Status = "degraded"
		}
		if err := db.Ping(r.Context(), redisClient); err != nil {
			resp.Redis = "error"
			resp.Status = "degraded"
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.Status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("missing required env var", "key", key)
		os.Exit(1)
	}
	return v
}

func parseRedisURL(rawURL string) (addr, password string, dbIndex int) {
	addr = "localhost:6379"
	password = ""
	dbIndex = 0
	if rawURL == "" {
		return
	}
	rest := rawURL
	if len(rest) >= 8 && rest[:8] == "redis://" {
		rest = rest[8:]
	}
	if at := strLastIndex(rest, "@"); at >= 0 {
		userInfo := rest[:at]
		rest = rest[at+1:]
		if colon := strIndex(userInfo, ":"); colon >= 0 {
			password = userInfo[colon+1:]
		}
	}
	if slash := strIndex(rest, "/"); slash >= 0 {
		dbStr := rest[slash+1:]
		rest = rest[:slash]
		for _, c := range dbStr {
			if c >= '0' && c <= '9' {
				dbIndex = dbIndex*10 + int(c-'0')
			}
		}
	}
	if rest != "" {
		addr = rest
	}
	return
}

func strIndex(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func strLastIndex(s, sub string) int {
	last := -1
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			last = i
		}
	}
	return last
}
