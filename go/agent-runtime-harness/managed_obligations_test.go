package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type managedObligationClock struct{ now time.Time }

func (c *managedObligationClock) Now() time.Time { return c.now }

type managedObligationCrash struct{}
type managedObligationStore struct {
	*memory.Store
	hit   bool
	event string
	count int
}

func (s *managedObligationStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	out, err := s.Store.ApplyExecution(ctx, lease, revision, m)
	if err == nil && m.ManagedSafePoint.Present() {
		for _, e := range m.Events {
			if e.Type == s.event {
				s.count--
				if s.count == 0 {
					s.hit = true
					// Deliberately no original callback return/retirement. The stack
					// aborts and the test reconstructs a separate Runner and Runtime.
					panic(managedObligationCrash{})
				}
			}
		}
	}
	return out, err
}
func crashManagedObligation(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if _, ok := recover().(managedObligationCrash); !ok {
			t.Fatal("fixture did not interrupt the native transfer")
		}
	}()
	f()
}

type managedCountingInputs struct {
	*agent.MemoryRunInputs
	reads int
}

func (s *managedCountingInputs) ReadRunInputs(ctx context.Context, r kernel.Run, cursor uint64, limit int) ([]agent.RunInput, error) {
	s.reads++
	return s.MemoryRunInputs.ReadRunInputs(ctx, r, cursor, limit)
}

