package harness

import (
	"context"
	"strings"
)

// StateSnapshot is the bounded, read-only Harness Session/Turn/config projection.
// It does not scan Timeline Items, derive subtasks, or query the Kernel; it may
// lag a running Runtime until an explicit Refresh reconciles committed facts.
// Use Load for the complete snapshot, or ListTimeline for paged durable Items.
type StateSnapshot struct {
	Session Session        `json:"session"`
	Turn    Turn           `json:"turn"`
	Config  ConfigSnapshot `json:"config"`
}

func (runner *Runner) LoadState(ctx context.Context, turnID string) (StateSnapshot, error) {
	if runner == nil || runner.store == nil || strings.TrimSpace(turnID) == "" {
		return StateSnapshot{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return StateSnapshot{}, err
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return StateSnapshot{}, err
	}
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return StateSnapshot{}, err
	}
	if config.TurnID != turn.ID || session.ID != turn.SessionID {
		return StateSnapshot{}, ErrConflict
	}
	return StateSnapshot{
		Session: cloneSession(session), Turn: turn, Config: cloneConfigSnapshot(config),
	}, nil
}

// ListTimeline returns one bounded, cursor-ordered page of committed Harness
// Items. This never dispatches or mutates a Runtime, and avoids complete-history
// materialization for ordinary clients. The caller must authorize this Turn.
func (runner *Runner) ListTimeline(ctx context.Context, turnID string, afterSeq uint64, limit int) ([]Item, error) {
	turnID = strings.TrimSpace(turnID)
	if runner == nil || runner.store == nil || turnID == "" || limit <= 0 || limit > defaultItemListLimit {
		return nil, ErrInvalidRequest
	}
	if _, err := runner.store.GetTurn(ctx, turnID); err != nil {
		return nil, err
	}
	page, err := runner.store.ListItems(ctx, turnID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	for _, item := range page {
		if item.TurnID != turnID || item.Seq <= afterSeq {
			return nil, ErrConflict
		}
		afterSeq = item.Seq
	}
	if len(page) > limit {
		return nil, ErrConflict
	}
	return cloneItems(page), nil
}
