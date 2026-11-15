package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestNewIndependentCommittedContinuationClaimDoesNotReportSuccessWithoutRuntimeProgress(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	store := &auditLostContinuationClaimACK{Store: backing}
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
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
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItem, Key: "business-callback-known", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
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
	runBefore, err := runtime.Load(t.Context(), inv.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if err == nil || !store.failed.Load() {
		t.Fatalf("must lose claim ACK after committing it: err=%v faulted=%v", err, store.failed.Load())
	}
	replayed, replayErr := runner.ResolveInteraction(t.Context(), tid, i.ID, harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
	runAfter, loadErr := runtime.Load(t.Context(), inv.ExecutionRefID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !errors.Is(replayErr, harness.ErrInteractionOutcomeUnknown) || runAfter.Run.Revision != runBefore.Run.Revision || agentFeature.resumeCalls != 0 {
		t.Fatalf("REPRO: replay returned success while committed claim never entered Agent.Resume: turn=%s invocation=%s runtimeRevision=%d", replayed.Turn.Status, replayed.Invocations[0].Status, runAfter.Run.Revision)
	}
}
func TestNewIndependentVerifiedClaimBypassedByResumeTurn(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	store := &auditLostContinuationClaimACK{Store: backing}
	agentFeature := &recordingInteractionAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
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
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItem, Key: "business-callback-known", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
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
	resumed, resumeErr := runner.ResumeTurn(t.Context(), tid)
	if agentFeature.resumeCalls != 0 || !errors.Is(resumeErr, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("REPRO: ResumeTurn dispatched Agent.Resume after committed continuation claim, bypassing single-dispatch guard: calls=%d err=%v turn=%s", agentFeature.resumeCalls, resumeErr, resumed.Turn.Status)
	}
}
