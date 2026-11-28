package conformance

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
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type obligationCut struct {
	preparationCut
	initialInputs *agent.MemoryRunInputs
}

func (s *obligationCut) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	out, err := s.preparationKernelPorts.Create(ctx, r, events)
	if err == nil && s.initialInputs != nil && out.Run.Kind == agent.RunKind {
		_, err = s.initialInputs.Submit(ctx, out.Run, "initial-steering", "initial steering")
	}
	return out, err
}

type obligationModel struct {
	requests []model.Request
	generate func(model.Request) (model.Response, error)
}

func (m *obligationModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	if m.generate != nil {
		return m.generate(r)
	}
	return model.Response{Content: "answer"}, nil
}

// ManagedOriginalObligations shares the fault boundaries and assertions;
// durable backends reopen their actual stores, Memory advances its test clock.
func ManagedOriginalObligations(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store), func()), cases []string) {
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			cancel := strings.Contains(name, "cancel")
			phase, _, frontier := strings.Cut(name, "/")
			initial := strings.HasPrefix(phase, "initial_")
			event := "agent.model_invocation.pending"
			if frontier {
				switch phase {
				case "input_applied", "initial_input", "initial_cancel":
					event = "agent.input_applied"
				case "initial_intent":
					event = "agent.model_invocation.pending"
				case "rejected":
					event = "agent.model_invocation.completed"
				default:
					event = "agent.model_invocation." + phase
				}
			}
			wantBefore := 0
			if phase == "completed" || phase == "input_applied" || phase == "rejected" {
				wantBefore = 1
			}
			wantCalls := 2
			if frontier && phase != "input_applied" {
				wantCalls = 1
			}
			ctx := t.Context()
			base, host, reopen, advance := factory(t)
			ks := base.(preparationKernelPorts)
			h := host.(preparationHostPorts)
			inputs, e := agent.NewMemoryRunInputs("original-obligations")
			if e != nil {
				t.Fatal(e)
			}
			provider := &obligationModel{}
			after := strings.HasSuffix(name, "/after") || !frontier
			cut := &obligationCut{preparationCut: preparationCut{preparationKernelPorts: ks, event: event, remaining: 1, after: after}}
			if initial {
				cut.initialInputs = inputs
			}
			rt, e := kernel.New(kernel.Dependencies{Store: cut, ManagedRecoveryHost: h})
			if e != nil {
				t.Fatal(e)
			}
			provider.generate = func(r model.Request) (model.Response, error) {
				if phase == "input_applied" && len(provider.requests) == 1 {
					current, err := ks.Load(ctx, r.RunID)
					if err != nil {
						return model.Response{}, err
					}
					if _, err = inputs.Submit(ctx, current.Run, "late", "late steering"); err != nil {
						return model.Response{}, err
					}
				}
				if phase == "rejected" {
					return model.Response{ResponseID: "rejected-original", Usage: &model.Usage{InputTokens: 20, OutputTokens: 10}}, model.NewResponseRejectedError(model.NewClassifiedError(errors.New("invalid fixture response"), "model.upstream_request_invalid"))
				}
				return model.Response{Content: "answer"}, nil
			}
			execution, leaseDuration := ManagedFixtureExecutionPolicy()
			options := harness.AgentSessionOptions{Runtime: rt, Store: h, Model: provider, Inputs: inputs,
				Session: harness.SessionOptions{HostThread: harness.HostRef{Kind: "conversation", ID: "obligation-frontier"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "obligation-frontier"},
					Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 128}, SharedBudget: &budget.Limits{MaxLLMCalls: 3}},
					Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
						return nil, nil
					})},
				Context: harness.AgentSessionContextOptions{ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
					return runtimecontext.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 128}, nil
				}},
				Execution:    execution,
				RunExecution: &agent.RunExecution{OwnerID: "managed-original", LeaseDuration: leaseDuration}}
			session, e := harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			func() {
				defer func() {
					if _, ok := recover().(preparationInterrupted); !ok {
						t.Fatal("missing safe point cut")
					}
				}()
				_, _ = session.Prompt(ctx, "managed-obligations", "original task")
			}()
			pending, e := session.Load(ctx, "managed-obligations")
			if e != nil {
				t.Fatal(e)
			}
			root, ok := harness.TopLevelInvocation(pending)
			if !ok {
				t.Fatal("missing root")
			}
			original, e := ks.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			safe, e := ks.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if cut.remaining != 0 || e != nil {
				t.Fatal(e)
			}
			proof, ok := safe.SafePoint.AgentObligations()
			if !ok || proof.LedgerID != pending.Turn.ID || len(provider.requests) != wantBefore {
				t.Fatalf("wrong cut: %+v", proof)
			}
			view, e := agent.ViewState(original)
			wantInv := 1
			if initial && (phase != "initial_intent" || !after) {
				wantInv = 0
			}
			if e != nil || len(view.ModelInvocations) != wantInv {
				t.Fatalf("bad original state: %v inv=%d", e, len(view.ModelInvocations))
			}
			var firstID string
			if wantInv != 0 {
				firstID = view.ModelInvocations[0].ID
			}
			if initial {
				wantCursor := uint64(1)
				if phase != "initial_intent" && !after {
					wantCursor = 0
				}
				if view.InputCursor != wantCursor || !kernel.ManagedSourceMatches(safe.SafePoint, original) ||
					(wantInv == 0 && (proof.Phase != "prepare" || proof.InvocationID != "" || proof.PreparationContext == nil)) {
					t.Fatalf("wrong initial frontier: cursor=%d inv=%d proof=%+v", view.InputCursor, len(view.ModelInvocations), proof)
				}
			}
			if !frontier {
				if _, e = inputs.Submit(ctx, original.Run, "late", "late steering"); e != nil {
					t.Fatal(e)
				}
			}
			if name == "cancel closure" {
				now := time.Now().UTC()
				payload, _ := json.Marshal(map[string]string{"boundary": "cancel_scope_pending", "runID": root.ExecutionRefID, "protocol": "effects_v1", "canceller": "native"})
				_, fresh, e := host.AppendItem(ctx, harness.Item{ID: "sqlite-managed-cancel", TurnID: pending.Turn.ID, RunID: root.ExecutionRefID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.CancellationFenceParentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now})
				if !errors.Is(e, harness.ErrConflict) || fresh {
					t.Fatalf("independent Host cancel unexpectedly admitted: %v", e)
				}
			}
			// Probe the live owner before the potentially slow encrypted
			// profile reopen. Never accidentally elect RESUME on the
			// public-CANCEL fixture after the real Store lease has expired.
			if until := safe.SafePoint.OriginalLease.ExpiresAt.Add(-200 * time.Millisecond); advance != nil || time.Until(until) > time.Second {
				probeOptions := options
				probeOptions.Runtime, e = kernel.New(kernel.Dependencies{Store: ks, ManagedRecoveryHost: h})
				if e != nil {
					t.Fatal(e)
				}
				probeOptions.Session.Transcript = nil
				probe, e := harness.OpenAgentSession(probeOptions)
				if e != nil {
					t.Fatal(e)
				}
				probeCtx, cancelProbe := context.WithDeadline(ctx, until)
				_, probeErr := probe.Resume(probeCtx, "managed-obligations")
				cancelProbe()
				unknownReceipt := (phase == "completed" || phase == "rejected") && !after && (errors.Is(probeErr, agent.ErrRootOutcomeUnknown) || errors.Is(probeErr, agent.ErrModelOutcomeUnknown))
				if (!unknownReceipt && !errors.Is(probeErr, kernel.ErrExecutionConflict) && !errors.Is(probeErr, context.DeadlineExceeded)) || len(provider.requests) != wantBefore {
					t.Fatalf("live lease permitted work: %v", probeErr)
				}
				if current, e := ks.Load(ctx, root.ExecutionRefID); e != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(original) {
					t.Fatalf("live probe changed source: %v", e)
				}
			}
			base, host = reopen()
			ks, h = base.(preparationKernelPorts), host.(preparationHostPorts)
			rt, e = kernel.New(kernel.Dependencies{Store: ks, ManagedRecoveryHost: h})
			if e != nil {
				t.Fatal(e)
			}
			if reopened, e := ks.LoadManagedRecovery(ctx, root.ExecutionRefID); e != nil || kernel.ManagedDigest(reopened) != kernel.ManagedDigest(safe) {
				t.Fatalf("safe point changed after reopen: %v", e)
			}
			options.Runtime, options.Store, options.Session.Transcript = rt, h, nil
			options.RunExecution = &agent.RunExecution{OwnerID: "managed-successor", LeaseDuration: leaseDuration}
			session, e = harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			if advance != nil {
				advance()
				lease := safe.SafePoint.OriginalLease
				if e = ks.CheckExecution(ctx, lease.RunID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("simulated Store still authorizes original lease: %v", e)
				}
			} else {
				AwaitManagedOriginalLeaseExpiry(t, ks, original, safe, 2*leaseDuration+5*time.Second)
			}
			if name == "cancel closure" {
				parked, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				selected, e := rt.AuthorizeManagedContinuation(ctx, parked, "stop original task")
				if e != nil {
					t.Fatal(e)
				}
				closed, e := rt.CommitManagedCancellation(ctx, selected)
				if e != nil {
					t.Fatal(e)
				}
				receipt, e := rt.ManagedRecoveryClosure(ctx, closed.SafePoint.ID)
				if e != nil {
					t.Fatal(e)
				}
				l, le := ks.LoadBudget(ctx, pending.Turn.ID)
				if !receipt.Matches(closed.Selection) || le != nil || !l.RunCancelled(root.ExecutionRefID) || !l.FinishedRuns[root.ExecutionRefID] {
					t.Fatalf("native CANCEL did not commit accounting: %v", le)
				}
			}
			var result harness.Snapshot
			if name == "public cancel" || phase == "initial_cancel" {
				result, e = session.Cancel(ctx, "managed-obligations", "stop original task")
			} else {
				result, e = session.Resume(ctx, "managed-obligations")
			}
			if !after && (phase == "completed" || phase == "rejected") {
				current, ce := ks.Load(ctx, root.ExecutionRefID)
				l, le := ks.LoadBudget(ctx, pending.Turn.ID)
				res := l.Reservations["model:"+firstID]
				if e == nil || ce != nil || le != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(original) ||
					(advance != nil && !reflect.DeepEqual(current, original)) || len(provider.requests) != 1 || l.FinishedRuns[root.ExecutionRefID] || !res.Dispatched || res.Status != budget.ReservationHeld {
					t.Fatalf("unknown receipt escaped original fence: %v %v %v", e, ce, le)
				}
				return
			}
			if e != nil && phase != "rejected" {
				t.Fatal(e)
			}
			ledger, e := ks.LoadBudget(ctx, pending.Turn.ID)
			if e != nil {
				t.Fatal(e)
			}
			final, e := ks.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			v, e := agent.ViewState(final)
			if e != nil {
				t.Fatal(e)
			}
			if !ledger.FinishedRuns[root.ExecutionRefID] || ledger.View("").Reserved != (budget.Usage{}) {
				t.Fatalf("budget lifetime not closed: %+v", ledger.View(""))
			}
			if cancel {
				if result.Turn.Status != harness.TurnCancelled || v.InputCursor != view.InputCursor || len(provider.requests) != 0 || len(v.ModelInvocations) != wantInv || (firstID != "" && v.ModelInvocations[0].ID != firstID) || !ledger.RunCancelled(root.ExecutionRefID) || ledger.View("").Usage.LLMCalls != 0 {
					t.Fatalf("cancel entered effects or lost obligations: %+v", ledger.View(""))
				}
			} else {
				wantCursor := uint64(0)
				if wantCalls == 2 || initial {
					wantCursor = 1
				}
				wantStatus := harness.TurnCompleted
				if phase == "rejected" {
					wantStatus = harness.TurnFailed
					if len(v.ModelInvocations) == 0 || v.ModelInvocations[0].ErrorCode != "model.upstream_request_invalid" || ledger.View("").Usage.TotalTokens != 30 {
						t.Fatal("original rejection lost", v.ModelInvocations, ledger.View(""))
					}
				}
				if result.Turn.Status != wantStatus || v.InputCursor != wantCursor || len(provider.requests) != wantCalls ||
					len(v.ModelInvocations) != wantCalls || (firstID != "" && v.ModelInvocations[0].ID != firstID) || ledger.View("").Usage.LLMCalls != wantCalls {
					t.Fatalf("resume accounting mismatch: %+v", ledger.View(""))
				}
				if initial {
					first, _ := json.Marshal(provider.requests[0].Messages)
					if strings.Count(string(first), "initial steering") != 1 {
						t.Fatal("initial input lost or duplicated")
					}
				}
				if wantCalls == 2 {
					first, _ := json.Marshal(provider.requests[0].Messages)
					second, _ := json.Marshal(provider.requests[1].Messages)
					if strings.Contains(string(first), "late steering") || strings.Count(string(second), "late steering") != 1 {
						t.Fatal("late input changed frozen invocation")
					}
				}
			}
			if _, e = rt.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("grant not retired: %v", e)
			}
		})
	}
}
