package harness_test

import (
	"context"
	"encoding/json"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
)

type auditCountingInteractionHandler struct{ calls int }

func (h *auditCountingInteractionHandler) HandleInteractionResponse(context.Context, harness.InteractionResponseContext) (harness.InteractionResponseResult, error) {
	h.calls++
	return harness.InteractionResponseResult{}, nil
}
func TestIndependentAuditLegacyInteractionCannotApplyHostEffect(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "old-interaction-thread"}
	sid, _ := harness.SessionID(hostThread)
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "old-interaction-turn"}
	tid, _ := harness.TurnID(sid, hostTurn)
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	_, _, err := store.CreateSession(t.Context(), harness.Session{ID: sid, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := harness.SealConfigSnapshot(tid, harness.ConfigSnapshot{Model: "fixture"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.PutConfigSnapshot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateTurn(t.Context(), harness.Turn{ID: tid, SessionID: sid, HostTurn: hostTurn, ConfigSnapshotID: cfg.ID, Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	handler := &auditCountingInteractionHandler{}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store, Clock: featureInvocationClock{}, Interactions: handler})
	if err != nil {
		t.Fatal(err)
	}
	pending := harness.Interaction{ID: "historical-interaction", TurnID: tid, InvocationID: featureParentInvocationID(tid), ParentItemID: parentItemID, Key: "old-approval", Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"), Status: harness.InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now}
	parent, err := store.GetInvocation(t.Context(), featureParentInvocationID(tid))
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	_, fresh, err := store.CreateInteraction(t.Context(), pending, turn.Revision, parent.Revision)
	if err != nil || !fresh {
		t.Fatalf("cannot seed historical persisted interaction: %v", err)
	}
	parent.Status = harness.InvocationWaitingInput
	parent.UpdatedAt = now
	if _, err = store.UpdateInvocation(t.Context(), parent, parent.Revision); err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnWaitingInput
	turn.UpdatedAt = now
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = runner.ResolveInteraction(t.Context(), tid, pending.ID, harness.ResolveInteractionRequest{Response: json.RawMessage("{}")})
	if handler.calls != 0 {
		t.Fatalf("REPRO: legacy Turn called business-side InteractionResponseHandler=%d before hardcut resume check (err=%v)", handler.calls, err)
	}
}

func TestIndependentAuditRecoveredTerminalTurnFeedHasTerminalEvent(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	rawFeed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	turnFeed, err := harness.NewTurnFeed(rawFeed)
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	faulty := &auditTerminalFaultOnce{Store: backing}
	mk := func(s harness.Store) *harness.Runner {
		t.Helper()
		r, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: s, Clock: fixedClock{}, TurnFeed: turnFeed})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	req := testStartRequest()
	_, err = mk(faulty).StartAgentTurn(t.Context(), req)
	if err == nil || !faulty.failed {
		t.Fatalf("failed fault setup: %v", err)
	}
	sid, _ := harness.SessionID(req.HostThread)
	tid, _ := harness.TurnID(sid, req.HostTurn)
	snapshot, err := mk(backing).Refresh(t.Context(), tid)
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted {
		t.Fatalf("refresh: %v %+v", err, snapshot.Turn)
	}
	events, err := turnFeed.Replay(t.Context(), tid, 0)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events {
		if event.Terminal && event.Type == harness.EventTurnCompleted {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal TurnFeed recovery emitted %d completion events; want one", terminal)
	}
	if _, err := mk(backing).Refresh(t.Context(), tid); err != nil {
		t.Fatalf("repeated Refresh: %v", err)
	}
	replayed, err := turnFeed.Replay(t.Context(), tid, 0)
	if err != nil {
		t.Fatal(err)
	}
	terminal = 0
	for _, event := range replayed {
		if event.Terminal && event.Type == harness.EventTurnCompleted {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("repeated Refresh duplicated terminal event: count=%d", terminal)
	}
}
