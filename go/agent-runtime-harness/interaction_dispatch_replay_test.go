package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

type interactionDuplicateDispatchHandler struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	failure error
}

func (handler *interactionDuplicateDispatchHandler) HandleInteractionResponse(
	_ context.Context, _ harness.InteractionResponseContext,
) (harness.InteractionResponseResult, error) {
	if handler.calls.Add(1) == 1 && handler.started != nil {
		close(handler.started)
		<-handler.release
	}
	return harness.InteractionResponseResult{
		OutputRefs: []harness.HostRef{{Kind: testApplicationDecisionRefKind, ID: testApplicationDecisionID}},
	}, handler.failure
}

func newDuplicateDispatchFixture(t *testing.T, handler *interactionDuplicateDispatchHandler) (*harness.Runner, harness.Snapshot) {
	t.Helper()
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "repeat-interaction-story"},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	}}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: harness.NewMemoryStore(), Clock: featureInvocationClock{},
		Applications: executor, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.StartApplicationTurn(t.Context(), harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	})
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || len(waiting.Interactions) != 1 {
		t.Fatalf("setup waiting Application: turn=%s interactions=%d err=%v", waiting.Turn.Status, len(waiting.Interactions), err)
	}
	return runner, waiting
}

func TestResolvedInteractionConcurrentReplayCannotDispatchTwice(t *testing.T) {
	handler := &interactionDuplicateDispatchHandler{started: make(chan struct{}), release: make(chan struct{})}
	runner, waiting := newDuplicateDispatchFixture(t, handler)
	response := harness.ResolveInteractionRequest{Response: json.RawMessage("{}")}
	done := make(chan error, 1)
	go func() {
		_, err := runner.ResolveInteraction(context.Background(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
		done <- err
	}()
	<-handler.started // The first physical effect is in progress.
	_, retryErr := runner.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	if !errors.Is(retryErr, harness.ErrInteractionOutcomeUnknown) || handler.calls.Load() != 1 {
		close(handler.release)
		t.Fatalf("same accepted Interaction physically dispatched twice: calls=%d retry=%v", handler.calls.Load(), retryErr)
	}
	close(handler.release)
	if err := <-done; err != nil || handler.calls.Load() != 1 {
		t.Fatalf("original response failed to settle without another effect: calls=%d err=%v", handler.calls.Load(), err)
	}
}

func TestAmbiguousInteractionResponseCannotBeReplayedWithoutReconciliation(t *testing.T) {
	remoteError := errors.New("simulated uncertain host business effect")
	handler := &interactionDuplicateDispatchHandler{failure: remoteError}
	runner, waiting := newDuplicateDispatchFixture(t, handler)
	response := harness.ResolveInteractionRequest{Response: json.RawMessage("{}")}
	_, firstErr := runner.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	if !errors.Is(firstErr, harness.ErrInteractionOutcomeUnknown) || !errors.Is(firstErr, remoteError) {
		t.Fatalf("ambiguous response error=%v", firstErr)
	}
	_, secondErr := runner.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	if !errors.Is(secondErr, harness.ErrInteractionOutcomeUnknown) || handler.calls.Load() != 1 {
		t.Fatalf("unverified response re-dispatched: calls=%d err=%v", handler.calls.Load(), secondErr)
	}
	_, reconcileErr := runner.ReconcileInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID)
	if !errors.Is(reconcileErr, harness.ErrInteractionOutcomeUnknown) || handler.calls.Load() != 1 {
		t.Fatalf("missing read-only host reconciler caused a second effect: calls=%d err=%v", handler.calls.Load(), reconcileErr)
	}
	observed, err := runner.Load(t.Context(), waiting.Turn.ID)
	if err != nil || observed.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("unknown outcome rewrote waiting Turn: %+v err=%v", observed.Turn, err)
	}
	owner, ok := harness.TopLevelInvocation(observed)
	if !ok || owner.Status != harness.InvocationRunning {
		t.Fatalf("unknown response lost durable dispatch claim: %+v", owner)
	}
}

// Refresh is the authoritative root Application reconciliation path.
// Child-only Invocation entry points reject this root rather than accidentally
// offering a second root projection protocol. A running business callback
// must never reenter the original Application executor.
func TestApplicationInvocationRefreshCannotBypassBusinessCallbackClaim(t *testing.T) {
	handler := &interactionDuplicateDispatchHandler{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	runner, waiting := newDuplicateDispatchFixture(t, handler)
	owner, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("missing root Application")
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.ResolveInteraction(context.Background(), waiting.Turn.ID, waiting.Interactions[0].ID,
			harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
		done <- err
	}()
	<-handler.started
	snapshot, err := runner.Refresh(t.Context(), waiting.Turn.ID)
	if err != nil || snapshot.Turn.Status != harness.TurnWaitingInput || handler.calls.Load() != 1 {
		close(handler.release)
		t.Fatalf("root refresh crossed claimed business effect: turn=%s calls=%d err=%v",
			snapshot.Turn.Status, handler.calls.Load(), err)
	}
	for _, rejected := range []func(context.Context, string) (harness.Snapshot, error){
		runner.RefreshInvocation, runner.ResumeInvocation,
	} {
		if _, err := rejected(t.Context(), owner.ID); !errors.Is(err, harness.ErrInvalidRequest) || handler.calls.Load() != 1 {
			close(handler.release)
			t.Fatalf("root Application entered child-only route or redispatched callback: calls=%d err=%v", handler.calls.Load(), err)
		}
	}
	close(handler.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
