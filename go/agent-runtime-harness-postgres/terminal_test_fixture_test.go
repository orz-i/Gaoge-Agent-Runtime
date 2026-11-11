package harnesspostgres

import (
	"context"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/gorm"
)

// TerminalFixtureInvocationForTest intentionally exists ONLY inside
// *_test.go: external-package harnesspostgres_test can seed a historical
// terminal row without pretending that a fresh, unproven Host Store CAS is
// authorized. It is never linked into production packages or consumers.
// It must never be used in a test of real terminal admission itself.
func TerminalFixtureInvocationForTest(
	ctx context.Context, store *Store, value harness.Invocation, expected uint64,
) (harness.Invocation, error) {
	if store == nil || expected == 0 {
		return harness.Invocation{}, harness.ErrInvalidRequest
	}
	record, err := invocationToRecord(value)
	if err != nil {
		return harness.Invocation{}, err
	}
	result := store.db.WithContext(ctx).Model(&invocationRecord{}).
		Where("id = ? AND revision = ? AND turn_id = ? AND execution_ref_id = ? AND attempt = ?",
			value.ID, expected, value.TurnID, value.ExecutionRefID, value.Attempt).
		Updates(map[string]any{
			"status": string(value.Status), "revision": expected + 1,
			"error_code": record.ErrorCode, "error_detail": record.ErrorDetail,
			"output_refs_json": record.OutputRefsJSON, "updated_at": record.UpdatedAt,
		})
	if result.Error != nil {
		return harness.Invocation{}, result.Error
	}
	if result.RowsAffected != 1 {
		return harness.Invocation{}, harness.ErrConflict
	}
	return store.GetInvocation(ctx, value.ID)
}

// TerminalFixtureTurnForTest deliberately inserts historical terminal
// state outside the ordinary authority CAS in test-only fixtures.
// This is a privileged crash/recovery setup, not an original authorization.
func TerminalFixtureTurnForTest(
	ctx context.Context, store *Store, value harness.Turn, expected uint64,
) (harness.Turn, error) {
	if store == nil || expected == 0 {
		return harness.Turn{}, harness.ErrInvalidRequest
	}
	result := store.db.WithContext(ctx).Model(&turnRecord{}).
		Where("id = ? AND revision = ? AND session_id = ?", value.ID, expected, value.SessionID).
		Updates(map[string]any{
			"status": string(value.Status), "revision": expected + 1,
			"error_code": value.ErrorCode, "error_detail": value.ErrorDetail,
			"updated_at": value.UpdatedAt,
		})
	if result.Error != nil {
		return harness.Turn{}, result.Error
	}
	if result.RowsAffected != 1 {
		return harness.Turn{}, harness.ErrConflict
	}
	return store.GetTurn(ctx, value.ID)
}

// CheckTerminalProjectionForTest calls the real read-only terminal proof in
// the original Store's Turn transaction, but never performs a terminal CAS.
// It is test-only and is not linked into any production binary.
func CheckTerminalProjectionForTest(
	ctx context.Context, store *Store, turnID, invocationID string,
) error {
	if store == nil {
		return harness.ErrInvalidRequest
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAdmissionTurn(tx, turnID); err != nil {
			return err
		}
		return checkTerminalProjectionTx(ctx, tx, turnID, invocationID)
	})
}
