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
	hit    bool
	event  string
	count  int
	before bool
}

func (s *managedObligationStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	hit := false
	if m.ManagedSafePoint.Present() {
		for _, e := range m.Events {
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
	out, err := s.Store.ApplyExecution(ctx, lease, revision, m)
	if err == nil && hit {
		s.hit = true
		// Deliberately no original callback return/retirement.
		panic(managedObligationCrash{})
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
				transferred, e := options.Runtime.CommitManagedCancellation(ctx, selected)
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

// Interrupt the actual first owner at a native boundary, without a callback
// return. The successor must qualify only the persisted original obligations.
type managedFrontierCutStore struct {
	*memory.Store
	event         string
	after, hit    bool
	initialInputs *agent.MemoryRunInputs
}

func (s *managedFrontierCutStore) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	out, err := s.Store.Create(ctx, r, events)
	if err == nil && s.initialInputs != nil && out.Run.Kind == agent.RunKind {
		_, err = s.initialInputs.Submit(ctx, out.Run, "initial-steering", "frontier steering")
	}
	return out, err
}

func (s *managedFrontierCutStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, e := range m.Events {
		if !s.hit && e.Type == s.event {
			s.hit = true
			if s.after {
				out, err := s.Store.ApplyExecution(ctx, lease, revision, m)
				if err != nil {
					return out, err
				}
			}
			panic(managedObligationCrash{})
		}
	}
	return s.Store.ApplyExecution(ctx, lease, revision, m)
}
func TestManagedOriginalModelAndInputFrontiers(t *testing.T) {
	for _, phase := range []string{"claimed", "completed", "input_applied", "rejected", "initial_input", "initial_intent", "initial_cancel"} {
		for _, after := range []bool{false, true} {
			name := phase + "/before"
			if after {
				name = phase + "/after"
			}
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				clock := &managedObligationClock{now: time.Now().UTC()}
				base := memory.NewStoreWithExecutionClock(clock)
				event := "agent.model_invocation." + phase
				initial := strings.HasPrefix(phase, "initial_")
				switch phase {
				case "input_applied", "initial_input", "initial_cancel":
					event = "agent.input_applied"
				case "initial_intent":
					event = "agent.model_invocation.pending"
				case "rejected":
					event = "agent.model_invocation.completed"
				}
				cut := &managedFrontierCutStore{Store: base, event: event, after: after}
				host := harness.NewMemoryStore()
				q, err := agent.NewMemoryRunInputs("frontier")
				if err != nil {
					t.Fatal(err)
				}
				if initial {
					cut.initialInputs = q
				}
				client := &agentSessionModel{}
				opts := agentSessionOptions(t, cut, host, client)
				opts.Runtime, err = kernel.New(kernel.Dependencies{Store: cut, ManagedRecoveryHost: host})
				if err != nil {
					t.Fatal(err)
				}
				opts.Context.ManagedRecovery = true
				opts.Inputs = q
				opts.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 3}
				client.generate = func(req model.Request) (model.Response, error) {
					if phase == "input_applied" && len(client.requests) == 1 {
						s, e := base.Load(ctx, req.RunID)
						if e != nil {
							return model.Response{}, e
						}
						if _, e = q.Submit(ctx, s.Run, "steer-once", "frontier steering"); e != nil {
							return model.Response{}, e
						}
					}
					if phase == "rejected" {
						return model.Response{ResponseID: "rejected-original", Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}, model.NewResponseRejectedError(model.NewClassifiedError(errors.New("invalid fixture response"), "model.upstream_request_invalid"))
					}
					return model.Response{Content: "complete"}, nil
				}
				session, err := harness.OpenAgentSession(opts)
				if err != nil {
					t.Fatal(err)
				}
				crashManagedObligation(t, func() { _, _ = session.Prompt(ctx, "frontier", "original task") })
				if !cut.hit {
					t.Fatal("cut not reached")
				}
				pending, err := session.Load(ctx, "frontier")
				if err != nil {
					t.Fatal(err)
				}
				root, ok := harness.TopLevelInvocation(pending)
				if !ok {
					t.Fatal("missing root")
				}
				original, err := base.Load(ctx, root.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := agent.ViewState(original)
				wantInv := 1
				if initial && (phase != "initial_intent" || !after) {
					wantInv = 0
				}
				if err != nil || len(view.ModelInvocations) != wantInv {
					t.Fatalf("bad original state: %v inv=%d", err, len(view.ModelInvocations))
				}
				var firstID string
				if wantInv > 0 {
					firstID = view.ModelInvocations[0].ID
				}
				if initial {
					wantCursor := uint64(1)
					if phase != "initial_intent" && !after {
						wantCursor = 0
					}
					if view.InputCursor != wantCursor {
						t.Fatalf("wrong committed initial cursor: %d", view.InputCursor)
					}
					point, e := opts.Runtime.LoadManagedRecovery(ctx, root.ExecutionRefID)
					p, ok := point.SafePoint.AgentObligations()
					if e != nil || !ok || !kernel.ManagedSourceMatches(point.SafePoint, original) ||
						(wantInv == 0 && (p.Phase != "prepare" || p.InvocationID != "" || p.PreparationContext == nil)) {
						t.Fatalf("no exact original preparation point: %v %+v", e, p)
					}
				}
				wantCalls := 1
				if phase == "input_applied" {
					wantCalls = 2
				}
				if (phase == "claimed" || initial) && len(client.requests) != 0 {
					t.Fatal("claim dispatched")
				}
				if phase != "claimed" && !initial && len(client.requests) != 1 {
					t.Fatal("receipt cut did not dispatch exactly once")
				}
				opts.Session.Transcript = nil
				opts.Runtime, err = kernel.New(kernel.Dependencies{Store: base, ManagedRecoveryHost: host})
				if err != nil {
					t.Fatal(err)
				}
				session, err = harness.OpenAgentSession(opts)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = session.Resume(ctx, "frontier"); err == nil {
					t.Fatal("live lease enabled recovery")
				}
				clock.now = clock.now.Add(25 * time.Hour)
				if phase == "initial_cancel" {
					done, e := session.Cancel(ctx, "frontier", "cancel before first model")
					final, readErr := base.Load(ctx, root.ExecutionRefID)
					end, viewErr := agent.ViewState(final)
					ledger, ledgerErr := base.LoadBudget(ctx, pending.Turn.ID)
					if e != nil || readErr != nil || viewErr != nil || ledgerErr != nil ||
						done.Turn.Status != harness.TurnCancelled || len(client.requests) != 0 || len(end.ModelInvocations) != 0 ||
						end.InputCursor != view.InputCursor || ledger.View("").Usage.LLMCalls != 0 ||
						!ledger.RunCancelled(root.ExecutionRefID) || !ledger.FinishedRuns[root.ExecutionRefID] {
						t.Fatalf("preparation CANCEL lost original obligations: %v status=%s cursor=%d", e, done.Turn.Status, end.InputCursor)
					}
					if _, e = opts.Runtime.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
						t.Fatalf("grant not retired: %v", e)
					}
					return
				}
				done, err := session.Resume(ctx, "frontier")
				if !after && (phase == "completed" || phase == "rejected") {
					// Atomic receipt CAS did not commit. The provider might have
					// charged, but no original receipt exists in either state.
					current, e := base.Load(ctx, root.ExecutionRefID)
					l, le := base.LoadBudget(ctx, pending.Turn.ID)
					res := l.Reservations["model:"+firstID]
					if err == nil || e != nil || le != nil || !reflect.DeepEqual(current, original) || len(client.requests) != 1 || !res.Dispatched || res.Status != budget.ReservationHeld || l.FinishedRuns[root.ExecutionRefID] {
						t.Fatalf("unknown physical result not fenced: err=%v load=%v ledger=%v reservation=%+v", err, e, le, res)
					}
					return
				}
				if err != nil && phase != "rejected" {
					t.Fatalf("resume: %v status=%s", err, done.Turn.Status)
				}
				final, err := base.Load(ctx, root.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				end, err := agent.ViewState(final)
				if err != nil {
					t.Fatal(err)
				}
				ledger, err := base.LoadBudget(ctx, pending.Turn.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := harness.TurnCompleted
				if phase == "rejected" {
					wantStatus = harness.TurnFailed
					if end.ModelInvocations[0].ErrorCode != "model.upstream_request_invalid" || ledger.View("").Usage.TotalTokens != 30 {
						t.Fatalf("original rejection lost: %+v %+v", end.ModelInvocations[0], ledger.View(""))
					}
				}
				if done.Turn.Status != wantStatus || len(client.requests) != wantCalls || len(end.ModelInvocations) != wantCalls ||
					(firstID != "" && end.ModelInvocations[0].ID != firstID) || ledger.View("").Usage.LLMCalls != wantCalls || !ledger.FinishedRuns[root.ExecutionRefID] {
					t.Fatalf("original frontier lost: status=%s calls=%d inv=%d cursor=%d ledger=%+v err=%v", done.Turn.Status, len(client.requests), len(end.ModelInvocations), end.InputCursor, ledger.View(""), err)
				}
				if phase == "input_applied" || initial {
					if end.InputCursor != 1 || strings.Count(requestTextManaged(client.requests[wantCalls-1]), "frontier steering") != 1 {
						t.Fatal("input obligation lost or duplicated")
					}
				}
				if _, err = opts.Runtime.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("grant not retired: %v", err)
				}
			})
		}
	}
}
