package db

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a pgx connection pool and verifies connectivity with a ping.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
	}
	// DB_MAX_CONNS caps the pool (e.g. a small managed Postgres with a low
	// connection limit). Unset keeps pgx's default.
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 {
			return nil, fmt.Errorf("db: DB_MAX_CONNS must be a positive integer, got %q", v)
		}
		cfg.MaxConns = int32(n)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return pool, nil
}

// RunMigrations runs all pending up-migrations from the given directory path.
func RunMigrations(dsn, migrationsPath string) error {
	source := "file://" + migrationsPath

	m, err := migrate.New(source, dsn)
	if err != nil {
		return fmt.Errorf("db: create migrate instance: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("db: run migrations: %w", err)
	}

	return nil
}
