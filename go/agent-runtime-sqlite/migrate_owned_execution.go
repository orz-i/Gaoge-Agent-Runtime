package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// MigrateV3ToV4 explicitly upgrades an exact private v3 database. OpenExisting
// never migrates implicitly. Callers must stop old writers and retain a backup;
// no outstanding physical grant is removed or guessed into an owned binding.
func MigrateV3ToV4(ctx context.Context, path string) error {
	store, err := openStoreMode(ctx, path, false, true)
	if err != nil {
		return err
	}
	return store.Close()
}
func schemaV3DDL() []string {
	result := append([]string(nil), schemaV4DDL()...)
	for i, ddl := range result {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			for _, line := range []string{"\n\t\texecution_owner_id TEXT NOT NULL DEFAULT '',", "\n\t\texecution_lease_id TEXT NOT NULL DEFAULT '',", "\n\t\texecution_epoch INTEGER NOT NULL DEFAULT 0,", "\n\t\tadmission_source_hash TEXT NOT NULL DEFAULT '',", "\n\t\tadmission_operation TEXT NOT NULL DEFAULT '',", "\n\t\tcallback_return_revision INTEGER NOT NULL DEFAULT 0,", "\n\t\tcallback_return_source_hash TEXT NOT NULL DEFAULT '',", "\n\t\tcallback_host_completed INTEGER NOT NULL DEFAULT 0,", "\n\t\tcallback_lease_owned INTEGER NOT NULL DEFAULT 0,"} {
				ddl = strings.ReplaceAll(ddl, line, "")
			}
			result[i] = ddl
		}
	}
	return result
}
func (s *Store) migrateV3ToV4(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 4 {
		return validateSchemaDDL(ctx, tx, schemaV4DDL())
	}
	if version != 3 {
		return fmt.Errorf("%w: migration requires exact v3, got %d", ErrUnsupportedSchema, version)
	}
	if err = validateSchemaDDL(ctx, tx, schemaV3DDL()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE runtime_child_effect_grants RENAME TO runtime_child_effect_grants_v3"); err != nil {
		return err
	}
	for _, ddl := range schemaV4DDL() {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,settled_revision)
 SELECT grant_id,parent_run_id,child_run_id,start_revision,settled_revision FROM runtime_child_effect_grants_v3`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE runtime_child_effect_grants_v3"); err != nil {
		return err
	}
	for _, ddl := range schemaV4DDL() {
		if strings.HasPrefix(ddl, "CREATE INDEX runtime_child_grants_") {
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
		return err
	}
	if err = validateSchemaDDL(ctx, tx, schemaV4DDL()); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}
