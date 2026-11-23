// Package harnesssqlite persists Harness state in its own private SQLite file.
// Hosts can pair it with a Kernel SQLite Store in the same profile directory.
package harnesssqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"github.com/libtnb/sqlite"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnessdb "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The shared relational implementation owns all admission, CAS and receipt
// predicates. Its SQLite connections acquire BEGIN IMMEDIATE before any read
// in a write transaction, including when another process owns the other pool.
type relationalStore = harnessdb.Store

type Store struct {
	*relationalStore
	db *sql.DB
}

type TurnConfigQuery = harnessdb.TurnConfigQuery
type TurnConfigProjection = harnessdb.TurnConfigProjection

var (
	_ harness.Store                   = (*Store)(nil)
	_ budget.LedgerStore              = (*Store)(nil)
	_ kernel.ManagedRecoveryHostStore = (*Store)(nil)
)

// CreateNew publishes a fully initialized database without replacing any file.
func CreateNew(ctx context.Context, path string) (*Store, error) {
	return CreateNewWith(ctx, path, nil)
}

// CreateNewWith enrolls a Harness database through the chosen connection.
func CreateNewWith(ctx context.Context, path string, open profile.OpenConnection) (*Store, error) {
	return openStore(open.CreateNew(ctx, path, initialize, validate))
}

// OpenExisting verifies the schema and original recovery authority before use.
func OpenExisting(ctx context.Context, path string) (*Store, error) {
	return OpenExistingWith(ctx, path, nil)
}

// OpenExistingWith checks a Harness database through the chosen connection.
func OpenExistingWith(ctx context.Context, path string, open profile.OpenConnection) (*Store, error) {
	return openStore(open.OpenExisting(ctx, path, validate))
}

func orm(pool *sql.DB) (*gorm.DB, error) {
	// GORM uses the supplied pool and supplies only the SQL dialect.
	return gorm.Open(sqlite.Dialector{Conn: pool, DriverName: "sqlite"}, &gorm.Config{Logger: logger.Discard})
}

func openStore(pool *sql.DB, err error) (*Store, error) {
	if err != nil {
		return nil, err
	}
	db, err := orm(pool)
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	store, err := harnessdb.New(db)
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	return &Store{relationalStore: store, db: pool}, nil
}

// Close releases this handle. Other processes may retain their own handles.
func (s *Store) Close() error { return s.db.Close() }

// The application ID "GAHS" distinguishes Harness from Kernel and third-party
// databases. Each schema version pins all persisted table and index definitions.
const applicationID = 0x47414853
const schemaVersion = 3

//go:embed schema_v3.sql
var schemaV3 string

const schemaObjects = `SELECT sql FROM sqlite_schema
	WHERE substr(name,1,7) <> 'sqlite_' ORDER BY type,name`

func initialize(ctx context.Context, pool *sql.DB) error {
	var journal string
	if err := pool.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return err
	}
	if journal != "wal" {
		return fmt.Errorf("%w: journal mode %q", profile.ErrUnsupportedSchema, journal)
	}
	db, err := orm(pool)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := harnessdb.Migrate(tx); err != nil {
			return err
		}
		if err := tx.Exec(fmt.Sprintf("PRAGMA application_id=%d", applicationID)).Error; err != nil {
			return err
		}
		return tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)).Error
	})
}

func validate(ctx context.Context, pool *sql.DB) error {
	db, err := orm(pool)
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var id, version int
		var journal string
		if err := tx.Raw("PRAGMA application_id").Row().Scan(&id); err != nil {
			return err
		}
		if err := tx.Raw("PRAGMA user_version").Row().Scan(&version); err != nil {
			return err
		}
		if err := tx.Raw("PRAGMA journal_mode").Row().Scan(&journal); err != nil {
			return err
		}
		if id != applicationID || version != schemaVersion || journal != "wal" {
			return fmt.Errorf("%w: Harness application ID %x, version %d, journal %q", profile.ErrUnsupportedSchema, id, version, journal)
		}
		if err := checkSchema(tx); err != nil {
			return err
		}
		store, err := harnessdb.New(tx)
		if err != nil {
			return err
		}
		if _, err := store.ManagedRecoveryAuthority(ctx); err != nil {
			if errors.Is(err, harness.ErrNotFound) || errors.Is(err, harness.ErrConflict) {
				return fmt.Errorf("%w: original Harness authority: %w", profile.ErrUnsupportedSchema, err)
			}
			return err
		}
		return nil
	})
}

func checkSchema(tx *gorm.DB) error {
	rows, err := tx.Raw(schemaObjects).Rows()
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for definition := range strings.Lines(schemaV3) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return fmt.Errorf("%w: missing Harness schema object", profile.ErrUnsupportedSchema)
		}
		var statement string
		if err := rows.Scan(&statement); err != nil {
			return err
		}
		if statement+";" != strings.TrimRight(definition, "\r\n") {
			return fmt.Errorf("%w: unexpected Harness schema definition", profile.ErrUnsupportedSchema)
		}
	}
	if rows.Next() {
		return fmt.Errorf("%w: extra Harness schema object", profile.ErrUnsupportedSchema)
	}
	return rows.Err()
}
