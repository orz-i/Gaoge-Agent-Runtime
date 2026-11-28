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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type heldToolRecoveryFault struct {
	*memory.Store
	cut   error
	fired bool
}

func (s *heldToolRecoveryFault) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	for _, ev := range m.Events {
		if ev.Type == "tool.receipt_recovered" && !s.fired {
			s.fired = true
			return kernel.Snapshot{}, s.cut
		}
	}
	return s.Store.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
}

type heldToolRecoveryModel struct{ requests []model.Request }

func (m *heldToolRecoveryModel) Generate(_ context.Context, q model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(q))
	if len(m.requests) == 1 {
		return model.Response{ToolCalls: []tools.Call{{ID: "held-call", ToolKey: "local.read", Arguments: json.RawMessage("{}")}}}, nil
	}
	return model.Response{Content: "done"}, nil
}

// Actual Harness Budget ports and native Agent recovery use one original-byte
// settlement codec. Cut after ledger CAS, before native receipt CAS; the Host
// verifier is a read-only evidence source, not another settlement driver.
func TestVerifiedToolOutcomeSettlementIsIdempotentAndFrozen(t *testing.T) {
	for _, kind := range []string{"child_terminal", "scheduled", "pending"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			ledger, shared, _ := hostedBudgetFixture(t, budget.Limits{MaxToolCalls: 2})
			base := memory.NewStore()
			fault := &heldToolRecoveryFault{Store: base, cut: errors.New("after native ledger CAS, before receipt CAS")}
			m := &heldToolRecoveryModel{}
			physical, lookups := 0, 0
			const original = `{ "answer": "<>&" }`
			const changed = `{"answer":"<>&"}`
			raw := original
			registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				result := tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "pending"}}
				switch kind {
				case "child_terminal":
					result.Pending = &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: q.Call.ID, ChildRunID: "completed-child"}
				case "scheduled":
					result.Pending = &tools.PendingExecution{Kind: tools.PendingScheduled}
				}
				return result, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			makeRunner := func(s kernel.Store, withBudget bool) *agent.Runner {
				t.Helper()
				rt, e := kernel.New(kernel.Dependencies{Store: s})
				if e != nil {
					t.Fatal(e)
				}
				verifier := agent.ToolOutcomeVerifierFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					lookups++
					if q.RunID != "hosted-run" || q.Call.ID != "held-call" || string(q.Call.Arguments) != "{}" {
						t.Fatal("wrong original call", q)
					}
					return tools.ExecutionResult{Content: json.RawMessage(raw), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
				})
				deps := agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry, ToolOutcomeVerifier: verifier}
				if withBudget {
					deps.BoundedBudget = mustBudgetAdapter(t, shared)
				}
				r, e := agent.NewRunner(deps)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			r := makeRunner(fault, true)
			started, e := r.StartRun(ctx, agent.StartRequest{ID: "hosted-run", RequestID: "req", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "user"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"local.read"}})
			if e != nil || started.Run.Status != kernel.RunStatusRunning || physical != 1 || len(m.requests) != 1 {
				t.Fatal("original held call not reached", e, started.Run.Status, physical)
			}
			g, e := base.LookupChildExecution(ctx, started.Run.ID)
			if e != nil {
				t.Fatal(e)
			}
			c, e := base.LookupPhysicalCallbackReturn(ctx, g)
			if e != nil {
				t.Fatal(e)
			}
			b, found, e := c.ToolReturns.Find("held-call")
			if e != nil || !found || b.ReceiptDigest != "" || b.PendingKind != kind || !c.HostCompleted {
				t.Fatal("missing original Call-bound held return", c, e)
			}
			if _, e = r.ReconcileToolOutcome(ctx, started.Run.ID, started.Run.Revision, "foreign-call"); !errors.Is(e, agent.ErrToolOutcomeUnknown) || lookups != 0 {
				t.Fatal("wrong call reached verifier", e, lookups)
			}
			_, e = r.ReconcileToolOutcome(ctx, started.Run.ID, started.Run.Revision, "held-call")
			if !errors.Is(e, fault.cut) || !fault.fired || lookups != 1 {
				t.Fatal("injected recovery cut not reached", e, lookups)
			}
			saved, e := ledger.LoadBudget(ctx, "hosted-budget-turn")
			if e != nil {
				t.Fatal(e)
			}
			held := saved.Reservations["tool:hosted-run:held-call"]
			if held.Status != budget.ReservationSettled || saved.View("").Usage.ToolCalls != 1 || saved.View("").Reserved.ToolCalls != 0 {
				t.Fatal("first receipt not durable", held)
			}
			var envelope struct {
				Evidence json.RawMessage `json:"_originalEvidence"`
			}
			if json.Unmarshal(held.Receipt, &envelope) != nil || len(envelope.Evidence) == 0 {
				t.Fatal("native settlement used legacy bytes", string(held.Receipt))
			}
			if _, e = makeRunner(base, false).ReconcileToolOutcome(ctx, started.Run.ID, started.Run.Revision, "held-call"); !errors.Is(e, agent.ErrToolReceiptMismatch) || lookups != 1 {
				t.Fatal("replacement removed original settlement owner", e, lookups)
			}
			raw = changed
			r = makeRunner(base, true)
			_, e = r.ReconcileToolOutcome(ctx, started.Run.ID, started.Run.Revision, "held-call")
			if !errors.Is(e, agent.ErrToolReceiptMismatch) || lookups != 2 || physical != 1 || len(m.requests) != 1 {
				t.Fatal("changed original receipt accepted", e, lookups, physical)
			}
			raw = original
			staged, e := r.ReconcileToolOutcome(ctx, started.Run.ID, started.Run.Revision, "held-call")
			if e != nil || lookups != 3 {
				t.Fatal("exact replay rejected", e, lookups)
			}
			final, e := r.Resume(ctx, staged.Run.ID, staged.Run.Revision)
			if e != nil || final.Run.Status != kernel.RunStatusCompleted || len(m.requests) != 2 || physical != 1 {
				t.Fatal("unexpected consumption", e, final.Run.Status, len(m.requests), physical)
			}
			seen := false
			for _, msg := range m.requests[1].Messages {
				if msg.Role == model.RoleTool && msg.ToolCallID == "held-call" {
					seen = true
					if model.TextValue(msg.Content) != original {
						t.Fatal("wrong model-facing original bytes", model.TextValue(msg.Content))
					}
				}
			}
			if !seen {
				t.Fatal("missing original Tool receipt")
			}
			after, e := ledger.LoadBudget(ctx, "hosted-budget-turn")
			if e != nil || after.View("").Usage.ToolCalls != 1 || after.View("").Reserved.ToolCalls != 0 || string(after.Reservations["tool:hosted-run:held-call"].Receipt) != string(held.Receipt) {
				t.Fatal("replay rewrote receipt or charged twice", e, after.View(""))
			}
		})
	}
}
