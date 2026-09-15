package db

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// db test suite. Packages agent and server hold the same lock, so parallel
// `go test ./...` runs serialize across packages on the shared dev DB and
// avoid cross-package cleanup races (e.g. DELETE FROM assets WHERE id > X
// from one package deleting assets created by another).
func TestMain(m *testing.M) {
	dsn, _, err := DSN()
	if err != nil {
		// No DB configured — tests that need PG will skip themselves.
		// WARNING: this makes `go test ./...` look green while every PG-backed
		// test is silently skipped. Local dev keeps this lenient on purpose,
		// but CI (release.yml `test` job) MUST provide ARTEX_PG_DSN so the
		// suite actually runs — a green CI run with this warning in the log
		// means the gate is broken, not that the tests passed.
		fmt.Fprintf(os.Stderr,
			"\n*** WARNING: db tests running without ARTEX_PG_DSN — all PostgreSQL-backed tests will SKIP silently. CI must set ARTEX_PG_DSN. ***\n\n")
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}
