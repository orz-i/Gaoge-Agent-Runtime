package harness_test

import (
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIndependentVerifiedInteractionReceiptResumesRunningAgentTurn(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "reconcile-running-root"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "reconcile-running-root-turn"}
	tid, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItemID, Key: "running-turn-receipt", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("setup interaction=%+v err=%v", waiting.Turn, err)
	}
	i, err := store.GetInteraction(t.Context(), waiting.Interactions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage("{}")
	if _, err = store.ResolveInteraction(t.Context(), i, i.Revision); err != nil {
		t.Fatal(err)
	}
	inv, err := store.GetInvocation(t.Context(), featureParentInvocationID(tid))
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationRunning
	if _, err = store.UpdateInvocation(t.Context(), inv, inv.Revision); err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if err != nil {
		t.Fatalf("reconcile verified host success: %v", err)
	}
	if agentFeature.resumeCalls == 0 {
		t.Fatalf("REPRO: ReconcileInteraction returned success without resuming verified Agent callback; turn=%s rootInvocation=%s", snapshot.Turn.Status, snapshot.Invocations[0].Status)
	}
}
