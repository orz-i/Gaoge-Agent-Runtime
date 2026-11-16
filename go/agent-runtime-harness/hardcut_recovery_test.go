package harness_test

import (
	"context"
	"errors"
	"strconv"
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
	_, err = newRunner(flaky).StartAgentTurn(t.Context(), request)
	if err == nil || !flaky.failed {
		t.Fatalf("failed to inject before host item commit: %v", err)
	}
	recovered, err := newRunner(backing).StartAgentTurn(t.Context(), request)
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
	_, err = newRunner(flaky).StartAgentTurn(t.Context(), first)
	if err == nil || !flaky.failed {
		t.Fatalf("failed to inject between Invocation and Agent Run: %v", err)
	}
	changed := first
	changed.CallAllowance = nil
	recovered, err := newRunner(backing).StartAgentTurn(t.Context(), changed)
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
	_, err = newRunner(flaky).StartAgentTurn(t.Context(), request)
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
	_, err := first.StartAgentTurn(t.Context(), req)
	if err == nil || !flaky.failed {
		t.Fatalf("expected fault before context commit, err=%v", err)
	}
	second, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, clock, nil)
	req.Context = contextSeed("REPLACED private instructions", contextEntry("same-source", "turn", model.RoleUser, "answer directly", true))
	_, err = second.StartAgentTurn(t.Context(), req)
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
			snapshot, err := runner.StartAgentTurn(context.Background(), testStartRequest())
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < count; i++ {
				_, _, err = store.AppendItem(context.Background(), harness.Item{ID: "audit-extra-" + strconv.Itoa(i), TurnID: snapshot.Turn.ID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted})
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
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 1100 {
		_, _, err = store.AppendItem(t.Context(), harness.Item{ID: "paged-item-" + strconv.Itoa(i), TurnID: first.Turn.ID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted})
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := runner.LoadState(t.Context(), first.Turn.ID)
	if err != nil || state.Turn != first.Turn || state.Session != first.Session || state.Config.ContentHash != first.Config.ContentHash {
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
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
			if err != nil {
				b.Fatal(err)
			}
			first, err := runner.StartAgentTurn(context.Background(), testStartRequest())
			if err != nil {
				b.Fatal(err)
			}
			for i := range count {
				if _, _, err := store.AppendItem(context.Background(), harness.Item{ID: "state-item-" + strconv.Itoa(i), TurnID: first.Turn.ID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted}); err != nil {
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
	host, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := host.StartAgentTurn(t.Context(), testStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	cancelled := finished.Turn
	cancelled.Status = harness.TurnCancelled
	cancelled.UpdatedAt = fixedClock{}.Now()
	cancelled, err = harness.TerminalFixtureTurnForTest(store, cancelled, cancelled.Revision)
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
