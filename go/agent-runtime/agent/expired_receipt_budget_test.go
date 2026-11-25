package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type receiptLifecycleBudgetPorts struct {
	*frozenReceiptBudgetPorts
	retarget   string
	projectErr error
}

func (p *receiptLifecycleBudgetPorts) ResolveScope(ctx context.Context, runID string) (BoundedBudgetScope, error) {
	s, err := p.frozenReceiptBudgetPorts.ResolveScope(ctx, runID)
	if p.retarget != "" {
		s.LedgerID = p.retarget
	}
	return s, err
}
func (p *receiptLifecycleBudgetPorts) Project(context.Context, budget.Ledger) error {
	return p.projectErr
}

func TestExpiredReceiptPreservesElectedBudgetCancellation(t *testing.T) {
	for _, mode := range []string{"cancelled", "completed", "retarget", "projection-failed"} {
		t.Run(mode, func(t *testing.T) { testExpiredReceiptBudgetLifecycle(t, mode) })
	}
}
func testExpiredReceiptBudgetLifecycle(t *testing.T, mode string) {
	ctx := t.Context()
	storeClock := &modelRetryTestClock{now: time.Now().UTC()}
	base := memory.NewStoreWithExecutionClock(storeClock)
	fault := &summaryCoreFaultStore{Store: base, event: "agent.model_invocation.completed"}
	_, rt, _, provider, clock, start := newSummaryCoreFixture(t, fault)
	start.WorkingStateSummary = nil
	deadline := storeClock.now.Add(time.Minute)
	start.DeadlineAt = &deadline
	ledgerStore := budget.NewMemoryLedgerStore()
	ports := &receiptLifecycleBudgetPorts{frozenReceiptBudgetPorts: &frozenReceiptBudgetPorts{}}
	shared, e := NewBoundedBudgetAdapter(ledgerStore, ports)
	if e != nil {
		t.Fatal(e)
	}
	deps := Dependencies{Runtime: rt, Model: provider, Clock: clock, RunMiddleware: []plugin.RunMiddleware{shared}, ModelMiddleware: []plugin.ModelMiddleware{shared}, ToolMiddleware: []plugin.ToolMiddleware{shared}}
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
	storeClock.now = deadline.Add(time.Second)
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
	if mode == "retarget" {
		if _, err := ledgerStore.LoadBudget(ctx, "other-ledger"); err == nil {
			t.Fatal("receipt created a replacement budget ledger")
		}
	}
}
