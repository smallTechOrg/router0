// Package testdb applies the repo's migrations to DATABASE_URL once per test
// binary, so DB-backed unit tests do not depend on the integration suite having
// migrated the database first.
package testdb

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/zer0-router/zer0/internal/db"
)

var (
	once sync.Once
	err  error
)

// Migrate runs every pending migration against dsn (golang-migrate takes a
// Postgres advisory lock, so concurrent test packages are safe).
func Migrate(t testing.TB, dsn string) {
	t.Helper()
	once.Do(func() {
		_, f, _, _ := runtime.Caller(0)
		err = db.RunMigrations(dsn, filepath.Join(filepath.Dir(f), "..", "..", "..", "migrations"))
	})
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
}
