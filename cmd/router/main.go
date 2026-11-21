package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/db"
	"github.com/zer0-router/zer0/internal/orgs"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/server"
)

func main() {
	// ── Structured logger ────────────────────────────────────────────────────
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})).With("service", "router0"))

	// ── Config from env ──────────────────────────────────────────────────────
	port := envOrDefault("PORT", "8080")
	databaseURL := mustEnv("DATABASE_URL")
	jwtSecret := mustEnv("JWT_SECRET")
	if len(os.Getenv("AES_KEY")) != 64 {
		slog.Error("AES_KEY must be 64 hex chars (openssl rand -hex 32)")
		os.Exit(1)
	}

	// ── Migrations path ──────────────────────────────────────────────────────
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	// ── PostgreSQL ───────────────────────────────────────────────────────────
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		slog.Error("postgres: connect failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("postgres: connected")

	if err := db.RunMigrations(databaseURL, migrationsPath); err != nil {
		slog.Error("migrations: failed", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations: up to date")
	slog.Info("providers: registered", "providers", providers.ValidProviders())

	bootstrapAdmin(ctx, pool)

	// ── Gateway ──────────────────────────────────────────────────────────────
	app := server.Build(server.Deps{Pool: pool, JWTSecret: jwtSecret})

	// Background jobs: catalog refresh (startup pass for never-refreshed pairs,
	// then periodic) and hourly sweep of expired key-state rows.
	app.Scheduler.Start(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := app.KeyState.Sweep(ctx); err != nil {
					slog.Warn("keystate: sweep failed", "error", err)
				} else if n > 0 {
					slog.Info("keystate: swept expired rows", "count", n)
				}
			}
		}
	}()

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      app.Handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // streaming responses; per-attempt timeouts bound upstream calls
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		slog.Info("server: starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server: listen error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("server: shutting down")
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server: shutdown error", "error", err)
	}
	// Flush buffered request-log records after in-flight requests have finished.
	if err := app.Close(shutdownCtx); err != nil {
		slog.Error("recorder: flush incomplete", "error", err)
	}
	slog.Info("server: stopped")
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

// bootstrapAdmin creates the platform admin (ADMIN_EMAIL / ADMIN_PASSWORD) when
// none exists, and makes sure every platform admin belongs to an org (created
// as owner) so the dashboard works for them.
func bootstrapAdmin(ctx context.Context, pool *pgxpool.Pool) {
	email := os.Getenv("ADMIN_EMAIL")
	password := os.Getenv("ADMIN_PASSWORD")
	if email == "" || password == "" {
		slog.Warn("admin bootstrap: ADMIN_EMAIL or ADMIN_PASSWORD not set, skipping")
		return
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&count); err != nil {
		slog.Error("admin bootstrap: query failed", "error", err)
		return
	}
	if count == 0 {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			slog.Error("admin bootstrap: failed to hash password", "error", err)
			return
		}
		_, err = pool.Exec(ctx,
			`INSERT INTO users (email, password_hash, role, status)
			 VALUES ($1, $2, 'admin', 'active')
			 ON CONFLICT (email) DO UPDATE SET role = 'admin', password_hash = $2`,
			email, string(hash))
		if err != nil {
			slog.Error("admin bootstrap: failed to upsert admin", "error", err)
			return
		}
		slog.Info("admin bootstrap: admin user ready", "email", email)
	} else {
		slog.Info("admin bootstrap: admin user already exists")
	}
	ensureAdminOrgs(ctx, pool)
}

// ensureAdminOrgs gives every platform admin without an org a personal org.
func ensureAdminOrgs(ctx context.Context, pool *pgxpool.Pool) {
	rows, err := pool.Query(ctx,
		`SELECT u.id::text, u.email FROM users u
		  WHERE u.role = 'admin' AND NOT EXISTS (SELECT 1 FROM org_members m WHERE m.user_id = u.id)`)
	if err != nil {
		slog.Error("admin bootstrap: list admins failed", "error", err)
		return
	}
	type adm struct{ id, email string }
	var todo []adm
	for rows.Next() {
		var a adm
		if rows.Scan(&a.id, &a.email) == nil {
			todo = append(todo, a)
		}
	}
	rows.Close()
	for _, a := range todo {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			_, err := orgs.CreateWithOwner(ctx, tx, a.email, a.id)
			return err
		})
		if err != nil {
			slog.Error("admin bootstrap: create org failed", "email", a.email, "error", err)
			continue
		}
		slog.Info("admin bootstrap: org created", "email", a.email)
	}
}
