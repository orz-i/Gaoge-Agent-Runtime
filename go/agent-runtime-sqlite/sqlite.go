package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	sqlite3 "modernc.org/sqlite"
)

// The first public schema is still under development; retain version 1.
const schemaVersion = 1

var (
	ErrUnsupportedSchema = errors.New("unsupported SQLite runtime schema")
	ErrInvalidRecord     = errors.New("invalid SQLite kernel record")
	ErrBusy              = errors.New("SQLite database temporarily busy")
	ErrInsecurePath      = errors.New("SQLite path permissions or file type are insecure")
	ErrInvalidTime       = errors.New("SQLite time outside representable nanosecond range")
)

type Store struct {
	db *sql.DB
}

var _ kernel.Store = (*Store)(nil)

// Open is the fail-closed **existing database only** entry point.
// A missing DB never becomes a new empty Run Store. Use CreateNew for an
// explicit first initialization; migration and recovery must use Open.
func Open(ctx context.Context, path string) (*Store, error) { return OpenExisting(ctx, path) }

// OpenExisting opens a previously initialized, private database only.
func OpenExisting(ctx context.Context, path string) (*Store, error) {
	return openStore(ctx, path, false)
}

// CreateNew initializes an unpublished temporary database in the private
// Profile directory and publishes its completed main DB file atomically via
// a no-overwrite hard link. An interrupted initial initialization therefore
// never leaves a version-0 database at the requested path. Hard-link support
// is required for this opt-in desktop prototype; fail closed otherwise.
func CreateNew(ctx context.Context, path string) (*Store, error) {
	if ctx == nil || strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = validatePrivateDirectory(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	if err = checkNewDestination(abs); err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".init-*")
	if err != nil {
		return nil, err
	}
	staged := tmp.Name()
	// Only files created under this unique name in this invocation may be
	// removed on error. Never remove the requested destination or its WAL.
	defer func() {
		_ = os.Remove(staged)
		_ = os.Remove(staged + "-wal")
		_ = os.Remove(staged + "-shm")
	}()
	if err = tmp.Close(); err != nil {
		return nil, err
	}
	stagedStore, err := openStore(ctx, staged, true)
	if err != nil {
		return nil, err
	}
	if err = stagedStore.Close(); err != nil {
		return nil, err
	}
	// Do not publish a DB requiring an uncommitted sidecar. The final close
	// checkpoints WAL into the main file; unexpected sidecars fail closed.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, statErr := os.Lstat(staged + suffix); statErr == nil {
			return nil, fmt.Errorf("%w: unpublished SQLite sidecar remains", ErrUnsupportedSchema)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = checkNewDestination(abs); err != nil {
		return nil, err
	}
	// Unlike Rename, Link never replaces an existing DB and publishes only
	// the fully initialized, checkpointed main file on the same filesystem.
	if err = os.Link(staged, abs); err != nil {
		return nil, err
	}
	// The link is already a complete database. If syncing the parent fails,
	// report uncertain publication durability; callers must inspect/reopen
	// the target, NEVER delete/replace it or silently call CreateNew again.
	dir, err := os.Open(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("published SQLite directory cannot be synced: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return nil, fmt.Errorf("published SQLite directory sync failed: %w", syncErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("published SQLite directory close failed: %w", closeErr)
	}
	return OpenExisting(ctx, abs)
}

func checkNewDestination(abs string) error {
	if _, statErr := os.Lstat(abs); statErr == nil {
		return os.ErrExist
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, statErr := os.Lstat(abs + suffix); statErr == nil {
			return fmt.Errorf("%w: orphan SQLite sidecar exists: %s", ErrInsecurePath, abs+suffix)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return nil
}

// The caller owns the app Profile directory and platform credentials. Stage 1
// supports a single executor, NOT multi-worker execution epoch isolation.
func openStore(ctx context.Context, path string, create bool) (*Store, error) {
	if ctx == nil || strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, kernel.ErrInvalidInput
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = validatePrivateDirectory(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	if !create {
		// Existing sidecars must be private before SQLite reads them.
		for _, suffix := range []string{"-wal", "-shm"} {
			if err = validateOptionalPrivateFile(abs + suffix); err != nil {
				return nil, err
			}
		}
	}
	if err = validatePrivateFile(abs); err != nil {
		return nil, err
	}
	// mode=rw prevents SQLite's normal implicit file creation on reopen.
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	params := url.Values{}
	params.Set("mode", "rw")
	params.Add("_pragma", "foreign_keys(ON)")
	// A native busy handler may outlive cancellation; bound lock waiting.
	params.Add("_pragma", "busy_timeout(500)")
	params.Add("_pragma", "synchronous(FULL)")
	params.Set("_txlock", "immediate")
	u.RawQuery = params.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err = store.initialize(ctx, create); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = validateOptionalPrivateFile(abs + suffix); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("SQLite Profile directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: Profile directory is not a real directory", ErrInsecurePath)
	}
	// ACL validation differs on Windows; it is not an approved Stage 1 target.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: Profile directory must be private (0700): %s", ErrInsecurePath, path)
	}
	return nil
}
func validatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: SQLite file is not a regular file: %s", ErrInsecurePath, path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: SQLite file must be private (0600): %s", ErrInsecurePath, path)
	}
	return nil
}
func validateOptionalPrivateFile(path string) error {
	if err := validatePrivateFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Close releases this adapter's database connection; the OS owns checkpoint
// scheduling and may terminate the process without invoking Close.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) initialize(ctx context.Context, create bool) error {
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return fmt.Errorf("%w: SQLite foreign keys disabled", ErrUnsupportedSchema)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if create && version != 0 {
		return fmt.Errorf("%w: new database has version %d", ErrUnsupportedSchema, version)
	}
	if !create && version != schemaVersion {
		return fmt.Errorf("%w: existing database has version %d", ErrUnsupportedSchema, version)
	}
	// Reject incompatible existing DDL before switching journal modes:
	// PRAGMA journal_mode=WAL changes persistent SQLite metadata, even if
	// subsequent schema validation fails.
	if !create {
		if err := s.validateSchema(ctx); err != nil {
			return err
		}
	}
	if create {
		var existing int
		err := s.db.QueryRowContext(ctx,
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'",
		).Scan(&existing)
		if err != nil {
			return err
		}
		if existing != 0 {
			return fmt.Errorf("%w: unversioned nonempty database", ErrUnsupportedSchema)
		}
	}
	// Only switch to WAL after validating existing Schema shape (or after
	// proving this is an unpublished empty staging database).
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("%w: expected WAL, got %q", ErrUnsupportedSchema, mode)
	}
	if create {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, ddl := range schemaDDL {
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				return fmt.Errorf("create SQLite runtime schema: %w", err)
			}
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return s.validateSchema(ctx)
	}
	return nil
}

