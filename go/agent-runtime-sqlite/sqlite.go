package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	ncruces "github.com/ncruces/go-sqlite3"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	sqlite3 "modernc.org/sqlite"
)

// Schema v11 includes the native transaction-owned budget ledger. Open never migrates.
const schemaVersion = 11

var (
	ErrUnsupportedSchema = profile.ErrUnsupportedSchema
	ErrInvalidRecord     = errors.New("invalid SQLite kernel record")
	ErrBusy              = errors.New("SQLite database temporarily busy")
	ErrInsecurePath      = profile.ErrInsecurePath
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
	return OpenExistingWith(ctx, path, nil)
}

// OpenExistingWith validates a Kernel database through the chosen connection.
func OpenExistingWith(ctx context.Context, path string, open profile.OpenConnection) (*Store, error) {
	return kernelStore(open.OpenExisting(ctx, path, validateExisting))
}

// CreateNew atomically publishes a fully initialized, private Kernel database.
func CreateNew(ctx context.Context, path string) (*Store, error) {
	return CreateNewWith(ctx, path, nil)
}

// CreateNewWith enrolls a Kernel database through the chosen connection.
func CreateNewWith(ctx context.Context, path string, open profile.OpenConnection) (*Store, error) {
	return kernelStore(open.CreateNew(ctx, path, initializeNew, validateExisting))
}

func initializeNew(ctx context.Context, db *sql.DB) error {
	return (&Store{db: db}).initialize(ctx, true)
}
func validateExisting(ctx context.Context, db *sql.DB) error {
	return (&Store{db: db}).initialize(ctx, false)
}

func kernelSetup(method func(*Store, context.Context) error) profile.Setup {
	return func(ctx context.Context, db *sql.DB) error { return method(&Store{db: db}, ctx) }
}

func openKernel(ctx context.Context, path string, setup profile.Setup) (*Store, error) {
	return kernelStore(profile.OpenExisting(ctx, path, setup))
}

func kernelStore(db *sql.DB, err error) (*Store, error) {
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
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
		if _, err := sqliteManagedAuthority(ctx, s.db); err != nil {
			return err
		}
		if err := validateFinalRetirements(ctx, s.db); err != nil {
			return err
		}
		if err := validateNativeBudgetBindings(ctx, s.db); err != nil {
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
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return err
		}
		if err = initializeManagedAuthority(ctx, tx); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return s.validateSchema(ctx)
	}
	return nil
}

const callbackToolReturnsColumn = "callback_tool_returns TEXT NOT NULL DEFAULT ''"

var schemaDDL = append(schemaV10DDL(), sqlledger.SQLiteDDL)

func schemaV10DDL() []string { return append(schemaV9DDL(), grantIdentityDDL) }

func schemaV9DDL() []string {
	out := schemaV8DDL()
	for i, ddl := range out {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			out[i] = strings.Replace(ddl, "settlement_evidence_json BLOB,", "settlement_evidence_json BLOB, "+callbackToolReturnsColumn+",", 1)
		}
	}
	return append(out, relationProgressDDL)
}

func schemaV8DDL() []string {
	return append(append([]string{
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
        execution_expiry_ms INTEGER NOT NULL DEFAULT 0,
        execution_quarantine_revision INTEGER NOT NULL DEFAULT 0,
        execution_quarantine_generation INTEGER NOT NULL DEFAULT 0,
        execution_quarantine_code TEXT NOT NULL DEFAULT ''
    , execution_physical_epoch INTEGER NOT NULL DEFAULT 0)`,
		// A child Grant is persisted against every ancestor. Terminal parent CAS
		// checks this table in the SAME SQLite writer transaction as its Run CAS.
		`CREATE TABLE runtime_child_effect_grants (
		grant_id TEXT NOT NULL,
		parent_run_id TEXT NOT NULL REFERENCES runtime_runs(run_id),
		child_run_id TEXT NOT NULL REFERENCES runtime_runs(run_id),
		start_revision INTEGER NOT NULL,
		settled_revision INTEGER NOT NULL DEFAULT 0,
		execution_owner_id TEXT NOT NULL DEFAULT '',
		execution_lease_id TEXT NOT NULL DEFAULT '',
		execution_epoch INTEGER NOT NULL DEFAULT 0,
		admission_source_hash TEXT NOT NULL DEFAULT '',
		admission_operation TEXT NOT NULL DEFAULT '',
		callback_return_revision INTEGER NOT NULL DEFAULT 0,
		callback_return_source_hash TEXT NOT NULL DEFAULT '',
		callback_host_completed INTEGER NOT NULL DEFAULT 0,
		callback_lease_owned INTEGER NOT NULL DEFAULT 0,
		callback_control_custody TEXT NOT NULL DEFAULT '',
		settlement_evidence_json BLOB,
		PRIMARY KEY(grant_id,parent_run_id)
	)`,
		`CREATE INDEX runtime_child_grants_parent ON runtime_child_effect_grants(parent_run_id,grant_id)`,
		`CREATE INDEX runtime_child_grants_child ON runtime_child_effect_grants(child_run_id)`,
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
		// Immutable Feature-neutral child ownership; Run records need not exist
		// in this Store (matching Memory/PostgreSQL runrelation.Store semantics).
		`CREATE TABLE runtime_run_relations (
		child_run_id TEXT PRIMARY KEY,
		parent_run_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		owner_node_id TEXT NOT NULL,
		created_ns INTEGER NOT NULL,
		UNIQUE(parent_run_id, kind, owner_node_id)
	)`,
		`CREATE INDEX runtime_run_relations_parent_order ON runtime_run_relations(parent_run_id, created_ns, kind, owner_node_id, child_run_id)`,
	}, managedSchemaDDL...), ownedRetirementSchemaDDL...)
}

func errorCode(err error) int {
	var se *sqlite3.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	var extended ncruces.ExtendedErrorCode
	if errors.As(err, &extended) {
		return int(extended)
	}
	var code ncruces.ErrorCode
	if errors.As(err, &code) {
		return int(code)
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
