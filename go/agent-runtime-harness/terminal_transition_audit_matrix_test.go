package harness

import (
	"testing"
	"time"
)

func auditTerminalMemoryRoot(t *testing.T, suffix string, status TurnStatus) (*MemoryStore, Turn, Invocation) {
	t.Helper()
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := Turn{ID: "audit-turn-" + suffix, SessionID: "audit-session-" + suffix, HostTurn: HostRef{Kind: "conversation_turn", ID: "host-" + suffix}, ConfigSnapshotID: "frozen-config", Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now}
	owner := Invocation{ID: "audit-root-" + suffix, TurnID: turn.ID, CapabilityKey: CapabilityAgent, ExecutionClass: ExecutionAgent, ExecutionRefID: "audit-run-" + suffix, Status: InvocationRunning, Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	store := NewMemoryStore()
	store.turns[turn.ID] = turn
	store.invocations[owner.ID] = owner
	return store, turn, owner
}
