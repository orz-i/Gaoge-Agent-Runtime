package harnesspostgres

import (
	"context"
	"encoding/json"
	"strings"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// TurnConfigQuery selects explicit Turn IDs under a required tenant and actor.
// The host must authorize this scope before reading frozen configuration.
type TurnConfigQuery struct {
	TenantID, ActorID, HostThreadKind string
	TurnIDs                           []string
}
type TurnConfigProjection struct {
	TurnID string
	Config harness.ConfigSnapshot
}

// ListTurnConfigs reads the original Turn-owned configuration, never an
// unrelated snapshot with a coincidentally matching identifier.
func (store *Store) ListTurnConfigs(ctx context.Context, q TurnConfigQuery) ([]TurnConfigProjection, error) {
	if store == nil || store.db == nil || q.TenantID == "" || q.ActorID == "" ||
		strings.TrimSpace(q.TenantID) != q.TenantID || strings.TrimSpace(q.ActorID) != q.ActorID ||
		len(q.TurnIDs) == 0 || len(q.TurnIDs) > 1000 {
		return nil, harness.ErrInvalidRequest
	}
	db := store.db.WithContext(ctx).Table(turnRecord{}.TableName()+" AS turns").
		Joins("JOIN "+sessionRecord{}.TableName()+" AS sessions ON sessions.id = turns.session_id").
		Joins("JOIN "+configRecord{}.TableName()+" AS configs ON configs.id = turns.config_snapshot_id AND configs.turn_id = turns.id").
		Where("sessions.tenant_id = ? AND sessions.actor_id = ? AND turns.id IN ?", q.TenantID, q.ActorID, q.TurnIDs)
	if q.HostThreadKind != "" {
		db = db.Where("sessions.host_thread_kind = ?", q.HostThreadKind)
	}
	var rows []struct {
		TurnID      string
		PayloadJSON string
	}
	if err := db.Select("turns.id AS turn_id, configs.payload_json").Order("turns.id ASC").Scan(&rows).Error; err != nil {
		return nil, mapError(err)
	}
	result := make([]TurnConfigProjection, 0, len(rows))
	for _, row := range rows {
		var value harness.ConfigSnapshot
		if err := json.Unmarshal([]byte(row.PayloadJSON), &value); err != nil {
			return nil, harness.ErrConflict
		}
		if value.TurnID != row.TurnID {
			return nil, harness.ErrConflict
		}
		result = append(result, TurnConfigProjection{TurnID: row.TurnID, Config: value})
	}
	return result, nil
}
