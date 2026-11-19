package sqlite

import (
	"context"
	"fmt"
	"strings"
)

func schemaV6DDL() []string {
	out := append([]string(nil), schemaDDL...)
	for i, ddl := range out {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			out[i] = strings.Replace(ddl, "\n\t\tcallback_control_custody TEXT NOT NULL DEFAULT '',", "", 1)
		}
	}
	return out
}

// MigrateV6ToV7 preserves every original callback/retirement fact. Old writers
// must be stopped and a backup retained. Unknown old custody stays unknown;
// migration cannot manufacture the actual executor's first-party attestation.
func MigrateV6ToV7(ctx context.Context, path string) error {
	s, err := openStoreMigration(ctx, path, 7)
	if err != nil {
		return err
	}
	return s.Close()
}

func (s *Store) migrateV6ToV7(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 7 {
		return validateSchemaDDL(ctx, tx, schemaDDL)
	}
	if version != 6 {
		return fmt.Errorf("%w: migration requires exact v6, got %d", ErrUnsupportedSchema, version)
	}
	if err = validateSchemaDDL(ctx, tx, schemaV6DDL()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE runtime_child_effect_grants RENAME TO runtime_child_effect_grants_v6"); err != nil {
		return err
	}
	for _, ddl := range schemaDDL {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	const columns = "grant_id,parent_run_id,child_run_id,start_revision,settled_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation,callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned,settlement_evidence_json"
	if _, err = tx.ExecContext(ctx, "INSERT INTO runtime_child_effect_grants("+columns+") SELECT "+columns+" FROM runtime_child_effect_grants_v6"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE runtime_child_effect_grants_v6"); err != nil {
		return err
	}
	for _, ddl := range schemaDDL {
		if strings.HasPrefix(ddl, "CREATE INDEX runtime_child_grants_") {
			if _, err = tx.ExecContext(ctx, ddl); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=7"); err != nil {
		return err
	}
	if err = validateSchemaDDL(ctx, tx, schemaDDL); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}
