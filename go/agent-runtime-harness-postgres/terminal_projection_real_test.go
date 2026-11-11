package harnesspostgres_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Two separate physical SQL connections must agree on the exact ordered
// Host callback → continuation claim → continuation completion boundary.
// A terminal Turn/Invocation CAS cannot race an unconfirmed continuation
// into an irreversible product-complete state.
func TestRealPostgresTerminalProjectionAndContinuationShareTurnLock(t *testing.T) {
	first, second, _ := realAuthorityStorePair(t)
	interaction, owner, facts := seedSQLInteractionAuthorityFixture(t, first, "terminal_cas_second_connection")
	for _, fact := range []harness.Item{facts[0], facts[1]} {
		if _, fresh, err := first.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("original Host callback and physical continuation claim: fresh=%t err=%v", fresh, err)
		}
	}
	target := owner
	target.Status = harness.InvocationCompleted
	if _, err := second.UpdateInvocation(t.Context(), target, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection committed terminal Invocation while original physical continuation was unconfirmed: %v", err)
	}
	turn, err := second.GetTurn(t.Context(), interaction.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	completedTurn := turn
	completedTurn.Status = harness.TurnCompleted
	if _, err := second.UpdateTurn(t.Context(), completedTurn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection committed terminal Turn while original physical continuation was unconfirmed: %v", err)
	}
	if _, fresh, err := first.AppendItem(t.Context(), facts[2]); err != nil || !fresh {
		t.Fatalf("original continuation completion after a failed terminal CAS: fresh=%t err=%v", fresh, err)
	}
	// A completed Host continuation is not an independent Kernel terminal
	// Run receipt. The original elected callback must still supply its
	// private, exact terminal execution witness before either CAS commits.
	// No generic second Store connection can mint that authority from
	// the durable continuation-completed Item alone.
	if _, err := second.UpdateInvocation(t.Context(), target, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection fabricated Kernel terminal from Host continuation completion: %v", err)
	}
	if _, err := second.UpdateTurn(t.Context(), completedTurn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection fabricated Turn terminal from Host continuation completion: %v", err)
	}
	persistedTurn, err := first.GetTurn(t.Context(), turn.ID)
	if err != nil || persistedTurn.Status != turn.Status || persistedTurn.Revision != turn.Revision {
		t.Fatalf("unverified terminal CAS modified original Turn: %+v err=%v", persistedTurn, err)
	}
}
