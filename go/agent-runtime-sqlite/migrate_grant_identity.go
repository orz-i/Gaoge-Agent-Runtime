package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// MigrateV9ToV10 reserves identities still provable from existing facts.
// Stop all old writers and back up the database. No Grant is recreated.
func MigrateV9ToV10(ctx context.Context, path string) error {
	return MigrateV9ToV10With(ctx, path, nil)
}

func MigrateV9ToV10With(ctx context.Context, path string, open profile.OpenConnection) error {
	s, err := kernelStore(open.OpenExisting(ctx, path, kernelSetup((*Store).migrateV9ToV10)))
	if err != nil {
		return err
	}
	return s.Close()
}

func (s *Store) migrateV9ToV10(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 10 {
		return validateSchemaDDL(ctx, tx, schemaDDL)
	}
	if version != 9 {
		return fmt.Errorf("%w: migration requires exact v9, got %d", ErrUnsupportedSchema, version)
	}
	if err = validateSchemaDDL(ctx, tx, schemaV9DDL()); err != nil {
		return err
	}
	if _, err = sqliteManagedAuthority(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, grantIdentityDDL); err != nil {
		return err
	}
	if err = sqliteBackfillGrantIdentities(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=10"); err != nil {
		return err
	}
	if err = validateSchemaDDL(ctx, tx, schemaDDL); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}

func sqliteBackfillGrantIdentities(ctx context.Context, tx *sql.Tx) error {
	queryIdentities := func(query string) ([]kernel.GrantIdentity, error) {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, conflictOrDatabaseError(err)
		}
		defer func() { _ = rows.Close() }()
		var out []kernel.GrantIdentity
		for rows.Next() {
			var i kernel.GrantIdentity
			if err = rows.Scan(&i.GrantID, &i.RunID); err != nil {
				return nil, conflictOrDatabaseError(err)
			}
			out = append(out, i)
		}
		if err = rows.Err(); err != nil {
			return nil, conflictOrDatabaseError(err)
		}
		if err = rows.Close(); err != nil {
			return nil, conflictOrDatabaseError(err)
		}
		return out, nil
	}
	active, err := queryIdentities("SELECT DISTINCT grant_id, child_run_id FROM runtime_child_effect_grants")
	if err != nil {
		return err
	}
	retired, err := queryIdentities("SELECT grant_id, run_id FROM runtime_owned_retirements")
	if err != nil {
		return err
	}
	var managed []kernel.GrantIdentity
	for _, query := range []string{
		"SELECT run_id, payload_json FROM runtime_managed_history",
		"SELECT run_id, payload_json FROM runtime_managed_transfers",
	} {
		rows, qErr := tx.QueryContext(ctx, query)
		if qErr != nil {
			return conflictOrDatabaseError(qErr)
		}
		for rows.Next() {
			var runID string
			var raw []byte
			if err = rows.Scan(&runID, &raw); err != nil {
				_ = rows.Close()
				return conflictOrDatabaseError(err)
			}
			found, e := kernel.ManagedGrantIdentities(raw, runID)
			if e != nil {
				_ = rows.Close()
				return e
			}
			managed = append(managed, found...)
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return conflictOrDatabaseError(err)
		}
		if err = rows.Close(); err != nil {
			return conflictOrDatabaseError(err)
		}
	}
	ids, err := kernel.MergeGrantIdentities(active, retired, managed)
	if err != nil {
		return err
	}
	for _, i := range ids {
		if _, err = tx.ExecContext(ctx, "INSERT INTO runtime_grant_identities(grant_id,run_id) VALUES(?,?) ON CONFLICT(grant_id) DO NOTHING", i.GrantID, i.RunID); err != nil {
			return conflictOrDatabaseError(err)
		}
		var owner string
		if err = tx.QueryRowContext(ctx, "SELECT run_id FROM runtime_grant_identities WHERE grant_id=?", i.GrantID).Scan(&owner); err != nil {
			return conflictOrDatabaseError(err)
		}
		if owner != i.RunID {
			return kernel.ErrExecutionConflict
		}
	}
	return nil
}
