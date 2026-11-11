package harnesspostgres_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// The same original Turn lock serializes physical Host continuation
// admission and terminal status CAS on both SQL/SQLite and PostgreSQL.
func TestSQLTerminalProjectionCASRequiresOriginalContinuationProof(t *testing.T) {
	store, interaction, owner, proofs := newSQLInteractionAuthorityFixture(t, "terminal_projection_original_claim")
	for _, fact := range []harness.Item{proofs[0], proofs[1]} {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("original Host receipt and continuation Claim setup failed: fresh=%v err=%v", fresh, err)
		}
	}
	completedOwner := owner
	completedOwner.Status = harness.InvocationCompleted
	if _, err := store.UpdateInvocation(t.Context(), completedOwner, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("SQL Invocation terminalized an uncompleted continuation: %v", err)
	}
	turn, err := store.GetTurn(t.Context(), interaction.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	completedTurn := turn
	completedTurn.Status = harness.TurnCompleted
	if _, err := store.UpdateTurn(t.Context(), completedTurn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("SQL Turn terminalized an uncompleted continuation: %v", err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), proofs[2]); err != nil || !fresh {
		t.Fatalf("physical continuation finished but its durable completion was rejected: %v fresh=%v", err, fresh)
	}
	// The Host continuation receipt alone cannot authorize an
	// unrelated Kernel terminal outcome. Only a trusted original
	// terminal callback can carry the private Store CAS witness.
	if _, err := store.UpdateInvocation(t.Context(), completedOwner, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("SQL accepted Kernel terminal solely from Host callback receipt: %v", err)
	}
	if _, err := store.UpdateTurn(t.Context(), completedTurn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("SQL accepted Turn terminal solely from Host callback receipt: %v", err)
	}
}
