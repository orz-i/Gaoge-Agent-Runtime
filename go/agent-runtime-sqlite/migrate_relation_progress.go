package sqlite

import (
	"context"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
)

// MigrateV8ToV9 adds initially empty relation progress and Call return evidence. Stop all old writers and retain a backup before invoking.
// No original effect, callback or queue acknowledgement is inferred.
func MigrateV8ToV9(ctx context.Context, path string) error {
	return MigrateV8ToV9With(ctx, path, nil)
}

// MigrateV8ToV9With uses the original host-owned connection, including a
// keyed SQLite VFS where selected. Never run it implicitly from Open.
func MigrateV8ToV9With(ctx context.Context, path string, open profile.OpenConnection) error {
	s, err := kernelStore(open.OpenExisting(ctx, path, kernelSetup((*Store).migrateV8ToV9)))
	if err != nil {
		return err
	}
	return s.Close()
}

// Used by explicit migration, not normal Open or the scheduler.
func (s *Store) migrateV8ToV9(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 8 && version != 9 {
		return fmt.Errorf("%w: migration requires exact v8, got %d", ErrUnsupportedSchema, version)
	}
	ddl := schemaV8DDL()
	if version == 9 {
		ddl = schemaV9DDL()
	}
	if err = validateSchemaDDL(ctx, tx, ddl); err != nil {
		return err
	}
	if _, err = sqliteManagedAuthority(ctx, tx); err != nil {
		return err
	}
	if version == 9 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE runtime_child_effect_grants ADD COLUMN "+callbackToolReturnsColumn); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, relationProgressDDL); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=9"); err != nil {
		return err
	}
	if err = validateSchemaDDL(ctx, tx, schemaV9DDL()); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}
