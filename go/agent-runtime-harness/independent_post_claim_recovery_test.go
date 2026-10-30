package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type auditLostInteractionReceiptStore struct {
	harness.Store
	faulted atomic.Bool
}

func (s *auditLostInteractionReceiptStore) UpdateInvocation(ctx context.Context, v harness.Invocation, expected uint64) (harness.Invocation, error) {
	if v.Status == harness.InvocationCompleted && s.faulted.CompareAndSwap(false, true) {
		return harness.Invocation{}, errors.New("FAULT: crash before persisting successful business handler result")
	}
	return s.Store.UpdateInvocation(ctx, v, expected)
}

// Read-only host reconciliation never dispatches or changes the external business
// object. "known" is asserted only after querying a verified host business receipt.
type auditReadOnlyInteractionReconciler struct {
	reauditBusinessResponseEffect
	known atomic.Bool
	reads atomic.Int32
}

func (h *auditReadOnlyInteractionReconciler) ReconcileInteractionResponse(
	_ context.Context, _ harness.InteractionResponseContext,
) (harness.InteractionResponseResult, bool, error) {
	h.reads.Add(1)
	if !h.known.Load() {
		return harness.InteractionResponseResult{}, false, nil
	}
	return harness.InteractionResponseResult{OutputRefs: []harness.HostRef{{Kind: testApplicationDecisionRefKind, ID: testApplicationDecisionID}}}, true, nil
}

func TestIndependentAuditedInteractionSuccessfulCallbackReceiptLossCanRecover(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := &auditLostInteractionReceiptStore{Store: harness.NewMemoryStore()}
	executor := &applicationCapabilityExecutorFixture{interaction: &harness.ApplicationInteractionRequest{
		ApplicationRef: &harness.HostRef{Kind: testApplicationStoryRefKind, ID: "story_lost_callback_receipt"},
		Key:            testApplicationCapabilityKey, Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	}}
	handler := &auditReadOnlyInteractionReconciler{}
	newRunner := func() *harness.Runner {
		r, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{},
			Applications: executor, Interactions: handler,
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	req := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	waiting, err := newRunner().StartApplicationTurn(t.Context(), req)
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || len(waiting.Interactions) != 1 {
		t.Fatalf("unexpected setup turn=%s interactions=%d err=%v", waiting.Turn.Status, len(waiting.Interactions), err)
	}
	response := harness.ResolveInteractionRequest{Response: json.RawMessage("{}")}
	_, err = newRunner().ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	if err == nil || !store.faulted.Load() || handler.count.Load() != 1 {
		t.Fatalf("fault setup: err=%v injected=%v calls=%d", err, store.faulted.Load(), handler.count.Load())
	}
	// External effect completed, but the Harness receipt was lost. Recovery
	// MUST verify the host business receipt read-only before committing it.
	reopened := newRunner()
	observed, err := reopened.Refresh(t.Context(), waiting.Turn.ID)
	if err != nil {
		t.Fatalf("read-only refresh of ambiguous handler: %v", err)
	}
	owner, ok := harness.TopLevelInvocation(observed)
	if !ok {
		t.Fatal("missing root")
	}
	t.Logf("After restart: turn=%s invocation=%s businessEffects=%d", observed.Turn.Status, owner.Status, handler.count.Load())
	_, retryErr := reopened.ResolveInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID, response)
	if !errors.Is(retryErr, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("unverified receipt replay accepted: %v", retryErr)
	}
	if handler.count.Load() != 1 {
		t.Fatalf("unsafe duplicate business callback: %d", handler.count.Load())
	}
	if observed.Turn.Status != harness.TurnWaitingInput || owner.Status != harness.InvocationRunning {
		t.Fatalf("read-only Refresh bypassed missing business receipt: %+v", observed.Turn)
	}
	if _, err = reopened.ReconcileInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID); !errors.Is(err, harness.ErrInteractionOutcomeUnknown) {
		t.Fatalf("unverified external outcome must fail closed: %v", err)
	}
	handler.known.Store(true) // Host has externally confirmed the effect via a durable business receipt.
	recovered, err := reopened.ReconcileInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID)
	if err != nil || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("verified response failed to repair stored completion: turn=%+v err=%v", recovered.Turn, err)
	}
	confirmed, ok := harness.TopLevelInvocation(recovered)
	if !ok || confirmed.Status != harness.InvocationCompleted || handler.count.Load() != 1 {
		t.Fatalf("post-reconciliation state=%+v physicalCalls=%d", confirmed, handler.count.Load())
	}
	repeated, err := reopened.ReconcileInteraction(t.Context(), waiting.Turn.ID, waiting.Interactions[0].ID)
	if err != nil || repeated.Turn.Status != harness.TurnCompleted || handler.count.Load() != 1 {
		t.Fatalf("replayed verification was not idempotent: status=%s calls=%d err=%v", repeated.Turn.Status, handler.count.Load(), err)
	}
}

