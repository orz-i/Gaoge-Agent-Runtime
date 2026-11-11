package harness

import (
	"errors"
	"testing"
	"time"
)

// Both ordinary Refresh and direct Store CAS must compete with the original
// physical continuation claim under one mutex. A claimed Runtime segment
// without its completion receipt is not a terminal Invocation or Turn.
func TestMemoryTerminalProjectionCASRequiresOriginalContinuationProof(t *testing.T) {
	store, interaction, owner, response, claim, completed := newAuthorityMemoryStore()
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	owner.CapabilityKey = CapabilityAgent
	owner.ExecutionClass = ExecutionAgent
	owner.Attempt, owner.Revision = 1, 1
	owner.CreatedAt, owner.UpdatedAt = now, now
	store.invocations[owner.ID] = owner
	turn := Turn{
		ID: interaction.TurnID, SessionID: "original-session",
		HostTurn:         HostRef{Kind: "conversation_turn", ID: "turn-origin"},
		ConfigSnapshotID: "frozen-config", Status: TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	store.turns[turn.ID] = turn
	for _, fact := range []Item{response, claim} {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("seed original committed Host receipt and continuation Claim: %v fresh=%v", err, fresh)
		}
	}
	terminalOwner := owner
	terminalOwner.Status = InvocationCompleted
	if _, err := store.UpdateInvocation(t.Context(), terminalOwner, owner.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("an uncompleted continuation authorized Invocation terminal CAS: %v", err)
	}
	terminalTurn := turn
	terminalTurn.Status = TurnCompleted
	if _, err := store.UpdateTurn(t.Context(), terminalTurn, turn.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("an uncompleted continuation authorized Turn terminal CAS: %v", err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), completed); err != nil || !fresh {
		t.Fatalf("original continuation completion could not commit after rejected terminal CAS: %v fresh=%v", err, fresh)
	}
	// A durable continuation-completed receipt proves the callback
	// returned, not that an unrelated Kernel Run became terminal. Only
	// the original Kernel/App result caller can mint terminal projection.
	if _, err := store.UpdateInvocation(t.Context(), terminalOwner, owner.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("callback completion alone authorized unrelated terminal Invocation: %v", err)
	}
	if _, err := store.UpdateTurn(t.Context(), terminalTurn, turn.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("callback completion alone authorized unrelated terminal Turn: %v", err)
	}
}
