package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Privileged corruption bypasses Store CAS, but a previously committed
// Host-success receipt cannot authorize a new continuation for different
// resolved business input after a restart or on a second SQL connection.
func TestSQLHistoricallyModifiedResolvedBusinessResponseCannotBorrowReceipt(t *testing.T) {
	store, db := newStoreWithDB(t)
	original, _, chain := seedSQLInteractionAuthorityFixture(t, store, "mutated_original")
	if _, fresh, err := store.AppendItem(t.Context(), chain[0]); err != nil || !fresh {
		t.Fatalf("persist original Host receipt: fresh=%v err=%v", fresh, err)
	}
	if err := db.Exec("UPDATE agent_harness_interactions SET response_json = ? WHERE id = ?",
		`{"modified":true}`, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	corrupt, err := store.GetInteraction(t.Context(), original.ID)
	if err != nil || string(corrupt.Response) != `{"modified":true}` {
		t.Fatalf("failed to verify exact historical corruption: %v %+v", err, corrupt)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[1]); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("changed business response borrowed original Host receipt: fresh=%v err=%v", fresh, err)
	}
	if err := db.Exec("UPDATE agent_harness_interactions SET response_json = ? WHERE id = ?",
		string(original.Response), original.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[1]); err != nil || !fresh {
		t.Fatalf("restored original business meaning was not accepted: fresh=%v err=%v", fresh, err)
	}
}

// A second Host answer cannot be committed through ResolveInteraction.
// This is enforced under the Store's original Turn/Invocation CAS for both
// Memory and SQL; the obsolete general UpdateInteraction port was removed.
func TestSQLResolveInteractionIsSoleBusinessResponseCAS(t *testing.T) {
	store := newStore(t)
	_, _, i := createWaitingInteractionFixture(t, store, "sole_response_cas")
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage(`{"ok":true}`)
	first, err := store.ResolveInteraction(t.Context(), i, i.Revision)
	if err != nil {
		t.Fatalf("first response CAS failed: %v", err)
	}
	other := first.Interaction
	other.Response = json.RawMessage(`{"ok":false}`)
	if _, err := store.ResolveInteraction(t.Context(), other, first.Interaction.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second Host response overwrote original resolved answer: %v", err)
	}
	original, err := store.GetInteraction(t.Context(), i.ID)
	if err != nil || string(original.Response) != string(i.Response) {
		t.Fatalf("original committed response was modified: %v %+v", err, original)
	}
}
