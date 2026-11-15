package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestReauditVerifiedInteractionReconcileIdempotency(t *testing.T) {
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
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "reconcile-idempotence-root"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "reconcile-idempotence-turn"}
	tid, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItemID, Key: "recoverable-idempotence", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("setup=%v", err)
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
	first, firstErr := runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if firstErr != nil || first.Turn.Status != harness.TurnRunning || agentFeature.resumeCalls != 1 {
		t.Fatalf("first verified continuation failed: err=%v turn=%s resumeCalls=%d", firstErr, first.Turn.Status, agentFeature.resumeCalls)
	}
	_, secondErr := runner.ReconcileInteraction(t.Context(), tid, i.ID)
	if !errors.Is(secondErr, harness.ErrInteractionOutcomeUnknown) || agentFeature.resumeCalls != 1 {
		t.Fatalf("duplicate verified continuation was not fail-closed: err=%v resumeCalls=%d", secondErr, agentFeature.resumeCalls)
	}
}

type auditBlockingResumeAgent struct {
	loadingFeatureAgent
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (a *auditBlockingResumeAgent) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	a.calls.Add(1)
	a.entered <- struct{}{}
	<-a.release
	return a.runtime.Load(ctx, runID)
}
func TestReauditConcurrentVerifiedReconcileDoubleResumesRootAgent(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	feature := &auditBlockingResumeAgent{loadingFeatureAgent: loadingFeatureAgent{runtime: runtime}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	handler := &auditReadOnlyInteractionReconciler{}
	handler.known.Store(true)
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: feature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "concurrent-reconcile-root"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "concurrent-reconcile-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItemID, Key: "reconcile-dual-run", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
	if err != nil {
		t.Fatal(err)
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
	current, err := store.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	current.Status = harness.TurnRunning
	if _, err = store.UpdateTurn(t.Context(), current, current.Revision); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, e := runner.ReconcileInteraction(context.Background(), tid, i.ID)
		done <- e
	}()
	<-feature.entered
	go func() {
		_, e := runner.ReconcileInteraction(context.Background(), tid, i.ID)
		done <- e
	}()
	twice := false
	select {
	case <-feature.entered:
		twice = true
	case <-time.After(400 * time.Millisecond):
	}
	close(feature.release)
	e1, e2 := <-done, <-done
	if twice || feature.calls.Load() != 1 {
		t.Fatalf("multiple concurrent reconciliations dispatched Agent.Resume: calls=%d errors=%v,%v", feature.calls.Load(), e1, e2)
	}
	if e1 == nil && e2 == nil {
		t.Fatal("duplicate verified continuation was acknowledged as successfully dispatched")
	}
	if e1 != nil && !errors.Is(e1, harness.ErrInteractionOutcomeUnknown) || e2 != nil && !errors.Is(e2, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("unexpected concurrent continuation errors: first=%v second=%v", e1, e2)
	}
}

type auditLostContinuationClaimACK struct {
	harness.Store
	failed atomic.Bool
}

func (s *auditLostContinuationClaimACK) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	result, created, err := s.Store.AppendItem(ctx, item)
	if err == nil && created && item.Kind == harness.ItemDiagnostic && bytes.Contains(item.Payload, []byte("runtime_continuation_claimed")) && s.failed.CompareAndSwap(false, true) {
		return harness.Item{}, false, errors.New("injected lost continuation-claim commit ACK")
	}
	return result, created, err
}
func TestReauditLostContinuationClaimACKCannotReenterAgent(t *testing.T) {
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
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "lost-continuation-ack"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "lost-continuation-ack-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, backing, sid, tid, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, backing, tid, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), tid, harness.RequestInteraction{InvocationID: featureParentInvocationID(tid), ParentItemID: parentItemID, Key: "lost-ack", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}")})
	if err != nil {
		t.Fatal(err)
	}
	interaction := waiting.Interactions[0]
	interaction.Status = harness.InteractionResolved
	interaction.Response = json.RawMessage("{}")
	if _, err = backing.ResolveInteraction(t.Context(), interaction, interaction.Revision); err != nil {
		t.Fatal(err)
	}
	invocation, err := backing.GetInvocation(t.Context(), featureParentInvocationID(tid))
	if err != nil {
		t.Fatal(err)
	}
	invocation.Status = harness.InvocationRunning
	if _, err = backing.UpdateInvocation(t.Context(), invocation, invocation.Revision); err != nil {
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
	if _, err = runner.ReconcileInteraction(t.Context(), tid, interaction.ID); err == nil || !store.failed.Load() {
		t.Fatalf("post-commit claim ACK fault was not injected: err=%v faulted=%v", err, store.failed.Load())
	}
	if agentFeature.resumeCalls != 0 {
		t.Fatalf("continuation ran despite uncertain claim commit: calls=%d", agentFeature.resumeCalls)
	}
	restarted, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: agentFeature, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.ReconcileInteraction(t.Context(), tid, interaction.ID)
	if !errors.Is(err, harness.ErrInteractionOutcomeUnknown) || agentFeature.resumeCalls != 0 {
		t.Fatalf("ambiguous claim ACK triggered unsafe replay: err=%v calls=%d", err, agentFeature.resumeCalls)
	}
}
