package harness

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type budgetClaimClock struct{ nanos atomic.Int64 }

func newBudgetClaimClock() *budgetClaimClock {
	value := &budgetClaimClock{}
	value.nanos.Store(time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC).UnixNano())
	return value
}
func (clock *budgetClaimClock) Now() time.Time          { return time.Unix(0, clock.nanos.Load()).UTC() }
func (clock *budgetClaimClock) Advance(d time.Duration) { clock.nanos.Add(d.Nanoseconds()) }

type heldBudgetModelMiddleware struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*heldBudgetModelMiddleware) Name() string { return "test.wait-before-physical-model" }
func (middleware *heldBudgetModelMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	middleware.once.Do(func() { close(middleware.entered) })
	select {
	case <-middleware.release:
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	return next(ctx, request, emit)
}

type countedBudgetModel struct{ calls atomic.Int64 }

func (client *countedBudgetModel) Generate(context.Context, model.Request) (model.Response, error) {
	client.calls.Add(1)
	return model.Response{Content: "successful recovery"}, nil
}

// Shared Budget persists its dispatched marker BEFORE calling the next
// Middleware. If Agent's final Claim gate then proves the Provider was never
// invoked, the budget must not leave an unknown charge that permanently
// prevents a valid later Resume with the same durable invocation identity.
func TestSharedBudgetReleasesProvenUnsentModelClaimAndAllowsRecovery(t *testing.T) {
	store, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2, MaxConcurrentRuns: 1})
	clock := newBudgetClaimClock()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	held := &heldBudgetModelMiddleware{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(held.release) }) }
	t.Cleanup(unblock)
	provider := &countedBudgetModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: provider, ModelMiddleware: []plugin.ModelMiddleware{shared, held}})
	if err != nil {
		t.Fatal(err)
	}
	type runResult struct {
		snapshot kernel.Snapshot
		err      error
	}
	outcomes := make(chan runResult, 1)
	go func() {
		snapshot, e := runner.StartRun(context.Background(), agent.StartRequest{
			ID: "hosted-run", Actor: kernel.ActorRef{TenantID: "audit", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "budget-claim"}, Goal: "recover", Model: "audit-model",
		})
		outcomes <- runResult{snapshot, e}
	}()
	select {
	case <-held.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("shared Budget did not enter Model middleware")
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if !ledger.View("").UnknownUsage || provider.calls.Load() != 0 {
		t.Fatalf("fixture did not persist preflight dispatch marker: %+v calls=%d", ledger.View(""), provider.calls.Load())
	}
	clock.Advance(3 * time.Minute)
	unblock()
	var pending kernel.Snapshot
	select {
	case finished := <-outcomes:
		pending = finished.snapshot
		if !errors.Is(finished.err, agent.ErrModelInvocationBusy) || pending.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("expired Claim was not rejected safely: %+v err=%v", pending.Run, finished.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("expired worker did not finish")
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.View("").UnknownUsage || ledger.View("").Reserved.LLMCalls != 0 || provider.calls.Load() != 0 {
		t.Fatalf("proven unsent dispatch retained uncertain quota: %+v calls=%d", ledger.View(""), provider.calls.Load())
	}
	recovered, err := runner.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || recovered.Run.Status != kernel.RunStatusCompleted || provider.calls.Load() != 1 {
		t.Fatalf("valid new Claim could not recover same request: %+v err=%v calls=%d", recovered.Run, err, provider.calls.Load())
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.UnknownUsage || view.Usage.LLMCalls != 1 || view.Reserved.LLMCalls != 0 {
		t.Fatalf("recovery shared budget not settled exactly once: %+v", view)
	}
}

func TestSharedBudgetCannotTreatProviderErrorAsUnsentProof(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 1})
	// A remote Provider error may happen to be identical to an Agent claim
	// rejection. Only the Agent's private terminal-boundary proof can refund.
	_, err := shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		return model.Response{}, agent.ErrModelInvocationBusy
	})
	if !errors.Is(err, agent.ErrModelInvocationBusy) || agent.IsModelPreDispatchDenied(err) {
		t.Fatalf("ordinary Provider error was misclassified as no-send: %v", err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil || !ledger.View("").UnknownUsage || !ledger.Reservations["model:"+request.InvocationID].Dispatched {
		t.Fatalf("uncertain physical Provider result was refunded: ledger=%+v, err=%v", ledger, err)
	}
	_, err = shared.Model(t.Context(), request, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		t.Fatal("unknown Provider result was sent again")
		return model.Response{}, nil
	})
	if !errors.Is(err, budget.ErrWaiting) {
		t.Fatalf("unknown request regained dispatch permission: %v", err)
	}
}

