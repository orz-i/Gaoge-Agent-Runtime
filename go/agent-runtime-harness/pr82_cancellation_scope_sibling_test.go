package harness

import "testing"

func TestPR82CancellationWithoutRelationsPreservesWaitingSibling(t *testing.T) {
	f, parent, _, _ := newPR82NestedApplicationParent(t, InvocationRunning)
	f.runner.relations, f.runner.relationReader = nil, nil
	f.runner.applications = pr82InteractionWaitExecutor{}
	waiting, err := f.runner.StartApplicationInvocation(t.Context(), f.turn.ID, ApplicationInvocationRequest{
		ParentItemID: f.startedItemID, RequestID: "unrelated-waiting-sibling", Goal: "wait independently",
		CapabilityKey: "app.sibling", DefinitionVersion: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting.Interactions) != 1 {
		t.Fatalf("interactions=%d", len(waiting.Interactions))
	}
	siblingID := waiting.Interactions[0].InvocationID
	if _, err = f.runner.completeApplicationInvocation(t.Context(), f.turn, parent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = f.runner.CancelInvocation(t.Context(), parent.ID, "cancel only parent subtree"); err != nil {
		t.Fatal(err)
	}
	sibling, err := f.store.GetInvocation(t.Context(), siblingID)
	if err != nil {
		t.Fatal(err)
	}
	if sibling.Status != InvocationWaitingInput {
		t.Fatalf("unrelated waiting sibling was changed by conservative admission fence: %s", sibling.Status)
	}
}
