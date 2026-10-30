package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestIndependentAuditRefreshMustNotDispatchAcceptedApplication(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	flaky := &auditInvocationFaultOnce{Store: backing}
	executor := &applicationCapabilityExecutorFixture{}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		runner, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
			Store: s, Clock: featureInvocationClock{}, Applications: executor,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	request := harness.ApplicationTurnRequest{
		StartRequest:      testStartRequest(),
		CapabilityKey:     testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion,
		Input:             json.RawMessage("{}"),
	}
	_, err := newRunner(flaky).StartApplicationTurn(context.Background(), request)
	if err == nil || !flaky.failed || executor.calls != 0 {
		t.Fatalf("fault preconditions: err=%v failed=%v calls=%d", err, flaky.failed, executor.calls)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := backing.GetTurn(context.Background(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != harness.TurnAccepted {
		t.Fatalf("turn not accepted: %v", accepted.Status)
	}
	_, err = newRunner(backing).Refresh(context.Background(), turnID)
	if err != nil {
		t.Logf("Refresh error: %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("REPRO: Refresh dispatched an unstarted Application Capability: physical calls=%d", executor.calls)
	}
}

func TestIndependentAuditLegacyTurnCannotStartNewTeamChild(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	now := featureInvocationClock{}.Now()
	hostThread := harness.HostRef{Kind: testThreadKind, ID: "legacy-thread"}
	sessionID, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	hostTurn := harness.HostRef{Kind: testContextHostKind, ID: "legacy-turn"}
	turnID, err := harness.TurnID(sessionID, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testTenant, ActorID: testActor}
	thread := kernel.ThreadRef{Kind: testThreadKind, ID: hostThread.ID}
	_, _, err = store.CreateSession(t.Context(), harness.Session{
		ID: sessionID, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(turnID, harness.ConfigSnapshot{Model: "fixture"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if config.AdmissionHash != "" {
		t.Fatal("not a legacy config")
	}
	_, _, err = store.PutConfigSnapshot(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: turnID, SessionID: sessionID, HostTurn: hostTurn, ConfigSnapshotID: config.ID,
		Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, turnID, actor, thread, now)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Teams: completedTeamFeature{runtime},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "legacy-child", Goal: "do legacy child",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "draft"}},
	})
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("REPRO: old Turn admitted new Team child: status=%s err=%v invocations=%d", result.Turn.Status, err, len(result.Invocations))
	}
}

type auditBlockFirstGetSession struct {
	harness.Store
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *auditBlockFirstGetSession) GetSession(ctx context.Context, id string) (harness.Session, error) {
	if s.first.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.Store.GetSession(ctx, id)
}

func TestIndependentAuditApplicationCancellationBeforePhysicalDispatch(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := &auditBlockFirstGetSession{
		Store:   harness.NewMemoryStore(),
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &applicationCapabilityExecutorFixture{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Applications: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := harness.ApplicationTurnRequest{
		StartRequest:  testStartRequest(),
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage("{}"),
	}
	finished := make(chan error, 1)
	go func() {
		_, e := runner.StartApplicationTurn(context.Background(), req)
		finished <- e
	}()
	<-store.entered // Invocation Running, but actual external effect not dispatched
	sid, _ := harness.SessionID(req.HostThread)
	tid, _ := harness.TurnID(sid, req.HostTurn)
	cancelled, err := runner.Cancel(context.Background(), tid, "user cancelled before dispatch")
	if err != nil || cancelled.Turn.Status != harness.TurnCancelled {
		close(store.release)
		t.Fatalf("cannot commit cancellation before release: status=%s err=%v", cancelled.Turn.Status, err)
	}
	close(store.release)
	launchErr := <-finished
	if executor.calls != 0 {
		t.Fatalf("REPRO: Executor dispatched after cancellation committed: calls=%d startErr=%v", executor.calls, launchErr)
	}
}

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
	_, _, err := store.CreateSession(t.Context(), harness.Session{
		ID: sid, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now})
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
	_, _, err = store.CreateTurn(t.Context(), harness.Turn{ID: tid, SessionID: sid, HostTurn: hostTurn,
		ConfigSnapshotID: cfg.ID, Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	_, parentItemID := seedFeatureInvocationParent(t, runtime, store, tid, actor, thread, now)
	handler := &auditCountingInteractionHandler{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Interactions: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := harness.Interaction{
		ID: "historical-interaction", TurnID: tid, InvocationID: "hiv_parent",
		ParentItemID: parentItemID, Key: "old-approval",
		Kind: harness.InteractionChoice, Schema: json.RawMessage("{}"),
		Status: harness.InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	parent, err := store.GetInvocation(t.Context(), "hiv_parent")
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
	_, err = runner.ResolveInteraction(t.Context(), tid, pending.ID, harness.ResolveInteractionRequest{
		Response: json.RawMessage("{}"),
	})
	if handler.calls != 0 {
		t.Fatalf("REPRO: legacy Turn called business-side InteractionResponseHandler=%d before hardcut resume check (err=%v)", handler.calls, err)
	}
}

type auditFailFirstRootCreate struct {
	harness.Store
	failed bool
}

func (s *auditFailFirstRootCreate) CreateInvocation(ctx context.Context, invocation harness.Invocation) (harness.Invocation, bool, error) {
	if invocation.ParentItemID == "" && !s.failed {
		s.failed = true
		return harness.Invocation{}, false, errors.New("inject failure after Turn but before root Invocation")
	}
	return s.Store.CreateInvocation(ctx, invocation)
}

func TestIndependentAuditTypedTopLevelInputMustFreezeBeforeTurnCommit(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	faulty := &auditFailFirstRootCreate{Store: backing}
	makeRunner := func(store harness.Store) *harness.Runner {
		t.Helper()
		runner, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
			Store: store, Clock: featureInvocationClock{}, Teams: completedTeamFeature{runtime},
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	start := harness.TeamTurnRequest{
		StartRequest: testStartRequest(),
		Mode:         team.ExecutionSequential,
		Members:      []team.Member{{ID: "writer", Goal: "original authorized work"}},
	}
	_, err := makeRunner(faulty).StartTeamTurn(t.Context(), start)
	if err == nil || !faulty.failed {
		t.Fatalf("could not inject after Turn, before invocation: %v", err)
	}
	// All generic StartRequest fields are unchanged; only the typed execution plan drifts.
	start.Members = []team.Member{{ID: "writer", Goal: "CHANGED work without original intent"}}
	resumed, err := makeRunner(backing).StartTeamTurn(t.Context(), start)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("REPRO: same Turn accepted changed top-level Team members: err=%v turn=%s invocation_count=%d", err, resumed.Turn.Status, len(resumed.Invocations))
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
	_, err = mk(faulty).Start(t.Context(), req)
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

func TestIndependentAuditCapabilityKindCannotChangeAfterTurnPersisted(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	backing := harness.NewMemoryStore()
	faulty := &auditFailFirstRootCreate{Store: backing}
	executor := &applicationCapabilityExecutorFixture{}
	makeRunner := func(s harness.Store) *harness.Runner {
		r, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
			Teams: completedTeamFeature{runtime}, Applications: executor,
			Store: s, Clock: featureInvocationClock{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	original := harness.TeamTurnRequest{
		StartRequest: testStartRequest(), Mode: team.ExecutionSequential,
		Members: []team.Member{{ID: "author", Goal: "original"}},
	}
	_, err := makeRunner(faulty).StartTeamTurn(t.Context(), original)
	if err == nil || !faulty.failed {
		t.Fatalf("fault not committed: %v", err)
	}
	changed := harness.ApplicationTurnRequest{
		StartRequest:      original.StartRequest,
		CapabilityKey:     testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	snapshot, err := makeRunner(backing).StartApplicationTurn(t.Context(), changed)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("REPRO: after persisted Team Turn, root capability switched to Application: err=%v status=%s appEffects=%d", err, snapshot.Turn.Status, executor.calls)
	}
}

// The admission hash freezes the logical input, but dispatch must also consume
// the stored Invocation rather than a caller-owned slice mutated after Create.
type auditMutateAfterInvocation struct {
	harness.Store
	onCreate func()
}

func (store *auditMutateAfterInvocation) CreateInvocation(ctx context.Context, value harness.Invocation) (harness.Invocation, bool, error) {
	created, fresh, err := store.Store.CreateInvocation(ctx, value)
	if fresh && err == nil && value.ParentItemID == "" && store.onCreate != nil {
		store.onCreate()
	}
	return created, fresh, err
}

type auditObservedTeam struct {
	runtime *kernel.Runtime
	goal    string
}

func (teamRunner *auditObservedTeam) StartRun(ctx context.Context, request team.StartRequest) (kernel.Snapshot, error) {
	if len(request.Members) != 1 {
		return kernel.Snapshot{}, harness.ErrConflict
	}
	teamRunner.goal = request.Members[0].Goal
	return (completedTeamFeature{runtime: teamRunner.runtime}).StartRun(ctx, request)
}

func (teamRunner *auditObservedTeam) Resume(ctx context.Context, id string, revision uint64) (kernel.Snapshot, error) {
	return (completedTeamFeature{runtime: teamRunner.runtime}).Resume(ctx, id, revision)
}

func TestHardcutTopLevelFeatureDispatchReadsFrozenInput(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	feature := &auditObservedTeam{runtime: runtime}
	input := harness.TeamTurnRequest{
		StartRequest: testStartRequest(),
		Mode:         team.ExecutionSequential,
		Members:      []team.Member{{ID: "writer", Goal: "sealed original task"}},
	}
	store := &auditMutateAfterInvocation{Store: harness.NewMemoryStore(), onCreate: func() {
		input.Members[0].Goal = "mutated after admission"
	}}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: store, Clock: featureInvocationClock{}, Teams: feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := host.StartTeamTurn(t.Context(), input)
	if err != nil || result.Turn.Status != harness.TurnCompleted ||
		feature.goal != "sealed original task" {
		t.Fatalf("model saw mutable host input: status=%s featureGoal=%q err=%v", result.Turn.Status, feature.goal, err)
	}
}

func TestHardcutRunningApplicationCancelCannotClaimEffectStopped(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &blockingHardcutApplication{started: make(chan struct{}), release: make(chan struct{})}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
		Store: harness.NewMemoryStore(), Clock: featureInvocationClock{}, Applications: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	done := make(chan error, 1)
	go func() {
		_, callErr := host.StartApplicationTurn(context.Background(), request)
		done <- callErr
	}()
	<-executor.started
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Cancel(t.Context(), turnID, "stop")
	if !errors.Is(err, harness.ErrApplicationOutcomeUnknown) {
		t.Fatalf("in-flight Application wrongly reported cancelled: %v", err)
	}
	observed, err := host.Load(t.Context(), turnID)
	if err != nil || observed.Turn.Status != harness.TurnRunning {
		t.Fatalf("in-flight Application mutated by failed cancellation: %v %+v", err, observed.Turn)
	}
	close(executor.release)
	if err = <-done; err != nil || executor.calls.Load() != 1 {
		t.Fatalf("running effect failed to settle: err=%v calls=%d", err, executor.calls.Load())
	}
}