func TestManagedContextCarriesOriginalBudgetAndInputCursor(t *testing.T) {
	for _, mode := range []string{"resume", "cancel", "cancel kernel committed", "changed ledger", "changed input"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			clock := &managedObligationClock{now: time.Now().UTC()}
			base := memory.NewStoreWithExecutionClock(clock)
			runs := &managedObligationStore{Store: base, event: "agent.model_invocation.pending", count: 2}
			host := harness.NewMemoryStore()
			q, err := agent.NewMemoryRunInputs("original-queue")
			if err != nil {
				t.Fatal(err)
			}
			inputs := &managedCountingInputs{MemoryRunInputs: q}
			client := &agentSessionModel{}
			options := agentSessionOptions(t, runs, host, client)
			options.Runtime, err = kernel.New(kernel.Dependencies{Store: runs, ManagedRecoveryHost: host})
			if err != nil {
				t.Fatal(err)
			}
			options.Context.ManagedRecovery = true
			options.Inputs = inputs
			options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 1}
			writes := 0
			addAgentSessionTool(&options, "lookup", &writes)
			client.generate = func(req model.Request) (model.Response, error) {
				if len(client.requests) == 1 {
					cur, e := options.Runtime.Load(ctx, req.RunID)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = q.Submit(ctx, cur.Run, "first", "first steering"); e != nil {
						t.Fatal(e)
					}
					return model.Response{ToolCalls: []tools.Call{{ID: "lookup-once", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
				}
				return model.Response{Content: "complete"}, nil
			}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			crashManagedObligation(t, func() { _, _ = session.Prompt(ctx, "managed-obligations", "original task") })
			if !runs.hit || writes != 1 || len(client.requests) != 1 {
				t.Fatalf("wrong cut: %t writes=%d models=%d", runs.hit, writes, len(client.requests))
			}
			pending, err := session.Load(ctx, "managed-obligations")
			if err != nil {
				t.Fatal(err)
			}
			root, ok := harness.TopLevelInvocation(pending)
			if !ok {
				t.Fatal("missing original root")
			}
			original, err := base.Load(ctx, root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(original)
			if err != nil || view.InputCursor != 1 || len(view.ModelInvocations) != 2 {
				t.Fatalf("wrong original input/intent: %v cursor=%d invocations=%d", err, view.InputCursor, len(view.ModelInvocations))
			}
			frozen := view.ModelInvocations[1]
			safe, err := options.Runtime.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if err != nil || safe.State != kernel.ManagedAvailable || safe.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations || safe.SafePoint.ModelInvocationID != frozen.ID {
				t.Fatalf("missing combined safe point: %v %s %s", err, safe.State, safe.SafePoint.LifecycleVersion)
			}
			ledger, err := host.LoadBudget(ctx, pending.Turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if l := ledger.View(""); l.Usage.LLMCalls != 1 || l.Usage.ToolCalls != 1 || l.Reserved != (budget.Usage{}) {
				t.Fatalf("wrong original ledger: %+v", l)
			}
			// Queueing after the intent cannot change it or its cursor.
			if _, err = q.Submit(ctx, original.Run, "late", "late steering"); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "cancel") {
				now := time.Now().UTC()
				payload, _ := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID, "protocol": "effects_v1", "canceller": "native"})
				_, fresh, e := host.AppendItem(ctx, harness.Item{ID: "combined-cancel", TurnID: pending.Turn.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now})
				if e != nil || !fresh {
					t.Fatalf("cancel contender: %v", e)
				}
			}
			options.Session.Transcript = nil
			options.Runtime, err = kernel.New(kernel.Dependencies{Store: base, ManagedRecoveryHost: host})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed input" {
				options.Inputs, _ = agent.NewMemoryRunInputs("foreign-queue")
			}
			if mode == "changed ledger" {
				// The original frozen charge may not be silently replaced even if
				// the same ledger ID and native Run are still present.
				altered := budget.CloneLedger(ledger)
				altered.Revision++
				altered.Limits.MaxLLMCalls++
				if err = host.SaveBudget(ctx, altered, ledger.Revision); err != nil {
					t.Fatal(err)
				}
			}
			session, err = harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			reads := inputs.reads
			if _, err = session.Resume(ctx, "managed-obligations"); err == nil || len(client.requests) != 1 || inputs.reads != reads {
				t.Fatalf("live original lease allowed callback: %v", err)
			}
			clock.now = clock.now.Add(25 * time.Hour)
			if mode == "cancel kernel committed" {
				// Emulate a crash after the kernel CANCEL transition but before
				// the original budget fence/lifetime receipt was closed.
				parked, e := options.Runtime.ParkManagedContinuation(ctx, root.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				selected, e := options.Runtime.SelectManagedContinuation(ctx, parked)
				if e != nil {
					t.Fatal(e)
				}
				if selected.Reservation().Mode != kernel.ManagedCancel {
					t.Fatal("wrong cancellation election")
				}
				transferred, e := options.Runtime.CancelParkedManagedContinuation(ctx, selected)
				if e != nil {
					t.Fatal(e)
				}
				closure, e := options.Runtime.ManagedRecoveryClosure(ctx, transferred.SafePoint.ID)
				if e != nil {
					t.Fatal(e)
				}
				if e = host.CloseManagedRecovery(ctx, closure); e == nil {
					t.Fatal("closed Host before original ledger")
				}
			}
			result, err := session.Resume(ctx, "managed-obligations")
			if strings.HasPrefix(mode, "changed") {
				if err == nil || len(client.requests) != 1 || inputs.reads != reads {
					t.Fatalf("changed source enabled recovery: %v", err)
				}
				current, e := base.Load(ctx, root.ExecutionRefID)
				if e != nil || !reflect.DeepEqual(current, original) {
					t.Fatal("changed source mutated original Run")
				}
				return
			}
			if err != nil {
				t.Fatalf("recover %s: %v status=%s", mode, err, result.Turn.Status)
			}
			final, err := base.Load(ctx, root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			end, err := agent.ViewState(final)
			if err != nil {
				t.Fatal(err)
			}
			after, err := host.LoadBudget(ctx, pending.Turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "cancel") {
				if result.Turn.Status != harness.TurnCancelled || end.InputCursor != 1 || inputs.reads != reads || len(client.requests) != 1 || writes != 1 ||
					!after.RunCancelled(root.ExecutionRefID) || !after.FinishedRuns[root.ExecutionRefID] || after.View("").Usage != ledger.View("").Usage {
					t.Fatalf("cancel lost original obligations: status=%s cursor=%d reads=%d models=%d writes=%d ledger=%+v", result.Turn.Status, end.InputCursor, inputs.reads, len(client.requests), writes, after.View(""))
				}
			} else {
				if result.Turn.Status != harness.TurnCompleted || end.InputCursor != 2 || len(client.requests) != 3 || writes != 1 || !after.FinishedRuns[root.ExecutionRefID] ||
					after.View("").Usage.LLMCalls != 3 || after.View("").Usage.ToolCalls != 1 {
					t.Fatalf("resume lost original obligations: status=%s cursor=%d models=%d writes=%d ledger=%+v", result.Turn.Status, end.InputCursor, len(client.requests), writes, after.View(""))
				}
				hash, err := model.PhysicalRequestHash(client.requests[1])
				if err != nil || frozen.SamplingPlan == nil || hash != frozen.SamplingPlan.RequestHash || client.requests[1].InvocationID != frozen.ID {
					t.Fatal("recovery changed the original physical sampling plan")
				}
				if strings.Contains(requestTextManaged(client.requests[1]), "late steering") || !strings.Contains(requestTextManaged(client.requests[2]), "late steering") {
					t.Fatal("late input rewrote frozen request or disappeared")
				}
			}
			if _, e := options.Runtime.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("original transfer grant not retired: %v", e)
			}
		})
	}
}
func requestTextManaged(r model.Request) string {
	var out string
	for _, m := range r.Messages {
		out += model.TextValue(m.Content) + "\n"
	}
	return out
}
