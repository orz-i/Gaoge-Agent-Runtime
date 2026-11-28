package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// MigrateV11ToV12 removes the redundant current index only after proving
// every pointer names the unique latest original history. Stop old writers
// and retain a backup. No Run, effect, quota or history row is rewritten.
func MigrateV11ToV12(ctx context.Context, path string) error {
	return MigrateV11ToV12With(ctx, path, nil)
}
func MigrateV11ToV12With(ctx context.Context, path string, open profile.OpenConnection) error {
	s, e := kernelStore(open.OpenExisting(ctx, path, kernelSetup((*Store).migrateV11ToV12)))
	if e != nil {
		return e
	}
	return s.Close()
}
func (s *Store) migrateV11ToV12(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	var v int
	if e = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); e != nil {
		return e
	}
	if v == 12 {
		return validateSchemaDDL(ctx, tx, schemaDDL)
	}
	if v != 11 {
		return fmt.Errorf("%w: migration requires v11, got %d", ErrUnsupportedSchema, v)
	}
	if e = validateSchemaDDL(ctx, tx, schemaV11DDL()); e != nil {
		return e
	}
	if _, e = sqliteManagedAuthority(ctx, tx); e != nil {
		return e
	}
	if e = validateFinalRetirements(ctx, tx); e != nil {
		return e
	}
	if e = validateNativeBudgetBindings(ctx, tx); e != nil {
		return e
	}
	if e = validateManagedCurrentV11(ctx, tx); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, managedRevisionIndexDDL); e != nil {
		return conflictOrDatabaseError(e)
	}
	if _, e = tx.ExecContext(ctx, "DROP TABLE runtime_managed_transfers"); e != nil {
		return conflictOrDatabaseError(e)
	}
	if _, e = tx.ExecContext(ctx, "PRAGMA user_version=12"); e != nil {
		return e
	}
	if e = validateSchemaDDL(ctx, tx, schemaDDL); e != nil {
		return e
	}
	return conflictOrDatabaseError(tx.Commit())
}
func validateManagedCurrentV11(ctx context.Context, tx *sql.Tx) error {
	rows, e := tx.QueryContext(ctx, `SELECT c.run_id,c.suspension_id,c.recovery_id,c.revision,c.payload_json FROM runtime_managed_transfers c`)
	if e != nil {
		return e
	}
	type pointer struct {
		run, id, recovery string
		revision          uint64
		raw               []byte
	}
	var pointers []pointer
	for rows.Next() {
		var p pointer
		if e = rows.Scan(&p.run, &p.id, &p.recovery, &p.revision, &p.raw); e != nil {
			rows.Close()
			return e
		}
		pointers = append(pointers, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var runs int
	if e = tx.QueryRowContext(ctx, "SELECT count(DISTINCT run_id) FROM runtime_managed_history").Scan(&runs); e != nil {
		return e
	}
	if runs != len(pointers) {
		return fmt.Errorf("%w: Managed history has missing current index", ErrUnsupportedSchema)
	}
	for _, p := range pointers {
		t, e := sqliteLoadManaged(ctx, tx, p.run)
		if e != nil || t.SafePoint.ID != p.id || t.SafePoint.RecoveryID != p.recovery || t.Revision != p.revision {
			return fmt.Errorf("%w: Managed index does not name latest history", ErrUnsupportedSchema)
		}
		if string(p.raw) != "null" {
			var old kernel.ManagedTransfer
			if e = json.Unmarshal(p.raw, &old); e != nil || kernel.ManagedDigest(old) != kernel.ManagedDigest(t) {
				return fmt.Errorf("%w: conflicting redundant Managed payload", ErrUnsupportedSchema)
			}
		}
	}
	return nil
}
