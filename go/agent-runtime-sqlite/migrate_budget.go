package sqlite

import (
	"context"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget/sqlledger"
)

// MigrateV10ToV11 enrolls the native ledger table without moving, deleting or
// inventing any Host accounting facts. Stop all writers and back up first.
// Existing Harness budget ledgers must be transferred and verified separately
// before a Host enables the native accounting path. Existing v11 files also
// must pass the native Run binding check; this function never backfills it.
func MigrateV10ToV11(ctx context.Context, path string) error {
	return MigrateV10ToV11With(ctx, path, nil)
}
func MigrateV10ToV11With(ctx context.Context, path string, open profile.OpenConnection) error {
	s, err := kernelStore(open.OpenExisting(ctx, path, kernelSetup((*Store).migrateV10ToV11)))
	if err != nil {
		return err
	}
	return s.Close()
}
func (s *Store) migrateV10ToV11(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 11 {
		if err = validateSchemaDDL(ctx, tx, schemaDDL); err != nil {
			return err
		}
		if err = validateFinalRetirements(ctx, tx); err != nil {
			return err
		}
		return validateNativeBudgetBindings(ctx, tx)
	}
	if version != 10 {
		return fmt.Errorf("%w: migration requires exact v10, got %d", ErrUnsupportedSchema, version)
	}
	if err = validateSchemaDDL(ctx, tx, schemaV10DDL()); err != nil {
		return err
	}
	if _, err = sqliteManagedAuthority(ctx, tx); err != nil {
		return err
	}
	if err = validateFinalRetirements(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, sqlledger.SQLiteDDL); err != nil {
		return conflictOrDatabaseError(err)
	}
	if _, err = tx.ExecContext(ctx, runIdentityDDL); err != nil {
		return conflictOrDatabaseError(err)
	}

	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=11"); err != nil {
		return conflictOrDatabaseError(err)
	}
	if err = validateSchemaDDL(ctx, tx, schemaDDL); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}

// Stop old writers before opening the atomic retirement contract. A legacy
// intermediate row may still own its lease; never erase or auto-ack it.
func validateFinalRetirements(ctx context.Context, q schemaQuery) error {
	var pending int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM runtime_owned_retirements WHERE phase <> 'ACKNOWLEDGED'`).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("%w: %d unfinished original retirements; drain with the previous writer", ErrUnsupportedSchema, pending)
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM (
        SELECT payload_json FROM runtime_managed_transfers
        UNION ALL SELECT payload_json FROM runtime_managed_history
        ) WHERE json_type(CAST(payload_json AS TEXT),'$.State') IS NOT NULL
        OR json_type(CAST(payload_json AS TEXT),'$.Selection.ID') IS NOT NULL`).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("%w: retired managed wire requires a fresh profile", ErrUnsupportedSchema)
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM runtime_owned_retirements
        WHERE json_type(CAST(payload_json AS TEXT),'$.Phase') IS NOT NULL
        OR json_type(CAST(payload_json AS TEXT),'$.ACKIdentity') IS NOT NULL`).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("%w: retired receipt wire requires a fresh profile", ErrUnsupportedSchema)
	}
	return nil
}

// The native ledger cannot contain an existing live Agent without its
// original Run binding. No historical charge or execution fact is inferred
// or rewritten by Open or migration.
func validateNativeBudgetBindings(ctx context.Context, q schemaQuery) error {
	var invalid int
	err := q.QueryRowContext(ctx, `
		WITH members AS (
			SELECT b.id AS scope, x.key AS run_id, COALESCE(json_extract(x.value,'$.parentRunID'),'') AS parent
			FROM agent_harness_budgets b, json_each(b.payload_json,'$.runs') x
		), runs AS (
			SELECT run_id, kind, status,
				COALESCE(json_extract(CAST(run_json AS TEXT),'$.budgetScopeID'),'') AS scope,
				COALESCE(json_extract(CAST(run_json AS TEXT),'$.logicalParentRunID'),'') AS parent
			FROM runtime_runs
		)
		SELECT COUNT(*) FROM (
			SELECT r.run_id FROM runs r JOIN members m ON m.run_id=r.run_id
			WHERE (r.scope<>'' OR (r.kind='agent' AND r.status IN ('running','waiting_input','paused_budget')))
			  AND (r.scope<>m.scope OR r.parent<>m.parent)
			UNION ALL
			SELECT r.run_id FROM runs r WHERE r.scope<>''
			 AND NOT EXISTS (SELECT 1 FROM members m WHERE m.run_id=r.run_id AND m.scope=r.scope AND m.parent=r.parent)
		) invalid`).Scan(&invalid)
	if err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("%w: %d inconsistent native budget bindings; reconcile with previous writer", ErrUnsupportedSchema, invalid)
	}
	return nil
}