var schemaDDL = []string{
	`CREATE TABLE runtime_runs (
		run_id TEXT PRIMARY KEY,
		scope_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK(revision > 0),
		run_json BLOB NOT NULL,
		state_json BLOB NOT NULL,
		checkpoint_json BLOB,
		result_json BLOB,
		event_head INTEGER NOT NULL DEFAULT 0,
        progress_kind TEXT NOT NULL DEFAULT 'legacy_six_state',
        execution_epoch INTEGER NOT NULL DEFAULT 0,
        execution_owner TEXT NOT NULL DEFAULT '',
        execution_lease_id TEXT NOT NULL DEFAULT '',
        execution_expiry_ms INTEGER NOT NULL DEFAULT 0
    )`,
	`CREATE TABLE runtime_events (
		run_id TEXT NOT NULL REFERENCES runtime_runs(run_id),
		seq INTEGER NOT NULL,
		event_type TEXT NOT NULL,
		message TEXT NOT NULL,
		data_json BLOB,
		created_ns INTEGER NOT NULL,
		PRIMARY KEY(run_id, seq)
	)`,
	`CREATE INDEX runtime_events_type ON runtime_events(event_type, run_id, seq)`,
	// This is the Kernel transition outbox materialized directly into the
	// local ready-work ledger. It is NOT an independent job queue.
	`CREATE TABLE runtime_ready_work (
		work_id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL REFERENCES runtime_runs(run_id),
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		revision INTEGER NOT NULL,
		events_json BLOB NOT NULL,
        payload_hash TEXT NOT NULL,
		committed_ns INTEGER NOT NULL,
		available_ns INTEGER NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		worker_id TEXT NOT NULL DEFAULT '',
		lease_id TEXT NOT NULL DEFAULT '',
        lease_until_ns INTEGER NOT NULL DEFAULT 0,
        quarantine_code TEXT NOT NULL DEFAULT '',
        quarantined_ns INTEGER NOT NULL DEFAULT 0
    )`,
	`CREATE INDEX runtime_ready_due ON runtime_ready_work(quarantine_code, available_ns, committed_ns, work_id)`,
	`CREATE TABLE runtime_commit_proofs (
		run_id TEXT NOT NULL REFERENCES runtime_runs(run_id),
		revision INTEGER NOT NULL,
		fingerprint TEXT NOT NULL,
		committed_ns INTEGER NOT NULL,
		PRIMARY KEY(run_id, revision)
	)`,
}

func errorCode(err error) int {
	var se *sqlite3.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

func constraintError(err error) bool { return errorCode(err)&0xff == 19 }

func conflictOrDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	// SQLITE_BUSY/LOCKED are transient database contention, never a Run
	// revision conflict. Mapping them to kernel.ErrConflict could make a
	// continuation dispatcher incorrectly acknowledge a needed wakeup.
	switch errorCode(err) & 0xff {
	case 5, 6:
		return fmt.Errorf("%w: %w", ErrBusy, err)
	default:
		return err
	}
}
