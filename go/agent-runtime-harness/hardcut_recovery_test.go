package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type auditMessageFaultOnce struct {
	harness.Store
	failed bool
}

func (store *auditMessageFaultOnce) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.Kind == harness.ItemUserMessage && !store.failed {
		store.failed = true
		return harness.Item{}, false, errors.New("audit: simulated persistence failure before first host message")
	}
	return store.Store.AppendItem(ctx, item)
}

func TestAuditStartReplayRepairsHostMessageItems(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	flaky := &auditMessageFaultOnce{Store: backing}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		result, e := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: s, Clock: fixedClock{}})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	request := testStartRequest()
	request.InputMessage = &harness.HostRef{Kind: testMessageKind, ID: "audit-user"}
	request.OutputMessage = &harness.HostRef{Kind: testMessageKind, ID: "audit-assistant"}
	_, err = newRunner(flaky).Start(t.Context(), request)
	if err == nil || !flaky.failed {
		t.Fatalf("failed to inject before host item commit: %v", err)
	}
	recovered, err := newRunner(backing).Start(t.Context(), request)
	if err != nil || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("restart could not finish: %+v / %v", recovered.Turn, err)
	}
	user, started, _ := messageLifecycleItems(recovered.Items)
	if user == nil || started == nil {
		t.Fatalf("REPRO: replay lost host message items after admitted Turn; user=%t started=%t turn=%s items=%d", user != nil, started != nil, recovered.Turn.Status, len(recovered.Items))
	}
}

type auditInvocationFaultOnce struct {
	harness.Store
	failed bool
}

func (store *auditInvocationFaultOnce) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.Kind == harness.ItemInvocation && !store.failed {
		store.failed = true
		return harness.Item{}, false, errors.New("audit: crashed after invocation was created but before Agent Run")
	}
	return store.Store.AppendItem(ctx, item)
}

func TestAuditRootReplayRefusesChangedExecutionAllowance(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	flaky := &auditInvocationFaultOnce{Store: backing}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		result, e := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: s, Clock: fixedClock{}})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	first := testStartRequest()
	first.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	_, err = newRunner(flaky).Start(t.Context(), first)
	if err == nil || !flaky.failed {
		t.Fatalf("failed to inject between Invocation and Agent Run: %v", err)
	}
	changed := first
	changed.CallAllowance = nil // silently expands frozen execution segmentation if replay admitted
	recovered, err := newRunner(backing).Start(t.Context(), changed)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("REPRO: same Turn accepted changed allowance, status=%s err=%v, want ErrConflict", recovered.Turn.Status, err)
	}
}

type auditTerminalFaultOnce struct {
	harness.Store
	failed bool
}

func (store *auditTerminalFaultOnce) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.Kind == harness.ItemAgentRun && item.Status == harness.ItemCompleted && !store.failed {
		store.failed = true
		return harness.Item{}, false, errors.New("audit: crash after terminal Turn CAS before terminal items")
	}
	return store.Store.AppendItem(ctx, item)
}

func TestAuditRefreshRepairsTerminalProjectionCrash(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	flaky := &auditTerminalFaultOnce{Store: backing}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		result, e := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: s, Clock: fixedClock{}})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	request := testStartRequest()
	_, err = newRunner(flaky).Start(t.Context(), request)
	if err == nil || !flaky.failed {
		t.Fatalf("expected post-terminal projection crash, err=%v", err)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := newRunner(backing).Refresh(t.Context(), turnID)
	if err != nil || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("terminal Turn unreadable: status=%s err=%v", recovered.Turn.Status, err)
	}
	found := false
	for _, item := range recovered.Items {
		if item.Kind == harness.ItemAgentRun && item.Status == harness.ItemCompleted {
			found = true
		}
	}
	if !found {
		t.Fatalf("REPRO: terminal Turn skips repairing missing completed Agent Run lifecycle Item; status=%s items=%d", recovered.Turn.Status, len(recovered.Items))
	}
}

type auditApplicationTerminalFaultOnce struct {
	harness.Store
	failed bool
}

