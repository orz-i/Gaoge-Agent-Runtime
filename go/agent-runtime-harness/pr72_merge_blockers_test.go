package harness_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// Freeze the original Start immediately AFTER its physical grant commits, but
// BEFORE Kernel Run creation. Another identical Start must not terminalize it.
type pr72PausedStartGrantStore struct {
	harness.Store
	stopped atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pr72PausedStartGrantStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	saved, created, err := s.Store.AppendItem(ctx, item)
	if err == nil && created && item.Kind == harness.ItemDiagnostic &&
		item.ParentItemID == harness.ExecutionDispatchParentItemID && s.stopped.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return saved, created, err
}

func TestPR72ConcurrentStartReplayDoesNotTerminalizeActiveDispatch(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	backing := harness.NewMemoryStore()
	store := &pr72PausedStartGrantStore{Store: backing, entered: make(chan struct{}), release: make(chan struct{})}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	type result struct {
		snapshot harness.Snapshot
		err      error
	}
	first := make(chan result, 1)
	go func() {
		snapshot, e := runner.StartAgentTurn(context.Background(), request)
		first <- result{snapshot, e}
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("original dispatch did not acquire grant")
	}
	second, secondErr := runner.StartAgentTurn(t.Context(), request)
	if !errors.Is(secondErr, harness.ErrConflict) {
		close(store.release)
		t.Fatalf("concurrent replay must return a conflict, not a failure projection: turn=%s err=%v", second.Turn.Status, secondErr)
	}
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		close(store.release)
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		close(store.release)
		t.Fatal(err)
	}
	during, loadErr := backing.GetTurn(t.Context(), turnID)
	close(store.release)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if during.Status != harness.TurnAccepted {
		t.Errorf("replay terminalized an active dispatch: %s", during.Status)
	}
	select {
	case original := <-first:
		if original.err != nil || original.snapshot.Turn.Status != harness.TurnCompleted {
			t.Errorf("first dispatch could not finish: status=%s err=%v", original.snapshot.Turn.Status, original.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("original dispatch did not complete")
	}
	after, err := backing.GetTurn(t.Context(), turnID)
	if err != nil || after.Status != harness.TurnCompleted {
		t.Fatalf("final status=%s err=%v", after.Status, err)
	}
}

// A custom Feature error has no trusted Kernel state. The old code projected
// Failed and rotated ExecutionRefID while the original dispatch was unknown.
func TestPR72AmbiguousChildStartKeepsOriginalIdentityAndGrant(t *testing.T) {
	_, turnID, parentItem, relations, _, store, runtime := newFeatureInvocationHarness(t)
	feature := &auditAmbiguousTeamStart{}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Teams: feature,
		Store: store, Clock: featureInvocationClock{}, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItem, RequestID: "unknown-team-start", Goal: "remote effect unverified",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "task"}},
	})
	if !errors.Is(startErr, harness.ErrExecutionOutcomeUnknown) || !errors.Is(startErr, kernel.ErrConflict) {
		t.Fatalf("missing precise unknown-outcome classification: %v", startErr)
	}
	values, err := store.ListInvocations(t.Context(), turnID)
	if err != nil {
		t.Fatal(err)
	}
	var child harness.Invocation
	for _, value := range values {
		if value.ExecutionClass == harness.ExecutionTeam {
			child = value
			break
		}
	}
	if child.ID == "" || child.Status != harness.InvocationAccepted || child.Attempt != 1 {
		t.Fatalf("unproven effect became retryable: %+v", child)
	}
	items, err := store.ListItems(t.Context(), turnID, 0, 500)
	if err != nil || !harness.PendingExecutionDispatch(items, child.ID) {
		t.Fatalf("unproven grant was released: %v", err)
	}
	_, retryErr := runner.RetryInvocation(t.Context(), child.ID)
	if !errors.Is(retryErr, harness.ErrConflict) || feature.calls.Load() != 1 {
		t.Fatalf("unsafe child retry was accepted: err=%v calls=%d", retryErr, feature.calls.Load())
	}
	// A stale or concurrent failure projection must also lose to the
	// unresolved physical grant at the Store CAS boundary, not just through
	// the high-level Start/Retry entrypoints.
	failed := child
	failed.Status = harness.InvocationFailed
	_, err = store.UpdateInvocation(t.Context(), failed, failed.Revision)
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("Store failed an Invocation with an unknown physical start: %v", err)
	}
	_, err = store.RetryInvocation(t.Context(), child.ID, child.Revision, "new-unsafe-run", featureInvocationClock{}.Now())
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("Store rotated in-doubt identity: %v", err)
	}
	after, err := store.GetInvocation(t.Context(), child.ID)
	if err != nil || after.Status != harness.InvocationAccepted || after.ExecutionRefID != child.ExecutionRefID || after.Attempt != 1 {
		t.Fatalf("original execution identity changed: %+v err=%v", after, err)
	}
}

