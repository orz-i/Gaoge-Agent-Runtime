package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// This read-only Store refuses every mutation by construction. It exercises
// exactly the Store operations allowed at the shared Host receipt proof
// boundary. Any accidental write panics rather than masking a side effect.
type proofReadOnlyStore struct {
	Store
	interactions []Interaction
	owners       map[string]Invocation
	items        []Item
	readErr      error
	itemReads    int
}

func (store *proofReadOnlyStore) ListInteractions(ctx context.Context, _ string) ([]Interaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return store.interactions, nil
}

func (store *proofReadOnlyStore) GetInvocation(ctx context.Context, id string) (Invocation, error) {
	if err := ctx.Err(); err != nil {
		return Invocation{}, err
	}
	owner, ok := store.owners[id]
	if !ok {
		return Invocation{}, ErrNotFound
	}
	return owner, nil
}

func (store *proofReadOnlyStore) ListItems(ctx context.Context, _ string, afterSeq uint64, _ int) ([]Item, error) {
	store.itemReads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store.readErr != nil {
		return nil, store.readErr
	}
	if afterSeq != 0 {
		return nil, nil
	}
	return store.items, nil
}

func auditResolvedInteractionSource(interaction Interaction, owner Invocation) Item {
	payload, err := interactionItemPayload(interaction)
	if err != nil {
		panic(err)
	}
	return Item{
		ID:     stableID("hinteractionitem", interaction.ID, string(InteractionResolved)),
		TurnID: interaction.TurnID, InvocationID: owner.ID,
		Kind: ItemInteraction, Status: ItemCompleted, Seq: 1,
		ParentItemID: interaction.ParentItemID,
		Payload:      payload,
	}
}

func TestHostResponseReceiptRequiresOriginalResolvedItem(t *testing.T) {
	interaction := Interaction{ID: "source-proof", TurnID: "turn-1", InvocationID: "owner-A", Status: InteractionResolved}
	owner := Invocation{ID: "owner-A", TurnID: "turn-1", ExecutionRefID: "run-A", Status: InvocationRunning}
	source := auditResolvedInteractionSource(interaction, owner)
	response := auditVerifiedHostReceipt(interaction, owner)
	response.Seq = 2
	for _, tc := range []struct {
		name    string
		missing bool
		corrupt func(*Item)
	}{
		{"canonical", false, func(*Item) {}},
		{"missing-source", true, func(*Item) {}},
		{"wrong-status", false, func(s *Item) { s.Status = ItemWaiting }},
		{"wrong-owner", false, func(s *Item) { s.InvocationID = "foreign" }},
		{"wrong-source-payload", false, func(s *Item) { s.Payload = json.RawMessage(`{"interactionID":"other","status":"resolved"}`) }},
		{"future-sequence", false, func(s *Item) { s.Seq = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := source
			tc.corrupt(&src)
			items := []Item{response}
			if !tc.missing {
				items = append([]Item{src}, items...)
			}
			want := tc.name == "canonical"
			if got := hasVerifiedInteractionResponse(items, interaction, owner); got != want {
				t.Fatalf("source %s authorized=%v want=%v", tc.name, got, want)
			}
		})
	}
}

func auditVerifiedHostReceipt(interaction Interaction, owner Invocation) Item {
	payload, _ := json.Marshal(struct {
		InteractionID string `json:"interactionID"`
		Boundary      string `json:"boundary"`
	}{InteractionID: interaction.ID, Boundary: "host_response_verified"})
	return Item{
		ID:     verifiedInteractionResponseItemID(interaction.ID),
		TurnID: interaction.TurnID, InvocationID: owner.ID,
		RunID: owner.ExecutionRefID, Seq: 2,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		ParentItemID: stableID("hinteractionitem", interaction.ID, string(InteractionResolved)),
		Payload:      payload,
	}
}

