package guard

import (
	"context"
	"os"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// Hold the same advisory lock as db, agent, server, evidence and intercept so
// PG-backed tests serialize across packages on the shared dev DB. Without a
// configured database, tests that need PG skip themselves.
func TestMain(m *testing.M) {
	dsn, _, err := db.DSN()
	if err != nil {
		os.Exit(m.Run())
	}
	pg, err := db.Open(dsn)
	if err != nil {
		os.Exit(m.Run())
	}
	defer pg.Close()
	conn, err := pg.Conn(context.Background())
	if err != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), `SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}