type pr72DispatchReceiptObserver struct {
	harness.Store
	mu          sync.Mutex
	grantID     string
	observed    bool
	hasDeadline bool
}

func (s *pr72DispatchReceiptObserver) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	s.mu.Lock()
	if item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemCompleted && item.ParentItemID == s.grantID && s.grantID != "" {
		s.observed = true
		_, s.hasDeadline = ctx.Deadline()
	}
	s.mu.Unlock()
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err == nil && fresh && item.ParentItemID == harness.ExecutionDispatchParentItemID {
		s.mu.Lock()
		s.grantID = saved.ID
		s.mu.Unlock()
	}
	return saved, fresh, err
}

func TestPR72DispatchCompletionUsesBoundedIndependentContext(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := &pr72DispatchReceiptObserver{Store: harness.NewMemoryStore()}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil || result.Turn.Status != harness.TurnCompleted {
		t.Fatalf("start: %s %v", result.Turn.Status, err)
	}
	store.mu.Lock()
	seen, bounded := store.observed, store.hasDeadline
	store.mu.Unlock()
	if !seen || !bounded {
		t.Fatalf("completion receipt observed=%t hasDeadline=%t", seen, bounded)
	}
}

// After one explicitly proven no-effect Start closes its grant, a second
// identical Start may win the next grant before the first caller projects
// Failed. Both Store implementations must arbitrate that projection against
// the new grant, or the first caller can terminalize the new executor.
type pr72DelayedFailureProjection struct {
	harness.Store
	once    atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (s *pr72DelayedFailureProjection) UpdateInvocation(ctx context.Context, value harness.Invocation, rev uint64) (harness.Invocation, error) {
	if value.Status == harness.InvocationFailed && s.once.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
	}
	return s.Store.UpdateInvocation(ctx, value, rev)
}

type pr72NoEffectThenActiveWorkflow struct {
	completedWorkflowFeature
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (f *pr72NoEffectThenActiveWorkflow) StartRun(ctx context.Context, req workflow.StartRequest) (kernel.Snapshot, error) {
	if f.calls.Add(1) == 1 {
		return kernel.Snapshot{}, harness.FeatureStartNoEffect(errors.New("first attempt had no effects"))
	}
	close(f.entered)
	<-f.release
	return f.completedWorkflowFeature.StartRun(ctx, req)
}

func TestPR72ProvenNoEffectFailureCannotTerminateNewDispatch(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := &pr72DelayedFailureProjection{Store: harness.NewMemoryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	feature := &pr72NoEffectThenActiveWorkflow{completedWorkflowFeature: completedWorkflowFeature{runtime: runtime}, entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-store.release:
		default:
			close(store.release)
		}
		select {
		case <-feature.release:
		default:
			close(feature.release)
		}
	}()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: store,
		Clock: featureInvocationClock{}, Workflows: feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := blockedWorkflowTurnRequest("noeffect-next-grant")
	type outcome struct {
		snapshot harness.Snapshot
		err      error
	}
	first := make(chan outcome, 1)
	go func() { s, e := runner.StartWorkflowTurn(context.Background(), req); first <- outcome{s, e} }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first no-effect grant not settled")
	}
	second := make(chan outcome, 1)
	go func() { s, e := runner.StartWorkflowTurn(context.Background(), req); second <- outcome{s, e} }()
	select {
	case <-feature.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("second Start did not acquire new grant")
	}
	close(store.release)
	select {
	case firstResult := <-first:
		if !errors.Is(firstResult.err, harness.ErrConflict) {
			t.Errorf("stale Failed CAS unexpectedly succeeded: status=%s err=%v", firstResult.snapshot.Turn.Status, firstResult.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale failure projection blocked")
	}
	turnID := workflowTurnID(t, req)
	state, err := store.GetTurn(t.Context(), turnID)
	if err != nil || state.Status == harness.TurnFailed {
		t.Errorf("second executor invalidated by stale failure: status=%s err=%v", state.Status, err)
	}
	close(feature.release)
	select {
	case result := <-second:
		if result.err != nil || result.snapshot.Turn.Status != harness.TurnCompleted {
			t.Errorf("new executor failed: status=%s err=%v", result.snapshot.Turn.Status, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new executor did not complete")
	}
}
