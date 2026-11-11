package harness_test

import (
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The original callback elected one unique continuation but its AppendItem
// acknowledgment was lost before physical Agent.Resume. An unrelated Kernel
// terminal snapshot may not certify that abandoned continuation and commit a
// terminal Harness Turn. This is deliberately a real Runner.Refresh call,
// not a direct invocation of the proof predicate.
func TestIndependentTerminalRefreshWaitsForConfirmedContinuation(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	store := &auditLostContinuationClaimACK{Store: backing}
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "claim-ack-replay"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "claim-ack-replay-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	now := featureInvocationClock{}.Now()
	seedFeatureInvocationEnvelope(t, backing, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItem := seedFeatureInvocationParent(t, runtime, backing, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{
		InvocationID: featureParentInvocationID(tid), ParentItemID: parentItem,
		Key: "business-callback-known", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("waiting=%v", err)
	}
	i := waiting.Interactions[0]
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage("{}")
	if _, err = backing.ResolveInteraction(t.Context(), i, i.Revision); err != nil {
		t.Fatal(err)
	}
	inv, err := backing.GetInvocation(t.Context(), featureParentInvocationID(tid))
	if err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationRunning
	if _, err = backing.UpdateInvocation(t.Context(), inv, inv.Revision); err != nil {
		t.Fatal(err)
	}
	turn, err := backing.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	if _, err = backing.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if err == nil || !store.failed.Load() {
		t.Fatalf("must lose claim ACK after committing it: err=%v faulted=%v", err, store.failed.Load())
	}
	// Advance the independent Kernel after the durable Claim but before
	// the elected continuation has ever entered physical Agent.Resume.
	baseline, loadErr := runtime.Load(t.Context(), inv.ExecutionRefID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	_, mutationErr := runtime.Apply(t.Context(), inv.ExecutionRefID, baseline.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: baseline.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"done":true}`)},
	})
	if mutationErr != nil {
		t.Fatal(mutationErr)
	}
	refreshed, refreshErr := runner.Refresh(t.Context(), tid)
	if refreshErr == nil && refreshed.Turn.Status == harness.TurnCompleted {
		t.Fatal("unconfirmed physical continuation was falsely committed as terminal Turn")
	}
	persisted, persistErr := backing.GetTurn(t.Context(), tid)
	if persistErr != nil {
		t.Fatal(persistErr)
	}
	if persisted.Status == harness.TurnCompleted {
		t.Fatal("in-flight physical continuation was terminalized by read-only Refresh")
	}
	if agentFeature.resumeCalls != 0 {
		t.Fatalf("independent Refresh reentered physical Agent.Resume %d times", agentFeature.resumeCalls)
	}
}