func (store *auditApplicationTerminalFaultOnce) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	if item.Kind == harness.ItemInvocation && item.Status == harness.ItemCompleted && !store.failed {
		store.failed = true
		return harness.Item{}, false, errors.New("audit: crash after application Invocation completion before Turn completion")
	}
	return store.Store.AppendItem(ctx, item)
}
func TestAuditApplicationTerminalInvocationReconcilesTurnOnRefresh(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "audit-output"}}}
	backing := harness.NewMemoryStore()
	flaky := &auditApplicationTerminalFaultOnce{Store: backing}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		result, e := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: s,
			Clock: featureInvocationClock{}, Applications: executor,
		})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	request := harness.ApplicationTurnRequest{
		StartRequest:  testStartRequest(),
		CapabilityKey: testApplicationCapabilityKey, DefinitionVersion: testApplicationVersion,
		Input: json.RawMessage("{}"),
	}
	_, err := newRunner(flaky).StartApplicationTurn(t.Context(), request)
	if err == nil || !flaky.failed {
		t.Fatalf("expected fault after application completion, err=%v", err)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := newRunner(backing).Refresh(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	invocation, ok := harness.TopLevelInvocation(recovered)
	if !ok {
		t.Fatal("no root Invocation")
	}
	if invocation.Status != harness.InvocationCompleted || recovered.Turn.Status != harness.TurnCompleted {
		t.Fatalf("REPRO: completed Application Invocation but Turn still %s, invocation=%s, calls=%d", recovered.Turn.Status, invocation.Status, executor.calls)
	}
}

type auditApplicationEffectLoss struct {
	harness.Store
	failed bool
}

func (store *auditApplicationEffectLoss) UpdateInvocation(ctx context.Context, value harness.Invocation, expectedRevision uint64) (harness.Invocation, error) {
	if value.ExecutionClass == harness.ExecutionApplication && value.Status == harness.InvocationCompleted && !store.failed {
		store.failed = true
		return harness.Invocation{}, errors.New("audit: crash after host capability effect but before completion commit")
	}
	return store.Store.UpdateInvocation(ctx, value, expectedRevision)
}
func TestAuditApplicationEffectDoesNotRedispatchUnknownOutcome(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "audit-effect"}}}
	backing := harness.NewMemoryStore()
	flaky := &auditApplicationEffectLoss{Store: backing}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		result, e := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: s,
			Clock: featureInvocationClock{}, Applications: executor,
		})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	req := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	_, err := newRunner(flaky).StartApplicationTurn(t.Context(), req)
	if err == nil || !flaky.failed || executor.calls != 1 {
		t.Fatalf("expected first effect then crash: err=%v calls=%d", err, executor.calls)
	}
	sessionID, err := harness.SessionID(req.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, req.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newRunner(backing).Refresh(t.Context(), turnID)
	if executor.calls != 1 || !errors.Is(err, harness.ErrApplicationOutcomeUnknown) {
		t.Fatalf("Application executor re-dispatched after unknown external outcome, calls=%d, err=%v", executor.calls, err)
	}
}

func TestAuditAcceptedTurnRejectsChangedContextSeed(t *testing.T) {
	store := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	provider := &multiContextCaptureModel{}
	flaky := &auditInvocationFaultOnce{Store: store}
	first, _ := newResumeTurnHarness(t, memory.NewStore(), flaky, provider, clock, nil)
	req := testStartRequest()
	req.Actor = kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	req.Config.Model = "fixture"
	req.Context = contextSeed("original private instructions", contextEntry("same-source", "turn", model.RoleUser, "answer directly", true))
	_, err := first.Start(t.Context(), req)
	if err == nil || !flaky.failed {
		t.Fatalf("expected fault before context commit, err=%v", err)
	}
	second, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, nil)
	// Kernel store must be shared across both launches, but there was no Run before fault.
	req.Context = contextSeed("REPLACED private instructions", contextEntry("same-source", "turn", model.RoleUser, "answer directly", true))
	_, err = second.Start(t.Context(), req)
	if !errors.Is(err, harness.ErrConflict) || len(provider.requests) != 0 {
		t.Fatalf("replayed changed Context seed: err=%v model_calls=%d", err, len(provider.requests))
	}
}
func BenchmarkAuditTimelineReadScaling(b *testing.B) {
	for _, count := range []int{0, 100, 1000, 5000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
			if err != nil {
				b.Fatal(err)
			}
			a, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
			if err != nil {
				b.Fatal(err)
			}
			store := harness.NewMemoryStore()
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: a, Store: store, Clock: fixedClock{}})
			if err != nil {
				b.Fatal(err)
			}
			snapshot, err := runner.Start(context.Background(), testStartRequest())
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < count; i++ {
				_, _, err = store.AppendItem(context.Background(), harness.Item{
					ID: "audit-extra-" + strconv.Itoa(i), TurnID: snapshot.Turn.ID,
					Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				loaded, err := runner.Load(context.Background(), snapshot.Turn.ID)
				if err != nil || len(loaded.Items) < count {
					b.Fatalf("load err=%v, items=%d", err, len(loaded.Items))
				}
			}
		})
	}
}

