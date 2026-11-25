package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// All public RunChain entrypoints consume the same original native return.
// A short-circuit after Create is a settled original Run, not a pre-Create
// NoEffect marker, and an approval denial must not resolve its checkpoint.
func TestNativeRunAdmissionDenialAtStartPrepareAndApproval(t *testing.T) {
	for _, op := range []string{"start", "prepare", "approval"} {
		t.Run(op, func(t *testing.T) {
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
			physical := 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			gate, err := interaction.New(rt)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
				return plugin.ApprovalRequired, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewRunner(Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, Approvals: interactionadapter.New(gate), ApprovalPolicies: []plugin.ApprovalPolicy{policy}, RunMiddleware: []plugin.RunMiddleware{adapter}, ToolMiddleware: []plugin.ToolMiddleware{adapter}})
			if err != nil {
				t.Fatal(err)
			}
			req := StartRequest{ID: "native-admission-root", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}}
			c := budget.Coordinator{Store: ledger}
			limits := budget.Limits{MaxToolCalls: 2}
			var before kernel.Snapshot
			if op == "approval" {
				before, err = r.StartRun(ctx, req)
				if err != nil || before.Run.Status != kernel.RunStatusWaitingInput {
					t.Fatal("approval fixture", err, before.Run)
				}
			} else {
				if _, err = c.Ensure(ctx, "turn", limits); err != nil {
					t.Fatal(err)
				}
				if _, err = c.RegisterRun(ctx, "turn", req.ID, budget.RunBudget{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = c.CancelRoot(ctx, "turn", req.ID); err != nil {
				t.Fatal(err)
			}
			invOp := plugin.RunStart
			if op == "approval" {
				invOp = plugin.RunResolveApproval
			}
			got, obs, err := r.ObservePhysicalCall(ctx, req.ID, invOp, func(c context.Context) (kernel.Snapshot, error) {
				switch op {
				case "start":
					return r.StartRun(c, req)
				case "prepare":
					return r.PrepareRun(c, req)
				default:
					return r.ResolveApproval(c, req.ID, before.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
				}
			})
			if !errors.Is(err, budget.ErrExhausted) || errors.Is(err, ErrRootOutcomeUnknown) || got.Run.ID != req.ID || obs.Kind() != PhysicalCallSettled || physical != 0 {
				t.Fatal("native denied operation not settled", err, got.Run, obs.Kind(), physical)
			}
			if op == "approval" {
				if got.Run.Revision != before.Run.Revision || got.Checkpoint == nil || got.Checkpoint.Status != kernel.CheckpointPending || m.calls.Load() != 1 {
					t.Fatal("denied approval mutated or sampled", got.Run, got.Checkpoint)
				}
			} else if got.Run.Status != kernel.RunStatusRunning || m.calls.Load() != 0 {
				t.Fatal("denied Start/Prepare invoked model", got.Run, m.calls.Load())
			}
			if _, err = rt.LookupChildExecution(ctx, req.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("native denied operation retained grant", err)
			}
			stopped, err := rt.Cancel(ctx, req.ID, got.Run.Revision, "elected cancellation")
			if err != nil || stopped.Run.Status != kernel.RunStatusCancelled {
				t.Fatal("original cancellation blocked", err, stopped.Run)
			}
		})
	}
}
