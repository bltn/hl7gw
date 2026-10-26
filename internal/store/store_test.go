package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenSetsConnectionPragmas(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "ch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	checks := []struct {
		pragma string
		want   string
	}{
		{pragma: "busy_timeout", want: "5000"},
		{pragma: "journal_mode", want: "wal"},
		{pragma: "synchronous", want: "2"},
		{pragma: "foreign_keys", want: "1"},
	}
	for _, c := range checks {
		var got string
		if err := st.db.QueryRowContext(t.Context(), "PRAGMA "+c.pragma).Scan(&got); err != nil {
			t.Fatalf("reading %s: %v", c.pragma, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q", c.pragma, got, c.want)
		}
	}
}

func TestReadsFailOnClosedStore(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "ch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := st.GetMessage(t.Context(), 1); err == nil {
		t.Error("GetMessage succeeded on a closed store, want error")
	}
	if _, err := st.ListMessages(t.Context(), 1, 50); err == nil {
		t.Error("ListMessages succeeded on a closed store, want error")
	}
	if err := st.Ping(t.Context()); err == nil {
		t.Error("Ping succeeded on a closed store, want error")
	}
	if _, err := st.Prune(t.Context(), time.Now()); err == nil {
		t.Error("Prune succeeded on a closed store, want error")
	}
}

func TestPingSucceedsOnOpenStore(t *testing.T) {
	t.Parallel()
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "ch.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	if err := st.Ping(t.Context()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestMigrateAppliesChainInOrderAndStampsVersion(t *testing.T) {
	t.Parallel()
	db := rawOpen(t, filepath.Join(t.TempDir(), "m.db"))
	migs := []migration{
		{name: "0001_one", sql: "CREATE TABLE t1 (id INTEGER)"},
		{name: "0002_two", sql: "CREATE TABLE t2 (t1_id INTEGER REFERENCES t1(id))"},
		{name: "0003_three", sql: "CREATE TABLE t3 (id INTEGER)"},
	}

	if err := migrate(t.Context(), db, migs); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if got := readVersion(t, db); got != 3 {
		t.Fatalf("user_version = %d, want 3", got)
	}
	for _, table := range []string{"t1", "t2", "t3"} {
		if !hasTable(t, db, table) {
			t.Errorf("table %s missing after migration", table)
		}
	}
}

func TestMigrateResumesFromCurrentVersion(t *testing.T) {
	t.Parallel()
	db := rawOpen(t, filepath.Join(t.TempDir(), "m.db"))
	migs := []migration{
		{name: "0001_first", sql: "CREATE TABLE t1 (n INTEGER)"},
		{name: "0002_second", sql: "CREATE TABLE t2 (id INTEGER)"},
		{name: "0003_third", sql: "CREATE TABLE t3 (id INTEGER)"},
	}

	if err := migrate(t.Context(), db, migs[:2]); err != nil {
		t.Fatalf("migrate first two: %v", err)
	}
	if err := migrate(t.Context(), db, migs); err != nil {
		t.Fatalf("migrate full chain: %v", err)
	}

	if got := readVersion(t, db); got != 3 {
		t.Fatalf("user_version = %d, want 3", got)
	}
}

func TestMigrateRunsMultiStatementSteps(t *testing.T) {
	t.Parallel()
	db := rawOpen(t, filepath.Join(t.TempDir(), "m.db"))
	migs := []migration{
		{name: "0001_two_tables", sql: "CREATE TABLE t1 (id INTEGER);\nCREATE TABLE t2 (id INTEGER)"},
	}

	if err := migrate(t.Context(), db, migs); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{"t1", "t2"} {
		if !hasTable(t, db, table) {
			t.Errorf("table %s missing after multi-statement migration", table)
		}
	}
}

func TestMigrateRollsBackFailedStepEntirely(t *testing.T) {
	t.Parallel()
	db := rawOpen(t, filepath.Join(t.TempDir(), "m.db"))
	migs := []migration{
		{name: "0001_good", sql: "CREATE TABLE t1 (kept INTEGER)"},
		{name: "0002_bad", sql: "CREATE TABLE t2 (id INTEGER);\nCREATE TABLE ("},
	}

	err := migrate(t.Context(), db, migs)
	if err == nil {
		t.Fatal("migrate succeeded with a broken step, want error")
	}
	if !strings.Contains(err.Error(), "migration 0002_bad") {
		t.Fatalf("error = %q, want it to name migration 0002_bad", err)
	}

	if got := readVersion(t, db); got != 1 {
		t.Fatalf("user_version = %d, want 1 (failed step must not advance)", got)
	}
	if !hasTable(t, db, "t1") {
		t.Error("table t1 from the successful step missing")
	}
	if hasTable(t, db, "t2") {
		t.Error("table t2 from the failed step survived, want full rollback")
	}
}

func TestMigrateSurfacesVersionReadError(t *testing.T) {
	t.Parallel()
	db := rawOpen(t, filepath.Join(t.TempDir(), "m.db"))
	if err := db.Close(); err != nil {
		t.Fatalf("closing db: %v", err)
	}

	if err := migrate(t.Context(), db, nil); err == nil {
		t.Fatal("migrate succeeded on a closed db, want error")
	}
}

func TestApplySurfacesBeginError(t *testing.T) {
	t.Parallel()
	db := rawOpen(t, filepath.Join(t.TempDir(), "m.db"))
	if err := db.Close(); err != nil {
		t.Fatalf("closing db: %v", err)
	}

	err := apply(t.Context(), db, migration{name: "0001_solo", sql: "CREATE TABLE t1 (unused INTEGER)"}, 1)
	if err == nil {
		t.Fatal("apply succeeded on a closed db, want error")
	}
}

func rawOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, _ := sql.Open("sqlite", "file:"+path)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func readVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("reading user_version: %v", err)
	}
	return v
}

func hasTable(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	if err != nil {
		t.Fatalf("checking for table %s: %v", name, err)
	}
	return n == 1
}
