package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
)

var managedSchemaDDL = []string{
	`CREATE TABLE runtime_managed_history (suspension_id TEXT PRIMARY KEY, recovery_id TEXT NOT NULL UNIQUE, run_id TEXT NOT NULL REFERENCES runtime_runs(run_id), revision INTEGER NOT NULL, payload_json BLOB NOT NULL)`,
	`CREATE TABLE runtime_managed_authority (authority_key TEXT PRIMARY KEY, namespace TEXT NOT NULL, generation INTEGER NOT NULL, protocol_version INTEGER NOT NULL)`,
	`CREATE TABLE runtime_managed_transfers (run_id TEXT PRIMARY KEY REFERENCES runtime_runs(run_id), suspension_id TEXT NOT NULL UNIQUE, recovery_id TEXT NOT NULL UNIQUE, revision INTEGER NOT NULL, payload_json BLOB NOT NULL)`,
}

func initializeManagedAuthority(ctx context.Context, tx *sql.Tx) error {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_managed_authority(authority_key,namespace,generation,protocol_version) VALUES(?,?,1,1) ON CONFLICT(authority_key) DO NOTHING`, "managed-recovery-authority/v1", hex.EncodeToString(token[:]))
	return err
}
func schemaV4DDL() []string { v5 := schemaV5DDL(); return v5[:len(v5)-len(managedSchemaDDL)] }

// MigrateV4ToV5 is explicit. Stop old writers and retain a backup first.
// No existing Run receives a suspension proof from this schema migration.
func MigrateV4ToV5(ctx context.Context, path string) error {
	s, err := openStoreMigration(ctx, path, 5)
	if err != nil {
		return err
	}
	return s.Close()
}
func (s *Store) migrateV4ToV5(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conflictOrDatabaseError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 5 {
		return validateSchemaDDL(ctx, tx, schemaV5DDL())
	}
	if version != 4 {
		return fmt.Errorf("%w: migration requires exact v4, got %d", ErrUnsupportedSchema, version)
	}
	if err = validateSchemaDDL(ctx, tx, schemaV4DDL()); err != nil {
		return err
	}
	for _, ddl := range managedSchemaDDL {
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	if err = initializeManagedAuthority(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA user_version=5"); err != nil {
		return err
	}
	if err = validateSchemaDDL(ctx, tx, schemaV5DDL()); err != nil {
		return err
	}
	return conflictOrDatabaseError(tx.Commit())
}
