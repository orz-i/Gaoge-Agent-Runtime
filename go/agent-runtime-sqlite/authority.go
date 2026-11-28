package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

func initializeManagedAuthority(ctx context.Context, tx *sql.Tx) error {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO runtime_managed_authority(authority_key,namespace,generation,protocol_version) VALUES(?,?,1,1) ON CONFLICT(authority_key) DO NOTHING`, "managed-recovery-authority/v1", hex.EncodeToString(token[:]))
	return err
}
