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
)

type budgetClaimClock struct{ nanos atomic.Int64 }

func newBudgetClaimClock() *budgetClaimClock {
	value := &budgetClaimClock{}
	value.nanos.Store(time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC).UnixNano())
	return value
}
func (clock *budgetClaimClock) Now() time.Time          { return time.Unix(0, clock.nanos.Load()).UTC() }
func (clock *budgetClaimClock) Advance(d time.Duration) { clock.nanos.Add(d.Nanoseconds()) }

type countedBudgetModel struct {
	calls         atomic.Int64
	outputCeiling atomic.Int64
}

func (client *countedBudgetModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls.Add(1)
	client.outputCeiling.Store(request.MaxOutputTokens)
	return model.Response{Content: "successful recovery"}, nil
}

// The admission barrier precedes the atomic native dispatch. The final
// claim gate must not leave any reservation or unknown charge if no physical
// request was admitted; the exact durable invocation may later resume.
func TestSharedBudgetReleasesProvenUnsentModelClaimAndAllowsRecovery(t *testing.T) {
	testSharedBudgetUnsentClaim(t, 0)
}
func TestSharedBudgetPreservesFrozenOutputLimitOnProvenUnsentRecovery(t *testing.T) {
	testSharedBudgetUnsentClaim(t, 128)
}
func testSharedBudgetUnsentClaim(t *testing.T, outputCeiling int64) {
	_, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 2, MaxConcurrentRuns: 1})
	clock := newBudgetClaimClock()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	shared = bindHostedBudgetAuthority(t, runtime, shared)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	provider := &countedBudgetModel{}
	boundedBudget, err := shared.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: provider, BoundedBudget: boundedBudget, ModelAdmissionBarrier: agent.NewModelAdmissionBarrier(entered, release)})
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
			MaxModelOutputTokens: outputCeiling,
		})
		outcomes <- runResult{snapshot, e}
	}()
	select {
	case <-entered:
	case <-time.After(4 * time.Second):
		t.Fatal("shared Budget did not enter Model middleware")
	}
	ledger, err := runtime.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.View("").UnknownUsage || len(ledger.Reservations) != 0 || provider.calls.Load() != 0 {
		t.Fatalf("pre-entry barrier admitted a physical effect: %+v calls=%d", ledger.View(""), provider.calls.Load())
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
	ledger, err = runtime.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
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
	if provider.outputCeiling.Load() != outputCeiling {
		t.Fatalf("budget recovery changed the frozen output ceiling: got=%d want=%d", provider.outputCeiling.Load(), outputCeiling)
	}
	ledger, err = runtime.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
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
	var fixturePhysical1 model.Request
	_, err := func() (model.Response, error) {
		var admissionErr error
		fixturePhysical1, admissionErr = admitModelLedgerFixture(t.Context(), shared, request)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(q model.Request) (model.Response, error) {
			return model.Response{}, agent.ErrModelInvocationBusy
		}(fixturePhysical1)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical1, response, callErr)
	}()
	if !errors.Is(err, agent.ErrModelInvocationBusy) || agent.IsModelPreDispatchDenied(err) {
		t.Fatalf("ordinary Provider error was misclassified as no-send: %v", err)
	}
	ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
	if err != nil || !ledger.View("").UnknownUsage || !ledger.Reservations["model:"+request.InvocationID].Dispatched {
		t.Fatalf("uncertain physical Provider result was refunded: ledger=%+v, err=%v", ledger, err)
	}
	_, err = func() (model.Response, error) {
		_, e := admitModelLedgerFixture(t.Context(), shared, request)
		return model.Response{}, e
	}()
	if !errors.Is(err, budget.ErrLedgerConflict) {
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
	nativeShared := bindHostedBudgetAuthority(t, rt, shared)
	provider := &countedBudgetModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: provider, BoundedBudget: mustBudgetAdapter(t, nativeShared),
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
	ledger, err := rt.BudgetStore().LoadBudget(t.Context(), "hosted-budget-turn")
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
	var fixturePhysical2 model.Request
	_, err = func() (model.Response, error) {
		var admissionErr error
		fixturePhysical2, admissionErr = admitModelLedgerFixture(t.Context(), shared, parent)
		if admissionErr != nil {
			return model.Response{}, admissionErr
		}
		response, callErr := func(bounded model.Request) (model.Response, error) {
			if bounded.HostedToolCallLimit == nil || *bounded.HostedToolCallLimit != 1 {
				t.Fatalf("preparation failure reserved the hosted budget: %+v", bounded)
			}
			return model.Response{Content: "parent result", HostedToolCalls: []model.HostedToolCall{{
				ID: "native-parent-1", ToolKey: "native.search", Status: "completed",
			}}}, nil
		}(fixturePhysical2)
		return settleModelLedgerFixture(t.Context(), shared, fixturePhysical2, response, callErr)
	}()
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

func agentInvocationID(t *testing.T, snapshot kernel.Snapshot) string {
	t.Helper()
	view, err := agent.ViewState(snapshot)
	if err != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("invalid persistent Model Invocation: %v", err)
	}
	return view.ModelInvocations[0].ID
}
