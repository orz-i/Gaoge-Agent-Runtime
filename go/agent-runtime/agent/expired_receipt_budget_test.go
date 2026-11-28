package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type receiptLifecycleBudgetPorts struct {
	*frozenReceiptBudgetPorts
	retarget   string
	projectErr error
	onProject  func(context.Context, budget.Ledger) error
}

func (p *receiptLifecycleBudgetPorts) ResolveScope(ctx context.Context, runID string) (BoundedBudgetScope, error) {
	s, err := p.frozenReceiptBudgetPorts.ResolveScope(ctx, runID)
	if p.retarget != "" {
		s.LedgerID = p.retarget
	}
	return s, err
}
func (p *receiptLifecycleBudgetPorts) Project(ctx context.Context, l budget.Ledger) error {
	if p.onProject != nil {
		if err := p.onProject(ctx, l); err != nil {
			return err
		}
	}
	return p.projectErr
}

type receiptHydrationPorts struct {
	BoundedContextPorts
	onHydrate func(context.Context) error
}

func (p *receiptHydrationPorts) Hydrate(ctx context.Context, inv plugin.RunInvocation) (context.Context, BoundedContextHydration, error) {
	if p.onHydrate != nil {
		if err := p.onHydrate(ctx); err != nil {
			return ctx, BoundedContextHydration{}, err
		}
	}
	return ctx, BoundedContextHydration{RunID: inv.RunID, SourceBinding: json.RawMessage(`{"receipt-only":true}`)}, nil
}

func (*receiptHydrationPorts) Plan(_ context.Context, request model.Request) (BoundedContextPlan, error) {
	return BoundedContextPlan{Request: request}, nil
}

