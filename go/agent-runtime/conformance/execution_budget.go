package conformance

// Conformance for the actual Run/ledger atomic Store write paths.
import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type ExecutionBudgetStore struct {
	Store  kernel.Store
	Ledger budget.LedgerStore
	// Optional independent reopen of the actual native schema.
	Reopen func(*testing.T) (kernel.Store, budget.LedgerStore)
}

func RunExecutionBudgetSuite(t *testing.T, factory func(*testing.T) ExecutionBudgetStore) {
	t.Run("native_model", func(t *testing.T) { runNativeModelBudget(t, factory) })
	t.Run("native_tool", func(t *testing.T) { runNativeToolBudget(t, factory) })
	t.Run("native_control", func(t *testing.T) { runNativeBudgetControl(t, factory) })
	t.Run("native_binding", func(t *testing.T) { runNativeBudgetBinding(t, factory) })
	const id = "run"
	const scope = "scope"
	atomic := func(f ExecutionBudgetStore, ctx context.Context, lease *kernel.ExecutionLease, guard *kernel.ControlGuard, m kernel.StoreMutation, change func(budget.Coordinator) error) error {
		if guard != nil {
			_, _, e := f.Store.(kernel.ControlStore).ApplyControl(ctx, m.Record.Run.ID, m.Record.Run.Revision-1, *guard, m)
			return e
		}
		original, e := f.Ledger.LoadBudget(ctx, scope)
		if e != nil {
			return e
		}
		plan, e := budget.PlanLedgerMutation(ctx, original, change)
		if e != nil {
			return e
		}
		m.Ledger = plan
		rev := m.Record.Run.Revision - 1
		_, e = f.Store.(kernel.ExecutionStore).ApplyExecution(ctx, *lease, rev, m)
		return e
	}

	request := budget.Reservation{ID: "tool:run:call", RunID: id, RequestHash: "original", Requested: budget.Usage{ToolCalls: 1}}
	receipt := json.RawMessage(`{"call":"call","result":"ok"}`)
	reserve := func(ctx context.Context) func(budget.Coordinator) error {
		return func(c budget.Coordinator) error { _, e := c.Reserve(ctx, scope, request, true); return e }
	}
	dispatch := func(ctx context.Context) func(budget.Coordinator) error {
		return func(c budget.Coordinator) error { _, e := c.DispatchTool(ctx, scope, request); return e }
	}
	settle := func(ctx context.Context) func(budget.Coordinator) error {
		return func(c budget.Coordinator) error {
			_, e := c.Settle(ctx, scope, request.ID, request.Requested, receipt)
			return e
		}
	}
	cancel := func(ctx context.Context) func(budget.Coordinator) error {
		return func(c budget.Coordinator) error {
			l, e := c.Store.LoadBudget(ctx, scope)
			if e != nil {
				return e
			}
			// A terminal control must not erase an admitted unknown effect.
			for _, r := range l.Reservations {
				if r.RunID == id && r.Dispatched && r.Status != budget.ReservationSettled {
					return kernel.ErrChildExecutionBusy
				}
			}
			_, e = c.CancelRun(ctx, scope, id)
			return e
		}
	}
	guard := kernel.ControlGuard{Kind: kernel.ControlCancel}
	mutation := func(s kernel.Snapshot, status kernel.RunStatus, state json.RawMessage) kernel.StoreMutation {
		run := s.Run
		run.Revision++
		run.Status = status
		run.UpdatedAt = time.Now().UTC()
		if status == kernel.RunStatusCancelled {
			run.ErrorCode = "run.cancelled"
			run.EndedAt = &run.UpdatedAt
		}
		return kernel.StoreMutation{Record: kernel.Record{Run: run, State: state, Checkpoint: s.Checkpoint}, Events: []kernel.EventDraft{{Type: "execution.budget.transition"}}}
	}
	load := func(t *testing.T, f ExecutionBudgetStore) (kernel.Snapshot, budget.Ledger) {
		t.Helper()
		s, e := f.Store.Load(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		l, e := f.Ledger.LoadBudget(t.Context(), scope)
		if e != nil {
			t.Fatal(e)
		}
		return s, l
	}
	setup := func(t *testing.T) (ExecutionBudgetStore, kernel.ExecutionLease, kernel.Snapshot) {
		t.Helper()
		f := factory(t)
		ctx := t.Context()
		now := time.Now().UTC()
		s, e := f.Store.Create(ctx, kernel.Record{ExecutionRequired: true, BudgetEnrollment: &budget.RunEnrollment{ScopeID: scope, Limits: budget.Limits{MaxToolCalls: 1}}, Run: kernel.Run{ID: id, BudgetScopeID: scope, Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "thread"}, Goal: "execution budget", Revision: 1, Status: kernel.RunStatusRunning, CreatedAt: now, UpdatedAt: now}, State: json.RawMessage(`{"phase":"initial"}`)}, nil)
		if e != nil {
			t.Fatal(e)
		}
		lease, e := f.Store.(kernel.ExecutionStore).AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: id, OwnerID: "owner", LeaseDuration: time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		return f, lease, s
	}
	prepare := func(t *testing.T, f ExecutionBudgetStore, lease kernel.ExecutionLease, s kernel.Snapshot) kernel.Snapshot {
		t.Helper()
		if e := atomic(f, t.Context(), &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"prepared"}`)), reserve(t.Context())); e != nil {
			t.Fatal(e)
		}
		next, l := load(t, f)
		if next.Run.Revision != s.Run.Revision+1 || l.Reservations[request.ID].Status != budget.ReservationHeld {
			t.Fatalf("intent/reservation split: %v %v", next.Run.Revision, l)
		}
		return next
	}
	t.Run("effect_lifecycle_and_unknown", func(t *testing.T) {
		f, lease, s := setup(t)
		s = prepare(t, f, lease, s)
		if e := atomic(f, t.Context(), &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"dispatched"}`)), dispatch(t.Context())); e != nil {
			t.Fatal(e)
		}
		s, l := load(t, f)
		if !l.Reservations[request.ID].Dispatched {
			t.Fatal("dispatch lost")
		}
		if e := atomic(f, t.Context(), nil, &guard, mutation(s, kernel.RunStatusCancelled, s.State), cancel(t.Context())); !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unknown cancellation: %v", e)
		}
		after, l := load(t, f)
		if after.Run.Revision != s.Run.Revision || l.RunCancelled(id) || l.Reservations[request.ID].Status != budget.ReservationHeld {
			t.Fatal("rejected control mutated authority")
		}
		if e := atomic(f, t.Context(), &lease, nil, mutation(s, kernel.RunStatusRunning, receipt), settle(t.Context())); e != nil {
			t.Fatal(e)
		}
		// Discard the response and recover from both committed facts, not another dispatch.
		if f.Reopen != nil {
			f.Store, f.Ledger = f.Reopen(t)
		}
		s, l = load(t, f)
		r := l.Reservations[request.ID]
		if string(s.State) != string(receipt) || string(r.Receipt) != string(receipt) || r.Status != budget.ReservationSettled || r.Usage.ToolCalls != 1 {
			t.Fatalf("receipt/settlement split: %s %#v", s.State, r)
		}
		// The independently reopened Store can consume the original committed facts.
		if e := atomic(f, t.Context(), &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"consumed"}`)), settle(t.Context())); e != nil {
			t.Fatal(e)
		}
		_, l = load(t, f)
		if l.View(id).Usage.ToolCalls != 1 {
			t.Fatal("receipt replay double charged")
		}
	})
	t.Run("budget_denial_preserves_run", func(t *testing.T) {
		f, lease, s := setup(t)
		s = prepare(t, f, lease, s)
		_, before := load(t, f)
		denied := request
		denied.ID = "tool:run:other"
		denied.Requested.ToolCalls = 2
		e := atomic(f, t.Context(), &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"forbidden"}`)), func(c budget.Coordinator) error { _, e := c.Reserve(t.Context(), scope, denied, true); return e })
		if e == nil {
			t.Fatal("expected budget denial")
		}
		after, l := load(t, f)
		if after.Run.Revision != s.Run.Revision || after.EventHead != s.EventHead || l.Revision != before.Revision {
			t.Fatal("failed budget transition escaped")
		}
	})
	t.Run("rejected_cancel_and_stale_owner", func(t *testing.T) {
		f, lease, s := setup(t)
		s = prepare(t, f, lease, s)
		_, before := load(t, f)
		e := atomic(f, t.Context(), nil, &guard, mutation(s, kernel.RunStatusCancelled, json.RawMessage(`{"rewritten":true}`)), cancel(t.Context()))
		if e == nil {
			t.Fatal("invalid cancellation accepted")
		}
		after, l := load(t, f)
		if after.Run.Revision != s.Run.Revision || l.Revision != before.Revision || l.RunCancelled(id) {
			t.Fatal("rejected cancellation revoked budget")
		}
		x := f.Store.(kernel.ExecutionStore)
		if e = x.ReleaseExecution(t.Context(), lease); e != nil {
			t.Fatal(e)
		}
		if _, e = x.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: id, OwnerID: "successor", LeaseDuration: time.Minute}); e != nil {
			t.Fatal(e)
		}
		e = atomic(f, t.Context(), &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"dispatched"}`)), dispatch(t.Context()))
		if e == nil {
			t.Fatal("stale owner admitted")
		}
		after, l = load(t, f)
		if after.Run.Revision != s.Run.Revision || l.Revision != before.Revision || l.Reservations[request.ID].Dispatched {
			t.Fatal("stale owner changed authority")
		}
	})
	t.Run("cancel_competes_with_dispatch", func(t *testing.T) {
		f, lease, s := setup(t)
		s = prepare(t, f, lease, s)
		ctx := t.Context()
		var wg sync.WaitGroup
		start := make(chan struct{})
		var de, ce error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			de = atomic(f, ctx, &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"dispatched"}`)), dispatch(ctx))
		}()
		go func() {
			defer wg.Done()
			<-start
			ce = atomic(f, ctx, nil, &guard, mutation(s, kernel.RunStatusCancelled, s.State), cancel(ctx))
		}()
		close(start)
		wg.Wait()
		if (de == nil) == (ce == nil) {
			t.Fatalf("expected one winning commit: dispatch=%v cancel=%v", de, ce)
		}
		next, l := load(t, f)
		r := l.Reservations[request.ID]
		if next.Run.Revision != s.Run.Revision+1 {
			t.Fatal("unexpected revision")
		}
		if ce == nil {
			if next.Run.Status != kernel.RunStatusCancelled || !l.RunCancelled(id) || r.Dispatched || r.Status != budget.ReservationReleased {
				t.Fatal("cancel split")
			}
		} else {
			if next.Run.Status != kernel.RunStatusRunning || l.RunCancelled(id) || !r.Dispatched || r.Status != budget.ReservationHeld {
				t.Fatal("dispatch split")
			}
		}
	})
	t.Run("parent_cancel_competes_with_child_dispatch", func(t *testing.T) {
		f, _, root := setup(t)
		ctx := t.Context()
		childRun := root.Run
		childRun.ID = "child"
		childRun.ParentRunID = id
		childRun.BudgetParentRunID = id
		child, e := f.Store.Create(ctx, kernel.Record{ExecutionRequired: true, BudgetEnrollment: &budget.RunEnrollment{ScopeID: scope, Limits: budget.Limits{MaxToolCalls: 1}, Binding: budget.RunBudget{ParentRunID: id}}, Run: childRun, State: json.RawMessage(`{"phase":"initial"}`)}, nil)
		if e != nil {
			t.Fatal(e)
		}
		x := f.Store.(kernel.ExecutionStore)
		lease, e := x.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: childRun.ID, OwnerID: "child-owner", LeaseDuration: time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		childReq := request
		childReq.ID = "tool:child:call"
		childReq.RunID = childRun.ID
		if e = atomic(f, ctx, &lease, nil, mutation(child, kernel.RunStatusRunning, json.RawMessage(`{"phase":"prepared"}`)), func(c budget.Coordinator) error {
			_, e := c.Reserve(ctx, scope, childReq, true)
			return e
		}); e != nil {
			t.Fatal(e)
		}
		child, e = f.Store.Load(ctx, childRun.ID)
		if e != nil {
			t.Fatal(e)
		}
		// Parent control and child dispatch deliberately touch different Run rows.
		// The shared ledger must itself serialize interpretation of the unknown
		// effect and the revocation, not just CAS each Run independently.
		var wg sync.WaitGroup
		start := make(chan struct{})
		var de, ce error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			de = atomic(f, ctx, &lease, nil, mutation(child, kernel.RunStatusRunning, json.RawMessage(`{"phase":"dispatched"}`)), func(c budget.Coordinator) error {
				_, e := c.DispatchTool(ctx, scope, childReq)
				return e
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			ce = atomic(f, ctx, nil, &guard, mutation(root, kernel.RunStatusCancelled, root.State), func(c budget.Coordinator) error {
				l, e := c.Store.LoadBudget(ctx, scope)
				if e != nil {
					return e
				}
				r := l.Reservations[childReq.ID]
				if r.Dispatched && r.Status != budget.ReservationSettled {
					return kernel.ErrChildExecutionBusy
				}
				_, e = c.CancelRun(ctx, scope, id)
				return e
			})
		}()
		close(start)
		wg.Wait()
		if (de == nil) == (ce == nil) {
			t.Fatalf("expected one winning commit across Run rows: dispatch=%v cancel=%v", de, ce)
		}
		next, l := load(t, f)
		current, e := f.Store.Load(ctx, childRun.ID)
		if e != nil {
			t.Fatal(e)
		}
		r := l.Reservations[childReq.ID]
		if ce == nil {
			if next.Run.Status != kernel.RunStatusCancelled || !l.RunCancelled(childRun.ID) || r.Dispatched || r.Status != budget.ReservationReleased || current.Run.Revision != child.Run.Revision {
				t.Fatal("parent cancel/child dispatch split")
			}
		} else {
			if next.Run.Revision != root.Run.Revision || l.RunCancelled(childRun.ID) || !r.Dispatched || r.Status != budget.ReservationHeld || current.Run.Revision != child.Run.Revision+1 {
				t.Fatal("child dispatch/parent cancel split")
			}
		}
	})

	t.Run("lease_expiry_after_budget_plan", func(t *testing.T) {
		f, lease, s := setup(t)
		s = prepare(t, f, lease, s)
		_, before := load(t, f)
		ctx := t.Context()
		x := f.Store.(kernel.ExecutionStore)
		lease, e := x.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 50 * time.Millisecond})
		if e != nil {
			t.Fatal(e)
		}
		staged := false
		e = atomic(f, ctx, &lease, nil, mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"dispatched"}`)), func(c budget.Coordinator) error {
			if e := dispatch(ctx)(c); e != nil {
				return e
			}
			staged = true
			// Fault injection: delay after planning but before submitting the Store CAS.
			time.Sleep(100 * time.Millisecond)
			return nil
		})
		if !staged || !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("expired owner after budget plan: staged=%v err=%v", staged, e)
		}
		after, l := load(t, f)
		if after.Run.Revision != s.Run.Revision || l.Revision != before.Revision || l.Reservations[request.ID].Dispatched {
			t.Fatal("expired owner committed budget or Run")
		}
	})

	t.Run("stale_ledger_plan_rolls_back_run", func(t *testing.T) {
		f, lease, s := setup(t)
		ctx := t.Context()
		_, before := load(t, f)
		planned, e := budget.PlanLedgerMutation(ctx, before, reserve(ctx))
		if e != nil || planned == nil {
			t.Fatalf("plan: %v", e)
		}
		c := budget.Coordinator{Store: f.Ledger}
		if _, e = c.RegisterRun(ctx, scope, "sibling", budget.RunBudget{ParentRunID: id}); e != nil {
			t.Fatal(e)
		}
		current, e := f.Ledger.LoadBudget(ctx, scope)
		if e != nil {
			t.Fatal(e)
		}
		m := mutation(s, kernel.RunStatusRunning, json.RawMessage(`{"phase":"forbidden"}`))
		m.Ledger = planned
		if _, e = f.Store.(kernel.ExecutionStore).ApplyExecution(ctx, lease, s.Run.Revision, m); !errors.Is(e, budget.ErrLedgerConflict) {
			t.Fatalf("stale plan accepted: %v", e)
		}
		after, l := load(t, f)
		if after.Run.Revision != s.Run.Revision || after.EventHead != s.EventHead || l.Revision != current.Revision || l.Reservations[request.ID].ID != "" {
			t.Fatal("stale plan changed Run or ledger")
		}
	})

}
