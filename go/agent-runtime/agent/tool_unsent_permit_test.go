package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type permitUnsentModel struct{ calls atomic.Int32 }

func (m *permitUnsentModel) Generate(context.Context, model.Request) (model.Response, error) {
	if m.calls.Add(1) == 1 {
		return model.Response{ToolCalls: []tools.Call{{ID: "permit-call", ToolKey: "read", Arguments: json.RawMessage("{}")}}}, nil
	}
	return model.Response{Content: "done"}, nil
}

// Occupy the actual shared Runner semaphore, not a simulated Budget error.
// Waiting for that permit must not create a shared reservation or dispatch
// intent, and the exact original child/root segment can relinquish ownership.
func TestNativeToolPermitPrecedesSharedCapacity(t *testing.T) {
	for _, parented := range []bool{false, true} {
		name := "root"
		if parented {
			name = "parented"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			ledger := budget.NewMemoryLedgerStore()
			adapter, err := NewBoundedBudgetAdapter(ledger, toolAccountingPorts{})
			if err != nil {
				t.Fatal(err)
			}
			m := &permitUnsentModel{}
			var physical, approvals atomic.Int32
			policy, err := NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
				approvals.Add(1)
				return plugin.ApprovalNotRequired, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical.Add(1)
				return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewRunner(Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, ApprovalPolicies: []plugin.ApprovalPolicy{policy}, BoundedBudget: adapter, StartControlPreflight: func(context.Context, StartRequest) error { return nil },
				Execution: ExecutionPolicy{Tool: CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "test", ID: "thread"}
			req := StartRequest{ID: "permit-root", Actor: actor, Thread: thread, Goal: "read", ToolKeys: []string{"read"}}
			if parented {
				parent, err := rt.Create(ctx, kernel.CreateRequest{ID: "permit-parent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				req.ParentRunID = parent.Run.ID
				req.OriginFingerprint = FrozenOriginFingerprint([]byte("{}"))
			}
			held, err := r.toolLimiter.acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			release := sync.OnceFunc(held)
			defer release()
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			type result struct {
				s   kernel.Snapshot
				err error
			}
			done := make(chan result, 1)
			go func() { s, err := r.StartRun(callCtx, req); done <- result{s, err} }()
			timeout := time.NewTimer(3 * time.Second)
			defer timeout.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			var ready kernel.Snapshot
			for ready.Run.ID == "" {
				select {
				case <-timeout.C:
					t.Fatal("did not reach the original authorization before semaphore wait")
				case got := <-done:
					t.Fatal("call did not wait for local permit", got.err, got.s.Run.Status)
				case <-ticker.C:
					s, err := rt.Load(ctx, req.ID)
					if err != nil {
						continue
					}
					state, err := decodeState(s.State)
					if err == nil && state.ToolAdmission != nil && state.ToolAdmission.Phase == toolAdmissionAuthorized {
						ready = s
					}
				}
			}
			admitted, err := ledger.LoadBudget(ctx, "turn")
			if err != nil || admitted.View("").Usage.LLMCalls != 1 || admitted.View("").Reserved.ToolCalls != 0 {
				t.Fatal("local queue changed the original model-only accounting", err, admitted.View(""))
			}
			if _, exists := admitted.Reservations["tool:"+req.ID+":permit-call"]; exists {
				t.Fatal("local queue acquired shared Tool capacity")
			}
			cancel()
			select {
			case got := <-done:
				if !errors.Is(got.err, context.Canceled) || errors.Is(got.err, ErrRootOutcomeUnknown) || errors.Is(got.err, kernel.ErrChildExecutionBusy) {
					t.Fatal("known local permit cancellation became unknown effect", got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled waiter did not return")
			}
			if physical.Load() != 0 || approvals.Load() != 1 || m.calls.Load() != 1 {
				t.Fatal("queued call executed", physical.Load(), approvals.Load(), m.calls.Load())
			}
			if _, err = rt.LookupChildExecution(ctx, req.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("known unsent grant retained", err)
			}
			current, err := rt.Load(ctx, req.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, marked, err := FrozenPendingDispatch(current); err != nil || marked {
				t.Fatal("local wait committed physical marker", err)
			}
			release()
			final, err := r.Resume(ctx, req.ID, current.Run.Revision)
			if err != nil || final.Run.Status != kernel.RunStatusCompleted || physical.Load() != 1 || approvals.Load() != 1 || m.calls.Load() != 2 {
				t.Fatal("original permit re-admission failed", err, final.Run.Status, physical.Load(), approvals.Load(), m.calls.Load())
			}
			saved, err := ledger.LoadBudget(ctx, "turn")
			if err != nil || saved.View("").Usage.ToolCalls != 1 || saved.View("").Reserved.ToolCalls != 0 {
				t.Fatal("wrong final charge", err, saved.View(""))
			}
		})
	}
}
