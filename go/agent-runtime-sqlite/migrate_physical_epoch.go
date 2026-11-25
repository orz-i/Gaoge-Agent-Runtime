package sqlite

import (
	"context"
	"fmt"
	"strings"
)

func schemaV7DDL() []string {
	out := schemaV8DDL()
	for i, ddl := range out {
		if strings.HasPrefix(ddl, "CREATE TABLE runtime_runs ") {
			out[i] = strings.Replace(ddl, ", execution_physical_epoch INTEGER NOT NULL DEFAULT 0)", ")", 1)
		}
	}
	return out
}

// MigrateV7ToV8 explicitly adds the physical-admission watermark. Stop all old
// writers and retain a backup first. Existing callback/ACK bytes are preserved;
// the watermark is derived ONLY from original admission/retirement records,
// never a native lease epoch or the new observer Runner's configuration.
func MigrateV7ToV8(ctx context.Context, path string) error {
	s, err := openKernel(ctx, path, kernelSetup((*Store).migrateV7ToV8))
	if err != nil {
		return err
	}
	return s.Close()
}

func (s *Store) migrateV7ToV8(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 8 {
		return validateSchemaDDL(ctx, tx, schemaV8DDL())
	}
	if version != 7 {
		return fmt.Errorf("%w: migration requires exact v7, got %d", ErrUnsupportedSchema, version)
	}
	if err = validateSchemaDDL(ctx, tx, schemaV7DDL()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "ALTER TABLE runtime_runs ADD COLUMN execution_physical_epoch INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// Original Store rows are trusted durable facts. No missing/legacy custody
	// is upgraded: this column records physical admission, not safe settlement.
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_runs SET execution_physical_epoch=COALESCE((
		SELECT MAX(epoch) FROM (
			SELECT execution_epoch AS epoch FROM runtime_child_effect_grants WHERE child_run_id=runtime_runs.run_id AND execution_epoch>0
			UNION ALL
			SELECT json_extract(payload_json,'$.Evidence.Grant.ExecutionEpoch') AS epoch FROM runtime_owned_retirements WHERE run_id=runtime_runs.run_id
		)
	),0)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=8"); err != nil {
		return err
	}
	if err = validateSchemaDDL(ctx, tx, schemaV8DDL()); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}