func TestResolvedHostReceiptProofScopeStatusAndForgeryMatrix(t *testing.T) {
	statuses := []InteractionStatus{InteractionWaiting, InteractionResolved, InteractionStatus("future-status")}
	owners := []InvocationStatus{
		InvocationAccepted, InvocationWaitingInput, InvocationRunning,
		InvocationPausedBudget, InvocationCompleted, InvocationFailed, InvocationCancelled,
	}
	receipts := []string{"absent", "verified", "foreign-turn", "foreign-run", "foreign-invocation", "bad-boundary"}
	checked := 0
	for _, interactionStatus := range statuses {
		for _, ownerStatus := range owners {
			for _, proofCase := range receipts {
				t.Run(fmt.Sprintf("%s/%s/%s", interactionStatus, ownerStatus, proofCase), func(t *testing.T) {
					interaction := Interaction{
						ID: "interaction-A", TurnID: "turn-1",
						InvocationID: "owner-A", Status: interactionStatus,
					}
					owner := Invocation{ID: "owner-A", TurnID: "turn-1",
						ExecutionRefID: "run-A", Status: ownerStatus}
					store := &proofReadOnlyStore{
						interactions: []Interaction{interaction},
						owners:       map[string]Invocation{"owner-A": owner},
					}
					if proofCase != "absent" {
						receipt := auditVerifiedHostReceipt(interaction, owner)
						switch proofCase {
						case "foreign-turn":
							receipt.TurnID = "turn-foreign"
						case "foreign-run":
							receipt.RunID = "run-foreign"
						case "foreign-invocation":
							receipt.InvocationID = "owner-foreign"
						case "bad-boundary":
							receipt.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"not_verified"}`)
						}
						store.items = []Item{auditResolvedInteractionSource(interaction, owner), receipt}
					}
					runner := &Runner{store: store}
					// An independent truth table: only a resolved Interaction
					// owned by a Running Invocation without an exact verified
					// business-response receipt is still effect-unknown.
					expected := interactionStatus == InteractionResolved &&
						ownerStatus == InvocationRunning && proofCase != "verified"
					actual, err := runner.resolvedInteractionClaimPending(t.Context(), "turn-1", "")
					if err != nil || actual != expected {
						t.Fatalf("all-scope proof: got=%t err=%v want=%t", actual, err, expected)
					}
					exact, err := runner.resolvedInteractionClaimPending(t.Context(), "turn-1", "owner-A")
					if err != nil || exact != expected {
						t.Fatalf("exact-owner proof: got=%t err=%v want=%t", exact, err, expected)
					}
					matched, err := runner.unverifiedResolvedInteraction(t.Context(), "turn-1", func(id string) bool {
						return id == "owner-A"
					})
					if err != nil || matched != expected {
						t.Fatalf("cancel-like scoped proof: got=%t err=%v want=%t", matched, err, expected)
					}
					sibling, err := runner.unverifiedResolvedInteraction(t.Context(), "turn-1", func(id string) bool {
						return id == "unrelated-owner"
					})
					if err != nil || sibling {
						t.Fatalf("unrelated sibling counted as affected: got=%t err=%v", sibling, err)
					}
					if store.itemReads != 0 && (interactionStatus != InteractionResolved || ownerStatus != InvocationRunning) {
						t.Fatalf("read receipt items without a running resolved owner: %d", store.itemReads)
					}
					checked++
				})
			}
		}
	}
	if checked != 126 {
		t.Fatalf("expected 126 combinations, got %d", checked)
	}
}

// A relation-scoped cancellation must never consult or mutate an unrelated
// sibling, including a sibling whose Store owner lookup would fail. Unlike
// the whole-Turn Refresh fence, the scoped check accepts a separately
// verified receipt without turning an unverified sibling into its own claim.
func TestResolvedHostReceiptProofPreservesSiblingScopeAndPagesOnce(t *testing.T) {
	a := Interaction{ID: "interaction-A", TurnID: "turn-1",
		InvocationID: "owner-A", Status: InteractionResolved}
	b := Interaction{ID: "interaction-B", TurnID: "turn-1",
		InvocationID: "owner-B", Status: InteractionResolved}
	foreign := Interaction{ID: "interaction-foreign", TurnID: "turn-1",
		InvocationID: "owner-foreign", Status: InteractionResolved}
	ownerA := Invocation{ID: "owner-A", TurnID: "turn-1", ExecutionRefID: "run-A", Status: InvocationRunning}
	ownerB := Invocation{ID: "owner-B", TurnID: "turn-1", ExecutionRefID: "run-B", Status: InvocationRunning}
	verified := auditVerifiedHostReceipt(a, ownerA)
	store := &proofReadOnlyStore{
		interactions: []Interaction{a, b, foreign},
		owners:       map[string]Invocation{"owner-A": ownerA, "owner-B": ownerB},
		items:        []Item{auditResolvedInteractionSource(a, ownerA), verified},
	}
	runner := &Runner{store: store}
	pendingA, err := runner.resolvedInteractionClaimPending(t.Context(), "turn-1", "owner-A")
	if err != nil || pendingA {
		t.Fatalf("verified owner's independent continuation blocked: pending=%v err=%v", pendingA, err)
	}
	pendingB, err := runner.resolvedInteractionClaimPending(t.Context(), "turn-1", "owner-B")
	if err != nil || !pendingB {
		t.Fatalf("unverified sibling's claim was lost: pending=%v err=%v", pendingB, err)
	}
	readsBefore := store.itemReads
	affected, err := runner.unverifiedResolvedInteraction(t.Context(), "turn-1", func(id string) bool {
		return id == "owner-A" || id == "owner-B"
	})
	if err != nil || !affected || store.itemReads != readsBefore+1 {
		t.Fatalf("cancellation scope did not reuse one verified evidence page: pending=%v err=%v itemReads=%d", affected, err, store.itemReads-readsBefore)
	}
	emptyScope, err := runner.unverifiedResolvedInteraction(t.Context(), "turn-1", func(string) bool { return false })
	if err != nil || emptyScope || store.itemReads != readsBefore+1 {
		t.Fatalf("unrelated owner leaked into empty cancellation scope: pending=%v err=%v", emptyScope, err)
	}
	// With no selection this orphan belongs to the Turn. The conservative
	// whole-Turn check cannot silently ignore an unverified Store owner.
	_, err = runner.unverifiedResolvedInteraction(t.Context(), "turn-1", func(id string) bool {
		return id == "owner-foreign"
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign unverified owner should fail closed, got %v", err)
	}
}

func TestResolvedHostReceiptProofErrorAndForeignOwnershipFailClosed(t *testing.T) {
	interaction := Interaction{ID: "interaction-A", TurnID: "turn-1",
		InvocationID: "owner-A", Status: InteractionResolved}
	owner := Invocation{ID: "owner-A", TurnID: "turn-1",
		ExecutionRefID: "run-A", Status: InvocationRunning}
	for _, tc := range []struct {
		name   string
		change func(*proofReadOnlyStore)
		want   error
	}{
		{"foreign-turn", func(s *proofReadOnlyStore) {
			v := s.owners["owner-A"]
			v.TurnID = "turn-foreign"
			s.owners["owner-A"] = v
		}, ErrConflict},
		{"missing-owner", func(s *proofReadOnlyStore) { delete(s.owners, "owner-A") }, ErrNotFound},
		{"item-read-failed", func(s *proofReadOnlyStore) { s.readErr = context.DeadlineExceeded }, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &proofReadOnlyStore{interactions: []Interaction{interaction},
				owners: map[string]Invocation{"owner-A": owner}}
			tc.change(store)
			runner := &Runner{store: store}
			for name, invoke := range map[string]func() (bool, error){
				"continuation": func() (bool, error) {
					return runner.resolvedInteractionClaimPending(t.Context(), "turn-1", "owner-A")
				},
				"cancellation": func() (bool, error) {
					return runner.unverifiedResolvedInteraction(t.Context(), "turn-1", func(id string) bool {
						return id == "owner-A"
					})
				},
			} {
				_, err := invoke()
				if !errors.Is(err, tc.want) {
					t.Fatalf("%s: expected %v got %v", name, tc.want, err)
				}
			}
		})
	}
}
