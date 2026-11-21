// Command migrate applies all pending database migrations and exits.
//
// It uses the same code path as the gateway's startup migration, so CI and local
// gates can prepare a fresh database before running DB-backed unit tests.
//
//	DATABASE_URL=postgres://... go run ./cmd/migrate
//
// MIGRATIONS_PATH overrides the migrations directory (default: ./migrations next
// to the repo root, resolved from this source file).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/zer0-router/zer0/internal/db"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "migrate: DATABASE_URL is required")
		os.Exit(1)
	}
	path := os.Getenv("MIGRATIONS_PATH")
	if path == "" {
		_, thisFile, _, _ := runtime.Caller(0)
		path = filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	}
	if err := db.RunMigrations(dsn, path); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: failed:", err)
		os.Exit(1)
	}
	fmt.Println("migrate: database is up to date")
}
