package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyFixture puts a copy of testdata/<name> in a temp dir (fixtures are
// committed read-only snapshots of older releases' databases).
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertFixtureData(t *testing.T, s *Store) {
	t.Helper()
	runs, err := s.ListRuns(RunFilter{})
	if err != nil || len(runs) != 1 || runs[0].Kind != "tf.plan" || runs[0].Status != StatusSucceeded {
		t.Fatalf("runs after upgrade: %+v %v", runs, err)
	}
	if st, _ := s.ListStages(1); len(st) != 1 || st[0].Name != "plan" {
		t.Fatalf("stages after upgrade: %+v", st)
	}
	if v, ok, _ := s.GetKV("onboarding"); !ok || v != `{"tour":"done"}` {
		t.Fatalf("kv after upgrade: %q %v", v, ok)
	}
	ds, err := s.ListDiagnostics("", "", "")
	if err != nil || len(ds) != 1 || ds[0].Code != "terraform_unused_declarations" {
		t.Fatalf("diagnostics after upgrade: %+v %v", ds, err)
	}
	// the migrated database is at the current schema and usable for new rows
	if v, _ := s.schemaVersion(); v != SchemaVersion() {
		t.Fatalf("schema %d, want %d", v, SchemaVersion())
	}
	if _, err := s.CreateRun("tf.plan", nil, nil, ""); err != nil {
		t.Fatal(err)
	}
}

// A database from an early 0.x build (only the first four migrations) is
// upgraded on open, after a backup and an integrity check, with its data
// intact.
func TestOpenUpgradesAnOldDatabaseWithBackup(t *testing.T) {
	p := copyFixture(t, "schema4.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertFixtureData(t, s)
	bak := p + ".bak-4"
	st, err := os.Stat(bak)
	if err != nil {
		t.Fatalf("no backup before migrating: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", st.Mode())
	}
	// the backup is the pre-migration database: schema 4, data intact
	raw, err := sql.Open("sqlite", "file:"+bak)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var ver int64
	var n int
	if err := raw.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&ver); err != nil || ver != 4 {
		t.Fatalf("backup schema %d %v, want 4", ver, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup runs %d %v", n, err)
	}
}

// A database written by 0.4.1 (schema 8) migrates with a backup when this
// build knows more migrations, and opens as-is with no backup when it
// doesn't. Opening it a second time never backs up again.
func TestOpenDatabaseFrom041(t *testing.T) {
	p := copyFixture(t, "v0.4.1.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	assertFixtureData(t, s)
	s.Close()
	m, _ := filepath.Glob(p + ".bak-*")
	if SchemaVersion() == 8 && len(m) != 0 {
		t.Fatalf("unexpected backup %v", m)
	}
	if SchemaVersion() > 8 && (len(m) != 1 || !strings.HasSuffix(m[0], ".bak-8")) {
		t.Fatalf("want one backup of schema 8, got %v", m)
	}
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if again, _ := filepath.Glob(p + ".bak-*"); len(again) != len(m) {
		t.Fatalf("a second open at the same schema must not back up again: %v", again)
	}
}

// A database last written by a newer release is refused with a clear error,
// not migrated down or misread.
func TestOpenRefusesANewerSchema(t *testing.T) {
	p := copyFixture(t, "v0.4.1.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, SchemaVersion()+5); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, _ := filepath.Glob(p + ".bak-*") // from the first, legitimate open
	_, err = Open(p)
	if !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("got %v", err)
	}
	if after, _ := filepath.Glob(p + ".bak-*"); len(after) != len(before) {
		t.Fatalf("a refused open must not write backups: %v", after)
	}
}

// A damaged database is not migrated and not written to: the error names
// the integrity check and the file is left for recovery.
func TestOpenRefusesToMigrateACorruptDatabase(t *testing.T) {
	p := copyFixture(t, "schema4.db")
	// damage the issues table's root page only: the version table still
	// reads fine, so it is the integrity check that has to notice
	raw, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	var page int
	if err := raw.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name = 'issues'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	b, _ := os.ReadFile(p)
	off := (page - 1) * 4096
	for i := off + 2048; i < off+4096 && i < len(b); i++ {
		b[i] = 0xff
	}
	os.WriteFile(p, b, 0o600)
	_, err = Open(p)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v, want ErrCorrupt", err)
	}
	// nothing was migrated: the file still says schema 4, for recovery by hand
	raw, _ = sql.Open("sqlite", "file:"+p)
	defer raw.Close()
	var ver int64
	if err := raw.QueryRow(`SELECT MAX(version_id) FROM goose_db_version`).Scan(&ver); err != nil || ver != 4 {
		t.Fatalf("schema after refused open: %d %v", ver, err)
	}
}