// Input preparation is the single pre-intent Agent boundary. Failing there
// never touches the shared budget ledger, so no post-intent preparation or
// release bridge is needed for a parent to reuse the same native allowance.
func TestPreIntentFailureDoesNotReserveSharedModelBudget(t *testing.T) {
	store, shared, parent := hostedBudgetFixture(t, budget.Limits{
		MaxLLMCalls: 2, MaxHostedToolCalls: 1, MaxToolCalls: 1,
	})
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &countedBudgetModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: provider, ModelMiddleware: []plugin.ModelMiddleware{shared},
		PrepareModelRequest: func(context.Context, model.Request) (model.Request, error) {
			return model.Request{}, errors.New("pre-intent context admission rejected")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), agent.StartRequest{ID: parent.RunID,
		Actor:  kernel.ActorRef{TenantID: "audit", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "pre-intent"}, Goal: "oversized context",
	})
	if err == nil {
		t.Fatal("pre-intent rejection was not observed")
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil && !errors.Is(err, budget.ErrLedgerNotFound) {
		t.Fatal(err)
	}
	// The strongest outcome is that no shared ledger was even created.
	if provider.calls.Load() != 0 || (err == nil && (len(ledger.Reservations) != 0 || ledger.View("").UnknownUsage)) {
		t.Fatalf("pre-intent failure touched shared budget: %+v", ledger)
	}
	// A separate pre-approved model step may consume the still-available
	// hosted Tool allowance in exactly one budget receipt.
	parent.InvocationID = "parent-uses-unsent-budget"
	_, err = shared.Model(t.Context(), parent, nil, func(_ context.Context, bounded model.Request, _ model.StreamSink) (model.Response, error) {
		if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
			t.Fatalf("preparation failure reserved the hosted budget: %+v", bounded)
		}
		return model.Response{Content: "parent result", HostedToolCalls: []model.HostedToolCall{{
			ID: "native-parent-1", ToolKey: "native.search", Status: "completed",
		}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if view := ledger.View(""); view.Usage.LLMCalls != 1 || view.Usage.HostedToolCalls != 1 || view.UnknownUsage {
		t.Fatalf("parent budget was not available after pre-intent error: %+v", view)
	}
}

type corruptFrozenModelRequest struct{}

func (corruptFrozenModelRequest) Name() string { return "test.invalid-frozen-model" }
func (corruptFrozenModelRequest) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	request.Model = "unauthorized-model"
	return next(ctx, request, emit)
}

func TestPermanentModelDispatchViolationTerminatesAndReleasesUnsentBudget(t *testing.T) {
	store, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2})
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &countedBudgetModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		ModelMiddleware: []plugin.ModelMiddleware{shared, corruptFrozenModelRequest{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "hosted-run", Actor: kernel.ActorRef{TenantID: "audit", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "invalid-dispatch"},
		Goal:   "should never be dispatched", Model: "original-model",
	})
	if !errors.Is(err, agent.ErrInvalidRequest) || failed.Run.Status != kernel.RunStatusFailed ||
		failed.Run.ErrorCode != "agent.model_dispatch_invalid" || provider.calls.Load() != 0 {
		t.Fatalf("invalid model dispatch did not terminate safely: status=%s code=%q err=%v calls=%d",
			failed.Run.Status, failed.Run.ErrorCode, err, provider.calls.Load())
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	reservation := ledger.Reservations["model:"+agentInvocationID(t, failed)]
	if reservation.Dispatched || ledger.View("").UnknownUsage || ledger.View("").Reserved.LLMCalls != 0 {
		t.Fatalf("permanently invalid no-send leaked shared-budget quota: %+v view=%+v", reservation, ledger.View(""))
	}
	_, err = runner.Resume(t.Context(), failed.Run.ID, failed.Run.Revision)
	if !errors.Is(err, agent.ErrRunTerminal) || provider.calls.Load() != 0 {
		t.Fatalf("permanent middleware mutation was reissued: err=%v calls=%d", err, provider.calls.Load())
	}
}

func agentInvocationID(t *testing.T, snapshot kernel.Snapshot) string {
	t.Helper()
	view, err := agent.ViewState(snapshot)
	if err != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("invalid persistent Model Invocation: %v", err)
	}
	return view.ModelInvocations[0].ID
}
