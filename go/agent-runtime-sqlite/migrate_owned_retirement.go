package sqlite

import (
	"context"
	"fmt"
	"strings"
)

var ownedRetirementSchemaDDL = []string{
	`CREATE TABLE runtime_owned_retirements (grant_id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runtime_runs(run_id) ON DELETE CASCADE, settled_revision INTEGER NOT NULL, version INTEGER NOT NULL, phase TEXT NOT NULL, payload_json BLOB NOT NULL)`,
	`CREATE INDEX runtime_owned_retirements_run ON runtime_owned_retirements(run_id)`,
}

func schemaV5DDL() []string {
	out := append([]string(nil), schemaDDL[:len(schemaDDL)-len(ownedRetirementSchemaDDL)]...)
	for i, ddl := range out {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			out[i] = strings.Replace(ddl, "\n\t\tsettlement_evidence_json BLOB,", "", 1)
		}
	}
	return out
}

// MigrateV5ToV6 is explicit: stop old writers and retain a backup first.
// It preserves all original marks/completions and creates no retirement fact.
func MigrateV5ToV6(ctx context.Context, path string) error {
	s, e := openStoreMigration(ctx, path, 6)
	if e != nil {
		return e
	}
	return s.Close()
}
func (s *Store) migrateV5ToV6(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return conflictOrDatabaseError(e)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if e = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); e != nil {
		return e
	}
	if version == 6 {
		return validateSchemaDDL(ctx, tx, schemaDDL)
	}
	if version != 5 {
		return fmt.Errorf("%w: migration requires exact v5, got %d", ErrUnsupportedSchema, version)
	}
	if e = validateSchemaDDL(ctx, tx, schemaV5DDL()); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `ALTER TABLE runtime_child_effect_grants RENAME TO runtime_child_effect_grants_v5`); e != nil {
		return e
	}
	for _, ddl := range schemaDDL {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_child_effect_grants ") {
			if _, e = tx.ExecContext(ctx, ddl); e != nil {
				return e
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO runtime_child_effect_grants(grant_id,parent_run_id,child_run_id,start_revision,settled_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation,callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned)
 SELECT grant_id,parent_run_id,child_run_id,start_revision,settled_revision,execution_owner_id,execution_lease_id,execution_epoch,admission_source_hash,admission_operation,callback_return_revision,callback_return_source_hash,callback_host_completed,callback_lease_owned FROM runtime_child_effect_grants_v5`); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DROP TABLE runtime_child_effect_grants_v5`); e != nil {
		return e
	}
	for _, ddl := range schemaDDL {
		if strings.HasPrefix(ddl, "CREATE INDEX runtime_child_grants_") {
			if _, e = tx.ExecContext(ctx, ddl); e != nil {
				return e
			}
		}
	}
	for _, ddl := range ownedRetirementSchemaDDL {
		if _, e = tx.ExecContext(ctx, ddl); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "PRAGMA user_version=6"); e != nil {
		return e
	}
	if e = validateSchemaDDL(ctx, tx, schemaDDL); e != nil {
		return e
	}
	return conflictOrDatabaseError(tx.Commit())
}
