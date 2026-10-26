package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type interactionCancelFenceHandler struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (h *interactionCancelFenceHandler) HandleInteractionResponse(_ context.Context, _ harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	close(h.started)
	<-h.release
	h.calls.Add(1)
	return harness.InteractionResponseResult{}, nil
}

func TestInteractionDispatchClaimSurvivesConcurrentRefresh(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	handler := &interactionCancelFenceHandler{started: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "agent-interaction-refresh-fence"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "fenced-interaction"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	seedFeatureInvocationEnvelope(t, store, sessionID, turnID, hostThread, hostTurn, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: "hiv_parent", ParentItemID: parentItemID,
		Key: "interaction-fence", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{}`),
	})
	if err != nil || len(waiting.Interactions) != 1 {
		t.Fatalf("create wait: %v turn=%+v", err, waiting.Turn)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := runner.ResolveInteraction(context.Background(), turnID, waiting.Interactions[0].ID,
			harness.ResolveInteractionRequest{Response: json.RawMessage(`{}`)})
		done <- runErr
	}()
	<-handler.started // The durable claim has been committed; business handler is in flight.
	observed, refreshErr := runner.Refresh(t.Context(), turnID)
	if refreshErr != nil {
		close(handler.release)
		t.Fatal(refreshErr)
	}
	if observed.Turn.Status != harness.TurnWaitingInput {
		close(handler.release)
		t.Fatalf("Refresh illegally unlocked a pending business response: turn=%s invocation=%s", observed.Turn.Status, observed.Invocations[0].Status)
	}
	_, cancelErr := runner.Cancel(t.Context(), turnID, "cancel after business dispatch")
	if !errors.Is(cancelErr, harness.ErrConflict) {
		close(handler.release)
		t.Fatalf("Cancel reported success for an already claimed business effect: err=%v", cancelErr)
	}
	close(handler.release)
	if err = <-done; err != nil || handler.calls.Load() != 1 {
		t.Fatalf("accepted callback did not finish: err=%v calls=%d", err, handler.calls.Load())
	}
}

type genericEffectHandler struct{ count atomic.Int32 }

func (h *genericEffectHandler) HandleInteractionResponse(context.Context, harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	h.count.Add(1)
	return harness.InteractionResponseResult{}, nil
}
func TestGenericInteractionCancellationDoesNotFollowCommittedResponse(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := &reauditResolveBarrierStore{Store: harness.NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	handler := &genericEffectHandler{}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	now := featureInvocationClock{}.Now()
	thread := harness.HostRef{Kind: testThreadKind, ID: "non-app-cancel-audit"}
	sid, _ := harness.SessionID(thread)
	turnref := harness.HostRef{Kind: testContextHostKind, ID: "turn"}
	tid, _ := harness.TurnID(sid, turnref)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	tref := kernel.ThreadRef{Kind: testThreadKind, ID: thread.ID}
	// Seed an existing valid admission and agent root Run. The helper accepts memory.Store,
	// the barrier store embeds memory so prepare on bare backing.
	backing := store.Store.(*harness.MemoryStore)
	seedFeatureInvocationEnvelope(t, backing, sid, tid, thread, turnref, actor, now)
	_, parentItemID := seedFeatureInvocationParent(t, runtime, backing, tid, actor, tref, now)
	waiting, err := runner.RequestInteraction(context.Background(), tid, harness.RequestInteraction{
		InvocationID: "hiv_parent", ParentItemID: parentItemID,
		Key: "generic-response", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) < 1 {
		t.Fatalf("setup err=%v %+v", err, waiting.Turn)
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.ResolveInteraction(context.Background(), tid, waiting.Interactions[0].ID, harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
		done <- err
	}()
	<-store.entered
	cancelled, err := runner.Cancel(context.Background(), tid, "cancel nonapplication before callback")
	close(store.release)
	resolutionErr := <-done
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		t.Fatalf("cancel=%s err=%v resolve=%v calls=%d", cancelled.Turn.Status, err, resolutionErr, handler.count.Load())
	}
	if handler.count.Load() != 0 {
		t.Fatalf("REPRO: generic Interaction handler called after committed Agent cancellation: calls=%d resolve=%v", handler.count.Load(), resolutionErr)
	}
}
