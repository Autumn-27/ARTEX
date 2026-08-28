package db

import (
	"testing"
)

// The raw-body columns were added after release, so existing installs only get
// them through llmRecordsMigrate. This runs the real migration against the dev
// PG on a table stripped back to its pre-upgrade shape, inside a transaction
// that is always rolled back — PG does transactional DDL, so nothing persists.
func TestLLMRecordsMigrateAddsRawColumnsToOldTable(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // the test never commits

	// Roll the table back to how a pre-upgrade install looks.
	if _, err := tx.Exec(`ALTER TABLE llm_records DROP COLUMN IF EXISTS raw_request, DROP COLUMN IF EXISTS raw_response`); err != nil {
		t.Fatalf("simulate old table: %v", err)
	}
	if _, err := tx.Exec(llmRecordsMigrate); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, col := range []string{"raw_request", "raw_response"} {
		var n int
		if err := tx.QueryRow(
			`SELECT count(*) FROM information_schema.columns
			 WHERE table_name='llm_records' AND column_name=$1`, col).Scan(&n); err != nil {
			t.Fatalf("inspect %s: %v", col, err)
		}
		if n != 1 {
			t.Errorf("column %s missing after migrate", col)
		}
	}

	// Re-running must stay a no-op (the migration runs on every startup).
	if _, err := tx.Exec(llmRecordsMigrate); err != nil {
		t.Fatalf("migrate is not idempotent: %v", err)
	}

	// An insert carrying raw bodies must round-trip through the migrated table.
	var got string
	if err := tx.QueryRow(
		`INSERT INTO llm_records(model, raw_request, raw_response) VALUES ('m','{"a":1}','data: x')
		 RETURNING raw_response`).Scan(&got); err != nil {
		t.Fatalf("insert into migrated table: %v", err)
	}
	if got != "data: x" {
		t.Errorf("raw_response=%q want %q", got, "data: x")
	}
}