type auditEpochClock struct{ unixNano atomic.Int64 }

func (c *auditEpochClock) Now() time.Time                 { return time.Unix(0, c.unixNano.Load()).UTC() }
func (c *auditEpochClock) Advance(duration time.Duration) { c.unixNano.Add(int64(duration)) }

type auditExpireDuringApplication struct{ clock *auditEpochClock }

func (x auditExpireDuringApplication) ExecuteApplicationCapability(context.Context, harness.ApplicationCapabilityRequest) (harness.ApplicationCapabilityResult, error) {
	x.clock.Advance(2 * time.Minute)
	return harness.ApplicationCapabilityResult{}, nil
}

type auditCountTerminalDrafts struct {
	runfeed.Store
	attempts atomic.Int32
}

func (s *auditCountTerminalDrafts) Append(ctx context.Context, id string, d runfeed.Draft, now time.Time, retention time.Duration) (runfeed.Event, error) {
	if d.Terminal {
		s.attempts.Add(1)
	}
	return s.Store.Append(ctx, id, d, now, retention)
}

func TestIndependentAuditedLongRunningTurnTerminalEventSurvivesFeedRetention(t *testing.T) {
	c := &auditEpochClock{}
	c.unixNano.Store(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC).UnixNano())
	backingFeed := &auditCountTerminalDrafts{Store: memory.NewRunFeedStore(memory.RunFeedOptions{Clock: c})}
	runFeed, err := runfeed.New(backingFeed, runfeed.Options{Clock: c, Retention: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(runFeed)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newFeatureInvocationRuntime(t)
	executor := auditExpireDuringApplication{clock: c}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: harness.NewMemoryStore(),
		Clock: c, Applications: executor, TurnFeed: feed,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	finished, err := runner.StartApplicationTurn(t.Context(), req)
	if err != nil || finished.Turn.Status != harness.TurnCompleted {
		t.Fatalf("completed Turn: status=%s err=%v", finished.Turn.Status, err)
	}
	if backingFeed.attempts.Load() != 1 {
		t.Fatalf("lost or duplicated terminal notification after progress retention expiry: attempts=%d", backingFeed.attempts.Load())
	}
	c.Advance(5 * time.Second)
	for i := 0; i < 2; i++ {
		if _, err = runner.Refresh(t.Context(), finished.Turn.ID); err != nil {
			t.Fatal(err)
		}
	}
	if backingFeed.attempts.Load() != 1 {
		t.Fatalf("retained terminal suffix not recognized after prefix expiry: attempts=%d", backingFeed.attempts.Load())
	}
}

type auditRunningTeamFeature struct{ runtime *kernel.Runtime }

func (f auditRunningTeamFeature) StartRun(ctx context.Context, req team.StartRequest) (kernel.Snapshot, error) {
	return f.runtime.Create(ctx, kernel.CreateRequest{
		ID: req.ID, Kind: team.RunKind, Actor: req.Actor, Thread: req.Thread, RequestID: req.RequestID, Goal: req.Goal, State: json.RawMessage("{}"),
	})
}
func (f auditRunningTeamFeature) Resume(ctx context.Context, id string, _ uint64) (kernel.Snapshot, error) {
	return f.runtime.Load(ctx, id)
}

type auditBlockBusinessHandler struct {
	start   chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (h *auditBlockBusinessHandler) HandleInteractionResponse(context.Context, harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	close(h.start)
	<-h.release
	h.calls.Add(1)
	return harness.InteractionResponseResult{}, nil
}
func TestIndependentAuditedCancelSubtaskCannotBypassClaimedBusinessHandler(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	handler := &auditBlockBusinessHandler{start: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
		Relations: relations, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "child_interaction_cancel", Goal: "child waiting work",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var child harness.Invocation
	for _, inv := range started.Invocations {
		if inv.ParentItemID != "" && inv.ExecutionClass == harness.ExecutionTeam {
			child = inv
			break
		}
	}
	if child.ID == "" {
		t.Fatal("missing child invocation")
	}
	if len(started.Subtasks) == 0 {
		t.Fatal("no related subtask")
	}
	task := started.Subtasks[0]
	if task.RunID != child.ExecutionRefID {
		t.Fatalf("not targeted child: task=%+v child=%+v", task, child)
	}
	waiting, err := runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: child.ID, ParentItemID: "", Key: "child_input_wait", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
	})
	if err != nil || len(waiting.Interactions) == 0 {
		t.Fatalf("no waiting interaction: err=%v status=%s", err, waiting.Turn.Status)
	}
	done := make(chan error, 1)
	go func() {
		_, callErr := runner.ResolveInteraction(context.Background(), turnID, waiting.Interactions[0].ID,
			harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
		done <- callErr
	}()
	<-handler.start
	cancelled, cancelErr := runner.CancelSubtask(t.Context(), turnID, task.ID, "cancel child while business callback claimed")
	if !errors.Is(cancelErr, harness.ErrConflict) {
		close(handler.release)
		<-done
		t.Fatalf("CancelSubtask did not honor running interaction claim: child=%s status=%s err=%v", task.ID, cancelled.Turn.Status, cancelErr)
	}
	observed, err := runner.Load(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	for _, subtask := range observed.Subtasks {
		if subtask.ID == task.ID && subtask.Status == "cancelled" {
			t.Fatalf("subtask cancelled despite already running business callback")
		}
	}
	close(handler.release)
	<-done
}

func TestSubtaskWaitingInteractionCanStillCancelBeforeBusinessDispatch(t *testing.T) {
	_, turnID, parentItemID, relations, _, store, runtime := newFeatureInvocationHarness(t)
	handler := &auditBlockBusinessHandler{start: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: auditRunningTeamFeature{runtime: runtime},
		Relations: relations, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "cancel-before-callback", Goal: "cancel waiting child",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}},
	})
	if err != nil || len(started.Subtasks) != 1 {
		t.Fatalf("start=%v subtasks=%d", err, len(started.Subtasks))
	}
	var child harness.Invocation
	for _, inv := range started.Invocations {
		if inv.ExecutionClass == harness.ExecutionTeam && inv.ParentItemID != "" {
			child = inv
			break
		}
	}
	if child.ID == "" {
		t.Fatal("child invocation missing")
	}
	_, err = runner.RequestInteraction(t.Context(), turnID, harness.RequestInteraction{
		InvocationID: child.ID, Key: "pending-choice", Kind: harness.InteractionChoice,
		Schema: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := runner.CancelSubtask(t.Context(), turnID, started.Subtasks[0].ID, "stop before dispatch")
	if err != nil {
		t.Fatalf("cancel pending subtask: %v", err)
	}
	if handler.calls.Load() != 0 {
		t.Fatalf("cancel dispatched business callback: calls=%d", handler.calls.Load())
	}
	if len(cancelled.Subtasks) != 1 || cancelled.Subtasks[0].Status != string(kernel.RunStatusCancelled) {
		t.Fatalf("pending child was not cancelled: %+v", cancelled.Subtasks)
	}
}