// Host reconciliation is observational. Only the original Executor can prove
// the logical outcome; recovery never sends a second physical effect.
type hardcutReconciler struct {
	*applicationCapabilityExecutorFixture
	observations int
}

func (value *hardcutReconciler) ReconcileApplicationCapability(_ context.Context, request harness.ApplicationCapabilityRequest) (harness.ApplicationCapabilityResult, bool, error) {
	value.observations++
	if request.Invocation.ExecutionRefID == "" || request.Invocation.Status != harness.InvocationRunning {
		return harness.ApplicationCapabilityResult{}, false, harness.ErrConflict
	}
	return harness.ApplicationCapabilityResult{
		OutputRefs: append([]harness.HostRef(nil), value.outputRefs...),
	}, true, nil
}

func TestHardcutApplicationReconcilerRepairsWithoutNewEffect(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &hardcutReconciler{applicationCapabilityExecutorFixture: &applicationCapabilityExecutorFixture{
		outputRefs: []harness.HostRef{{Kind: testApplicationReviewRefKind, ID: "known-external-effect"}},
	}}
	store := harness.NewMemoryStore()
	fault := &auditApplicationEffectLoss{Store: store}
	create := func(persistence harness.Store) *harness.Runner {
		t.Helper()
		runner, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
			Store: persistence, Clock: featureInvocationClock{}, Applications: executor,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner
	}
	request := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	_, err := create(fault).StartApplicationTurn(t.Context(), request)
	if err == nil || !fault.failed || executor.calls != 1 {
		t.Fatalf("expected effect followed by a lost receipt: %v, calls=%d", err, executor.calls)
	}
	sessionID, _ := harness.SessionID(request.HostThread)
	turnID, _ := harness.TurnID(sessionID, request.HostTurn)
	restored, err := create(store).Refresh(t.Context(), turnID)
	if err != nil || restored.Turn.Status != harness.TurnCompleted ||
		executor.calls != 1 || executor.observations != 1 {
		t.Fatalf("reconciled=%s err=%v effects=%d observations=%d",
			restored.Turn.Status, err, executor.calls, executor.observations)
	}
}

type blockingHardcutApplication struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (e *blockingHardcutApplication) ExecuteApplicationCapability(ctx context.Context, _ harness.ApplicationCapabilityRequest) (harness.ApplicationCapabilityResult, error) {
	if e.calls.Add(1) == 1 {
		close(e.started)
	}
	select {
	case <-e.release:
		return harness.ApplicationCapabilityResult{}, nil
	case <-ctx.Done():
		return harness.ApplicationCapabilityResult{}, ctx.Err()
	}
}

