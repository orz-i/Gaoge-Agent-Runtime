package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	runtimeconformance "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"reflect"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
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
	hit       bool
	event     string
	count     int
	before    bool
	allEvents bool
}

func (s *managedObligationStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		hit := false
		if s.allEvents || commitMutation.ManagedSafePoint.Present() {
			for _, e := range commitMutation.Events {
				if e.Type == s.event {
					s.count--
					hit = s.count == 0
				}
			}
		}
		if hit && s.before {
			s.hit = true
			panic(managedObligationCrash{})
		}
		out, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && hit {
			s.hit = true
			// Deliberately no original callback return/retirement.
			panic(managedObligationCrash{})
		}
		return out, false, err
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
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
			if err != nil || !safe.Valid() || safe.SafePoint.LifecycleVersion != kernel.ManagedLifecycleAgentObligations || safe.SafePoint.ModelInvocationID != frozen.ID {
				t.Fatalf("missing combined safe point: %v %s %s", err, safe.Selection.Mode, safe.SafePoint.LifecycleVersion)
			}
			ledger, err := base.LoadBudget(ctx, pending.Turn.ID)
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
			if mode == "cancel kernel committed" {
				now := time.Now().UTC()
				payload, _ := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID, "protocol": "effects_v1", "canceller": "native"})
				_, fresh, e := host.AppendItem(ctx, harness.Item{ID: "combined-cancel", TurnID: pending.Turn.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now})
				if !errors.Is(e, harness.ErrConflict) || fresh {
					t.Fatalf("independent Host cancel unexpectedly admitted: %v", e)
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
				if err = base.SaveBudget(ctx, altered, ledger.Revision); err != nil {
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
				// Emulate a crash after the atomic native CANCEL but before
				// the Host projection was closed.
				parked, e := options.Runtime.LoadManagedRecovery(ctx, root.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				selected, e := options.Runtime.AuthorizeManagedContinuation(ctx, parked, "stop original task")
				if e != nil {
					t.Fatal(e)
				}
				if selected.Authorization().Mode != kernel.ManagedCancel {
					t.Fatal("wrong cancellation election")
				}
				transferred, e := runtimeconformance.CancelManagedFixture(ctx, options.Runtime, selected)
				if e != nil {
					t.Fatal(e)
				}
				closure, e := options.Runtime.ManagedRecoveryClosure(ctx, transferred.SafePoint.ID)
				l, le := base.LoadBudget(ctx, pending.Turn.ID)
				if e != nil || !closure.Matches(transferred.Selection) || le != nil || !l.RunCancelled(root.ExecutionRefID) || !l.FinishedRuns[root.ExecutionRefID] {
					t.Fatalf("native CANCEL did not commit accounting: %v %v", e, le)
				}
			}
			var result harness.Snapshot
			if mode == "cancel" {
				result, err = session.Cancel(ctx, "managed-obligations", "stop original task")
			} else {
				result, err = session.Resume(ctx, "managed-obligations")
			}
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
			after, err := base.LoadBudget(ctx, pending.Turn.ID)
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

func TestManagedOriginalModelAndInputFrontiers(t *testing.T) {
	var cases []string
	for _, phase := range []string{"claimed", "completed", "input_applied", "rejected", "initial_input", "initial_intent", "initial_cancel"} {
		cases = append(cases, phase+"/before", phase+"/after")
	}
	conformance.ManagedOriginalObligations(t, func(t *testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store), func()) {
		clock := &managedObligationClock{now: time.Now().UTC()}
		k, h := memory.NewStoreWithExecutionClock(clock), harness.NewMemoryStore()
		return k, h, func() (kernel.Store, harness.Store) { return k, h }, func() { clock.now = clock.now.Add(25 * time.Hour) }
	}, cases)
}