func TestExpiredReceiptPreservesElectedBudgetCancellation(t *testing.T) {
	for _, mode := range []string{"cancelled", "completed", "retarget", "projection-failed", "cancel-during-project-live", "cancel-during-project-expired", "cancel-after-run-admission-live", "cancel-after-run-admission-expired"} {
		t.Run(mode, func(t *testing.T) { testExpiredReceiptBudgetLifecycle(t, mode) })
	}
}
func testExpiredReceiptBudgetLifecycle(t *testing.T, mode string) {
	ctx := t.Context()
	storeClock := &modelRetryTestClock{now: time.Now().UTC()}
	base := memory.NewStoreWithExecutionClock(storeClock)
	fault := &summaryCoreFaultStore{Store: base, event: "agent.model_invocation.completed"}
	_, rt, contextPorts, provider, clock, start := newSummaryCoreFixture(t, fault)
	start.WorkingStateSummary = nil
	deadline := storeClock.now.Add(time.Minute)
	start.DeadlineAt = &deadline
	ledgerStore := budget.NewMemoryLedgerStore()
	ports := &receiptLifecycleBudgetPorts{frozenReceiptBudgetPorts: &frozenReceiptBudgetPorts{}}
	shared, e := NewBoundedBudgetAdapter(ledgerStore, ports)
	if e != nil {
		t.Fatal(e)
	}
	deps := Dependencies{Runtime: rt, Model: provider, Clock: clock, BoundedBudget: shared}
	hydrate := &receiptHydrationPorts{}
	if strings.HasPrefix(mode, "cancel-after-run-admission") {
		inner, e := NewBoundedContextAdapter(contextPorts.manager, hydrate)
		if e != nil {
			t.Fatal(e)
		}
		deps.BoundedContext = inner
	}
	r, e := NewRunner(deps)
	if e != nil {
		t.Fatal(e)
	}
	before, e := r.StartRun(ctx, start)
	if !errors.Is(e, ErrRootOutcomeUnknown) || !fault.fired || len(provider.calls()) != 1 {
		t.Fatalf("original receipt fixture: %v %d", e, len(provider.calls()))
	}
	repaired, e := r.ReconcileModelOutcome(ctx, before.Run.ID, before.Run.Revision)
	if e != nil {
		t.Fatalf("reconcile exact original receipt: %v", e)
	}
	view, e := ViewState(repaired)
	if e != nil || view.ModelInvocations[0].Status != ModelInvocationCompleted {
		t.Fatalf("missing completed receipt: %v", e)
	}
	if mode == "cancelled" {
		ledger, e := (budget.Coordinator{Store: ledgerStore}).CancelRoot(ctx, "turn", start.ID)
		if e != nil || !ledger.RunCancelled(start.ID) {
			t.Fatalf("elect original budget cancellation: %v", e)
		}
		control, e := r.Resume(ctx, start.ID, repaired.Run.Revision)
		if !errors.Is(e, budget.ErrExhausted) || kernel.ManagedDigest(control) != kernel.ManagedDigest(repaired) {
			t.Fatalf("live cancellation not denied: %v", e)
		}
	}
	projectionErr := errors.New("receipt projection unavailable")
	if mode == "retarget" {
		ports.retarget = "other-ledger"
	}
	if mode == "projection-failed" {
		ports.projectErr = projectionErr
	}
	if mode == "cancel-during-project-live" || mode == "cancel-during-project-expired" {
		ports.onProject = func(ctx context.Context, l budget.Ledger) error {
			ports.onProject = nil
			if l.RunCancelled(start.ID) {
				t.Fatal("cancellation unexpectedly already elected")
			}
			cancelled, e := (budget.Coordinator{Store: ledgerStore}).CancelRoot(ctx, l.ID, start.ID)
			if e != nil || !cancelled.RunCancelled(start.ID) {
				t.Fatalf("real election failed: %v", e)
			}
			t.Logf("cancel elected in pre-Next projection: old revision=%d new=%d", l.Revision, cancelled.Revision)
			return nil
		}
	}
	if strings.HasPrefix(mode, "cancel-after-run-admission") {
		hydrate.onHydrate = func(ctx context.Context) error {
			hydrate.onHydrate = nil
			l, e := (budget.Coordinator{Store: ledgerStore}).CancelRoot(ctx, "turn", start.ID)
			if e != nil || !l.RunCancelled(start.ID) {
				t.Fatalf("inner hydration did not elect cancellation: %v", e)
			}
			return nil
		}
	}
	if !strings.HasSuffix(mode, "-live") {
		storeClock.now = deadline.Add(time.Second)
	}
	out, e := r.Resume(ctx, start.ID, repaired.Run.Revision)
	after, loadErr := ledgerStore.LoadBudget(ctx, "turn")
	t.Logf("expired deadline: status=%s rev=%d error=%v ledgerCancelled=%t finished=%t providerCalls=%d", out.Run.Status, out.Run.Revision, e, after.RunCancelled(start.ID), after.FinishedRuns[start.ID], len(provider.calls()))
	if loadErr != nil || len(provider.calls()) != 1 {
		t.Fatalf("fixture lost ledger or resent provider: %v", loadErr)
	}
	if mode == "completed" {
		if e != nil || out.Run.Status != kernel.RunStatusCompleted || !after.FinishedRuns[start.ID] {
			t.Fatalf("native receipt did not finish original budget obligation: %v", e)
		}
		return
	}
	want := budget.ErrExhausted
	if mode == "retarget" {
		want = budget.ErrLedgerConflict
	}
	if mode == "projection-failed" {
		want = projectionErr
	}
	persisted, readErr := rt.Load(ctx, start.ID)
	if readErr != nil || kernel.ManagedDigest(persisted) != kernel.ManagedDigest(repaired) || !errors.Is(e, want) || after.FinishedRuns[start.ID] {
		t.Fatalf("expired receipt bypassed original budget lifecycle: %v read=%v", e, readErr)
	}
	if strings.HasPrefix(mode, "cancel-") && (!after.RunCancelled(start.ID) || after.View("").Usage.LLMCalls != 1) {
		t.Fatal("receipt refusal lost the elected cancellation or original accounting")
	}
	if mode == "retarget" {
		if _, err := ledgerStore.LoadBudget(ctx, "other-ledger"); err == nil {
			t.Fatal("receipt created a replacement budget ledger")
		}
	}
}
