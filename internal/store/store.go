// Package store owns .groundwork/state.db (pure-Go SQLite, WAL).
package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNewerSchema means the database was last written by a release that knows
// more migrations than this build: opening it could misread or damage it.
var ErrNewerSchema = errors.New("database was written by a newer groundwork")

// ErrCorrupt means SQLite's integrity check failed before a migration.
var ErrCorrupt = errors.New("database failed its integrity check")

type Store struct {
	DB *sql.DB
}

var gooseOnce sync.Once

// SchemaVersion is the newest migration this build knows.
func SchemaVersion() int64 {
	gooseOnce.Do(gooseSetup)
	ms, err := goose.CollectMigrations("migrations", 0, math.MaxInt64)
	if err != nil || len(ms) == 0 {
		return 0
	}
	last, _ := ms.Last()
	return last.Version
}

func gooseSetup() {
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	_ = goose.SetDialect("sqlite3")
}

// Open opens (creating if needed) the database at path and applies migrations.
//
// An existing database is never migrated blindly: one from a newer release
// is refused (ErrNewerSchema), and an older one is upgraded only after
// SQLite's quick_check passes and a copy is kept as <path>.bak-<schema>.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialises writes; plenty for a local tool
	gooseOnce.Do(gooseSetup)
	target := SchemaVersion()
	current, err := goose.EnsureDBVersion(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	switch {
	case current > target:
		db.Close()
		return nil, fmt.Errorf("%w: %s has schema %d and this build knows %d; upgrade groundwork", ErrNewerSchema, path, current, target)
	case current > 0 && current < target:
		// an upgrade: don't touch a damaged file (it stays as it is for
		// recovery), and keep a copy of a healthy one before changing it
		if err := quickCheck(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w; not migrating it", path, err)
		}
		if err := backup(db, path, current); err != nil {
			db.Close()
			return nil, fmt.Errorf("back up %s before migrating: %w", path, err)
		}
	}
	if err := goose.Up(db, "migrations"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{DB: db}, nil
}

// backup writes a consistent copy of the open database (VACUUM INTO reads
// through the WAL) as <path>.bak-<schema>, replacing an older copy for the
// same schema.
func backup(db *sql.DB, path string, schema int64) error {
	dst := fmt.Sprintf("%s.bak-%d", path, schema)
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := db.Exec("VACUUM INTO ?", dst); err != nil {
		return err
	}
	return os.Chmod(dst, 0o600)
}

func quickCheck(db *sql.DB) error {
	var res string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if !strings.EqualFold(res, "ok") {
		return fmt.Errorf("%w: %s", ErrCorrupt, res)
	}
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

// KV helpers (onboarding state, settings, server info).

func (s *Store) GetKV(key string) (string, bool, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetKV(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO kv(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// schemaVersion is the migration the open database is at.
func (s *Store) schemaVersion() (int64, error) { return goose.GetDBVersion(s.DB) }
