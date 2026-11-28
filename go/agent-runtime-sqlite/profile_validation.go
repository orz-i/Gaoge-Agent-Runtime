package sqlite

import (
	"context"
	"fmt"
)

func validateFinalRetirements(ctx context.Context, q schemaQuery) error {
	var pending int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM runtime_managed_history WHERE json_type(CAST(payload_json AS TEXT),'$.State') IS NOT NULL
        OR json_type(CAST(payload_json AS TEXT),'$.Selection.ID') IS NOT NULL`).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("%w: retired managed wire requires a fresh profile", ErrUnsupportedSchema)
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM runtime_effects
        WHERE retirement_json<>'' AND (json_type(retirement_json,'$.Phase') IS NOT NULL
        OR json_type(retirement_json,'$.ACKIdentity') IS NOT NULL)`).Scan(&pending); err != nil {
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
