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

	"github.com/zer0-router/zer0/internal/db"
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

	// ── Migrations path ──────────────────────────────────────────────────────
	// Resolve migrations/ relative to the repo root (two levels above cmd/router/main.go).
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

	// ── Router ───────────────────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", healthzHandler(pool, redisClient))
	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	// ── HTTP server ──────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
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

		resp := healthResponse{
			Status: "ok",
			DB:     "ok",
			Redis:  "ok",
		}

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

// envOrDefault returns the value of the env var or a fallback.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// mustEnv reads an env var and exits if it is empty.
func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("missing required env var", "key", key)
		os.Exit(1)
	}
	return v
}

// parseRedisURL parses redis://[:password@]host:port/db into components.
// Supports the simple forms used in .env.example.
func parseRedisURL(rawURL string) (addr, password string, dbIndex int) {
	addr = "localhost:6379"
	password = ""
	dbIndex = 0

	if rawURL == "" {
		return
	}

	rest := rawURL
	// Strip scheme
	if len(rest) >= 8 && rest[:8] == "redis://" {
		rest = rest[8:]
	}

	// Extract password (redis://:password@host:port/db)
	if at := strLastIndex(rest, "@"); at >= 0 {
		userInfo := rest[:at]
		rest = rest[at+1:]
		if colon := strIndex(userInfo, ":"); colon >= 0 {
			password = userInfo[colon+1:]
		}
	}

	// Extract db index after last '/'
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
