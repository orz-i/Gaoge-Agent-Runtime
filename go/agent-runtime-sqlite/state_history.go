package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel/statehistory"
)

func sqliteAppendHistory(ctx context.Context, tx *sql.Tx, id string, deltas []statehistory.Append) error {
	for _, d := range deltas {
		for i, item := range d.Entries {
			if _, e := tx.ExecContext(ctx, "INSERT INTO runtime_state_history(run_id,stream,generation,ordinal,payload_json) VALUES(?,?,?,?,?)", id, d.Stream, d.Generation, d.First+i, []byte(item)); e != nil {
				return conflictOrDatabaseError(e)
			}
		}
	}
	return nil
}
func sqliteReadHistory(ctx context.Context, q schemaQuery, id string, stream int, gen uint64, count int) ([]json.RawMessage, error) {
	rows, e := q.QueryContext(ctx, "SELECT ordinal,payload_json FROM runtime_state_history WHERE run_id=? AND stream=? AND generation=? AND ordinal<? ORDER BY ordinal", id, stream, gen, count)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var items []json.RawMessage
	for rows.Next() {
		var ordinal int
		var raw []byte
		if e = rows.Scan(&ordinal, &raw); e != nil {
			return nil, e
		}
		if ordinal != len(items) {
			return nil, ErrInvalidRecord
		}
		items = append(items, json.RawMessage(raw))
	}
	return items, rows.Err()
}
