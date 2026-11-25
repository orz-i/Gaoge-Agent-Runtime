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
	for _, query := range []string{
		"INSERT INTO runtime_grant_identities(grant_id,run_id) SELECT DISTINCT grant_id,child_run_id FROM runtime_child_effect_grants",
		"INSERT INTO runtime_grant_identities(grant_id,run_id) SELECT grant_id,run_id FROM runtime_owned_retirements",
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return conflictOrDatabaseError(err)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT run_id,payload_json FROM runtime_managed_history")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var ids []kernel.GrantIdentity
	for rows.Next() {
		var runID string
		var raw []byte
		if err = rows.Scan(&runID, &raw); err != nil {
			return err
		}
		found, e := kernel.ManagedGrantIdentities(raw, runID)
		if e != nil {
			return e
		}
		ids = append(ids, found...)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, i := range ids {
		if _, err = tx.ExecContext(ctx, "INSERT INTO runtime_grant_identities(grant_id,run_id) VALUES(?,?) ON CONFLICT(grant_id) DO NOTHING", i.GrantID, i.RunID); err != nil {
			return err
		}
		var owner string
		if err = tx.QueryRowContext(ctx, "SELECT run_id FROM runtime_grant_identities WHERE grant_id=?", i.GrantID).Scan(&owner); err != nil {
			return err
		}
		if owner != i.RunID {
			return kernel.ErrExecutionConflict
		}
	}
	return nil
}
