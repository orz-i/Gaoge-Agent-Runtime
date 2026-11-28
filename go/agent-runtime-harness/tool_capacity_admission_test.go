package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Actual Harness scope/ledger and timeline ports must not publish a Tool
// Started/Failed item for a capacity-only denial. A later owner may acquire
// the original reservation, then enter the fixed budget and timeline order exactly once.
func TestHarnessToolCapacityWaitIsNotPhysicalDispatch(t *testing.T) {
	ctx := t.Context()
	limits := budget.Limits{MaxToolCalls: 3, MaxConcurrentRuns: 1}
	store, shared, _ := hostedBudgetFixture(t, limits)
	const ledgerID = "hosted-budget-turn"
	ks := memory.NewStore()
	coord := budget.Coordinator{Store: ks}
	if _, err := coord.Ensure(ctx, ledgerID, limits); err != nil {
		t.Fatal(err)
	}
	peerBlocked := false
	approval, err := agent.NewBoundedToolApproval(func(c context.Context, _ tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
		if !peerBlocked {
			original, err := ks.LoadBudget(c, ledgerID)
			if err != nil || original.View("").Usage.LLMCalls != 1 {
				t.Fatalf("first model not settled before capacity competition: %v", err)
			}
			if _, err := coord.RegisterRun(c, ledgerID, "peer-run", budget.RunBudget{}); err != nil {
				return "", err
			}
			if _, err := coord.Reserve(c, ledgerID, budget.Reservation{ID: "peer-slot", RunID: "peer-run", RequestHash: kernel.ManagedDigest("peer-slot"), Requested: budget.Usage{ToolCalls: 1}}, true); err != nil {
				return "", err
			}
			if _, err := coord.Dispatch(c, ledgerID, "peer-slot"); err != nil {
				return "", err
			}
			peerBlocked = true
		}
		return plugin.ApprovalNotRequired, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	m := &heldToolRecoveryModel{}
	physical, lookups := 0, 0
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		physical++
		return tools.ExecutionResult{Content: json.RawMessage(`{ "answer": true }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	makeRunner := func(authority *BudgetAuthority) *agent.Runner {
		t.Helper()
		rt, e := kernel.New(kernel.Dependencies{Store: ks})
		if e != nil {
			t.Fatal(e)
		}
		track, e := NewBoundedToolTimelineAdapter(store, hostedBudgetClock{}, nil)
		if e != nil {
			t.Fatal(e)
		}
		b := mustBudgetAdapter(t, bindHostedBudgetAuthority(t, rt, authority))
		r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: b, ToolTimeline: track, ApprovalPolicies: []plugin.ApprovalPolicy{approval}, ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			lookups++
			return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
		})})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := makeRunner(shared)
	cur, err := r.StartRun(ctx, agent.StartRequest{ID: "hosted-run", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"local.read"}})
	if err != nil || cur.Run.Status != kernel.RunStatusRunning || physical != 0 || len(m.requests) != 1 {
		t.Fatal("capacity wait not reached", err, cur.Run.Status, physical)
	}
	items, err := store.ListItems(ctx, ledgerID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == ItemTool {
			t.Fatal("capacity wait invented physical timeline", item)
		}
	}
	l, err := ks.LoadBudget(ctx, ledgerID)
	if err != nil {
		t.Fatal(err)
	}
	wait := l.Reservations["tool:hosted-run:held-call"]
	if wait.ID != "" || wait.Dispatched || wait.Active {
		t.Fatal("capacity wait dispatched", wait)
	}
	if _, err = ks.LookupChildExecution(ctx, cur.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("capacity wait retained grant", err)
	}
	if _, err = r.ReconcileToolOutcome(ctx, cur.Run.ID, cur.Run.Revision, "held-call"); !errors.Is(err, agent.ErrToolOutcomeUnknown) || lookups != 0 {
		t.Fatal("capacity wait used receipt verifier", err)
	}
	if _, err = coord.Settle(ctx, ledgerID, "peer-slot", budget.Usage{}, json.RawMessage("{}")); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	final, err := makeRunner(restarted).Resume(ctx, cur.Run.ID, cur.Run.Revision)
	if err != nil || final.Run.Status != kernel.RunStatusCompleted || physical != 1 || len(m.requests) != 2 || lookups != 0 {
		t.Fatal("original capacity recovery failed", err, final.Run.Status, physical, lookups)
	}
	items, err = store.ListItems(ctx, ledgerID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	started, completed := 0, 0
	for _, item := range items {
		if item.Kind == ItemTool {
			switch item.Status {
			case ItemStarted:
				started++
			case ItemCompleted:
				completed++
			case ItemWaiting, ItemFailed, ItemCancelled:
				t.Fatal("unexpected physical timeline", item.Status)
			default:
				t.Fatal("unknown physical timeline", item.Status)
			}
		}
	}
	if started != 1 || completed != 1 {
		t.Fatal("original Tool timeline not exactly once", started, completed)
	}
	l, err = ks.LoadBudget(ctx, ledgerID)
	if err != nil || l.View("").Usage.ToolCalls != 1 || l.View("").Reserved.ToolCalls != 0 {
		t.Fatal("wrong original capacity accounting", err, l.View(""))
	}
}