func TestHardcutConcurrentApplicationStartAdmitsOneEffect(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &blockingHardcutApplication{started: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{
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
		_, callErr := runner.StartApplicationTurn(context.Background(), request)
		done <- callErr
	}()
	<-executor.started
	_, err = runner.StartApplicationTurn(t.Context(), request)
	if !errors.Is(err, harness.ErrApplicationOutcomeUnknown) || executor.calls.Load() != 1 {
		t.Fatalf("concurrent replay entered an external effect: err=%v calls=%d", err, executor.calls.Load())
	}
	close(executor.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if executor.calls.Load() != 1 {
		t.Fatalf("executor ran %d times", executor.calls.Load())
	}
}

func TestHardcutLoadStateAndPaginatedTimeline(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Start(t.Context(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1100 {
		_, _, err = store.AppendItem(t.Context(), harness.Item{
			ID: "paged-item-" + strconv.Itoa(i), TurnID: first.Turn.ID,
			Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := runner.LoadState(t.Context(), first.Turn.ID)
	if err != nil || state.Turn != first.Turn || state.Session != first.Session ||
		state.Config.ContentHash != first.Config.ContentHash {
		t.Fatalf("light projection lost identity: %+v err=%v", state.Turn, err)
	}
	seen := map[uint64]bool{}
	var after uint64
	for {
		items, pageErr := runner.ListTimeline(t.Context(), first.Turn.ID, after, 200)
		if pageErr != nil {
			t.Fatal(pageErr)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if seen[item.Seq] || item.Seq <= after {
				t.Fatalf("nonmonotonic or duplicate sequence %d", item.Seq)
			}
			seen[item.Seq] = true
			after = item.Seq
		}
	}
	full, err := runner.Load(t.Context(), first.Turn.ID)
	if err != nil || len(seen) != len(full.Items) {
		t.Fatalf("paged=%d full=%d err=%v", len(seen), len(full.Items), err)
	}
	if _, err = runner.ListTimeline(t.Context(), first.Turn.ID, 0, 501); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("unbounded page accepted: %v", err)
	}
}

func BenchmarkHardcutLoadStateScaling(b *testing.B) {
	for _, count := range []int{0, 100, 1000, 5000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
			if err != nil {
				b.Fatal(err)
			}
			direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
			if err != nil {
				b.Fatal(err)
			}
			store := harness.NewMemoryStore()
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
			})
			if err != nil {
				b.Fatal(err)
			}
			first, err := runner.Start(context.Background(), testStartRequest())
			if err != nil {
				b.Fatal(err)
			}
			for i := range count {
				if _, _, err := store.AppendItem(context.Background(), harness.Item{
					ID: "state-item-" + strconv.Itoa(i), TurnID: first.Turn.ID,
					Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
				}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				state, err := runner.LoadState(context.Background(), first.Turn.ID)
				if err != nil || state.Turn.ID != first.Turn.ID {
					b.Fatal(err)
				}
			}
		})
	}
}

// A terminal Host control must also fence an Invocation that was accepted but
// never dispatched. Refresh and a replayed Start cannot resurrect it.
func TestHardcutTerminalApplicationTurnNeverDispatchesAcceptedInvocation(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	executor := &applicationCapabilityExecutorFixture{}
	store := harness.NewMemoryStore()
	now := featureInvocationClock{}.Now()
	request := harness.ApplicationTurnRequest{
		StartRequest: testStartRequest(), CapabilityKey: testApplicationCapabilityKey,
		DefinitionVersion: testApplicationVersion, Input: json.RawMessage("{}"),
	}
	// Begin once, deliberately lose the presentation append while preserving
	// the durable accepted Invocation; no Application executor has run yet.
	flaky := &auditInvocationFaultOnce{Store: store}
	newRunner := func(s harness.Store) *harness.Runner {
		t.Helper()
		value, err := harness.NewRunner(harness.Dependencies{
			Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
			Store: s, Clock: featureInvocationClock{}, Applications: executor,
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	_, err := newRunner(flaky).StartApplicationTurn(t.Context(), request)
	if err == nil || !flaky.failed || executor.calls != 0 {
		t.Fatalf("expected accepted, undispatched Invocation: err=%v calls=%d", err, executor.calls)
	}
	sessionID, _ := harness.SessionID(request.HostThread)
	turnID, _ := harness.TurnID(sessionID, request.HostTurn)
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnCancelled
	turn.UpdatedAt = now
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = newRunner(store).Refresh(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newRunner(store).StartApplicationTurn(t.Context(), request)
	if err != nil || executor.calls != 0 {
		t.Fatalf("terminal Turn dispatched an Application effect: err=%v calls=%d", err, executor.calls)
	}
}

// Old pending Harness records without the immutable admission digest cannot
// prove that a fresh host projection equals the original accepted input.
func TestHardcutOldRunningTurnCannotResumeWithoutAdmissionEvidence(t *testing.T) {
	store := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	provider := &multiContextCaptureModel{}
	host, runtime := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, nil)
	turn := seedAcceptedResumeTurn(t, store, clock, true, harness.ExecutionAgent)
	values, err := store.ListInvocations(t.Context(), turn.ID)
	if err != nil || len(values) != 1 {
		t.Fatalf("root fixture: %#v err=%v", values, err)
	}
	_, err = runtime.Create(t.Context(), kernel.CreateRequest{
		ID: values[0].ExecutionRefID, Kind: agent.RunKind,
		Actor: resumeTurnSessionOptions().Actor, Thread: resumeTurnSessionOptions().Thread,
		Goal: "pending older work", State: json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnRunning
	turn.UpdatedAt = clock.Now()
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	_, err = host.ResumeTurn(t.Context(), turn.ID)
	if !errors.Is(err, harness.ErrConflict) || len(provider.requests) != 0 {
		t.Fatalf("old admission crossed hard-cut dispatch gate: err=%v calls=%d", err, len(provider.requests))
	}
}

func TestHardcutTerminalProjectionNeverRewritesAnotherTerminalOutcome(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := host.Start(t.Context(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	cancelled := finished.Turn
	cancelled.Status = harness.TurnCancelled
	cancelled.UpdatedAt = fixedClock{}.Now()
	cancelled, err = store.UpdateTurn(t.Context(), cancelled, cancelled.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Refresh(t.Context(), cancelled.ID)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("conflicting terminal outcome accepted: %v", err)
	}
	persisted, err := store.GetTurn(t.Context(), cancelled.ID)
	if err != nil || persisted.Status != harness.TurnCancelled || persisted.Revision != cancelled.Revision {
		t.Fatalf("terminal status rewritten: %+v err=%v", persisted, err)
	}
}
