package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type settledToolPorts struct {
	nativeModelBudgetPorts
	id      string
	project func(context.Context, budget.Ledger) error
}

func (p settledToolPorts) ResolveScope(_ context.Context, runID string) (agent.BoundedBudgetScope, error) {
	return agent.BoundedBudgetScope{LedgerID: p.id, Limits: budget.Limits{MaxToolCalls: 3}, Runs: []agent.BoundedBudgetRunBinding{{RunID: runID}}}, nil
}
func (p settledToolPorts) Project(ctx context.Context, l budget.Ledger) error {
	if p.project != nil {
		return p.project(ctx, l)
	}
	return nil
}
func (settledToolPorts) ToolReservationActive(plugin.ToolInvocation) bool { return true }

type settledToolModel struct {
	calls  []model.Request
	strict bool
}

func (m *settledToolModel) Generate(_ context.Context, q model.Request) (model.Response, error) {
	m.calls = append(m.calls, model.CloneRequest(q))
	switch len(m.calls) {
	case 1:
		return model.Response{ToolCalls: []tools.Call{{ID: "original-call", ToolKey: "read", Arguments: json.RawMessage(`{"query":"original"}`)}}}, nil
	case 2:
		if m.strict {
			return model.Response{ToolCalls: []tools.Call{{ID: "correction-call", ToolKey: "other", Arguments: json.RawMessage(`{}`)}}}, nil
		}
	}
	return model.Response{Content: "finished"}, nil
}

// RunSettledToolRecovery exercises the complete original Tool ledger -> native
// receipt-only CAS -> successor Agent consumption path with real Kernel adapters.
// The ledger factory can independently reopen the Host's own store.
func RunSettledToolRecovery(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	t.Run("mixed-original-batch", func(t *testing.T) { mixedOriginalToolConsumption(t, factory, ledgerFactory) })
	t.Run("timeline-consumption", func(t *testing.T) { toolTimelineConsumption(t, factory, ledgerFactory) })
	t.Run("approval-recovery", func(t *testing.T) { originalToolApprovalRecovery(t, factory, ledgerFactory) })
	t.Run("batch-boundary", func(t *testing.T) { toolBatchBoundary(t, factory, ledgerFactory) })
	t.Run("capacity-admission", func(t *testing.T) { toolCapacityAdmission(t, factory, ledgerFactory) })
	t.Run("unsent-recovery", func(t *testing.T) { toolUnsentRecovery(t, factory, ledgerFactory) })
	t.Run("native-admission-return", func(t *testing.T) { nativeAdmissionReturns(t, factory, ledgerFactory) })
	t.Run("actual-dispatch-denial", func(t *testing.T) { toolDispatchDenial(t, factory, ledgerFactory) })
	for _, variant := range []string{"committed", "strict-rejection", "allow-completion", "foreign-ledger", "changed-receipt", "opaque-callback", "lost-recovery-ack", "cancel-before-consume", "cancel-during-project"} {
		t.Run(variant, func(t *testing.T) {
			ctx := t.Context()
			backing, peer, reopen := factory(t)
			var ledger budget.LedgerStore
			var reopenLedger func() budget.LedgerStore
			if ledgerFactory == nil {
				store := budget.NewMemoryLedgerStore()
				ledger = store
				reopenLedger = func() budget.LedgerStore { return store }
			} else {
				ledger, reopenLedger = ledgerFactory(t)
			}
			event := "tool.correction_requested"
			committed := variant == "committed" || variant == "cancel-before-consume" || variant == "cancel-during-project"
			if committed {
				event = "tool.completed"
			}
			injected := errors.New("settled Tool result CAS cut")
			fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), event: event, injected: injected}
			rt := ea1StartRuntime(t, fault)
			observer := ea1StartRuntime(t, peer)
			m := &settledToolModel{strict: variant == "strict-rejection"}
			originalCalls, correctedCalls, verifications := 0, 0, 0
			allow := !m.strict
			var original tools.ExecutionRequest
			const rawResult = `{ "html": "<>&", "n": 9007199254740993 }`
			registry, err := tools.NewRegistry([]tools.Registration{
				{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					originalCalls++
					original = tools.CloneExecutionRequest(q)
					if committed {
						return tools.ExecutionResult{Content: json.RawMessage(rawResult), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
					}
					refusal := tools.NewRecoverableCallErrorWithBlockedTools("tool.unavailable", "original refusal", nil, "read")
					var typed *tools.RecoverableCallError
					if !errors.As(refusal, &typed) {
						t.Fatal("missing refusal")
					}
					typed.AllowCompletion = allow
					return tools.RecoverableNoEffectResult(q.Call.ID, refusal)
				})},
				{Definition: tools.Definition{Key: "other", Name: "other", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					correctedCalls++
					return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			var project func(context.Context, budget.Ledger) error
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, scope string, opaque bool, verify bool) *agent.Runner {
				a, e := agent.NewBoundedBudgetAdapter(l, settledToolPorts{id: scope, project: func(ctx context.Context, l budget.Ledger) error {
					if project != nil {
						return project(ctx, l)
					}
					return nil
				}})
				if e != nil {
					t.Fatal(e)
				}
				var layers []plugin.ToolMiddleware
				if opaque {
					layers = append(layers, ea1OpaqueToolMiddleware{})
				}
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry, BoundedBudget: a, ToolMiddleware: layers}
				if verify {
					d.ToolOutcomeVerifier = agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
						verifications++
						return tools.ExecutionResult{}, errors.New("unexpected provider lookup")
					})
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			request := agent.StartRequest{ID: "settled-tool-run", RequestID: "original", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read evidence", Model: "fixture", ToolKeys: []string{"read", "other"}}
			r := makeRunner(rt, ledger, "original-ledger", variant == "opaque-callback", false)
			_, err = r.StartRun(ctx, request)
			if !errors.Is(err, injected) || fault.failures != 1 || originalCalls != 1 || len(m.calls) != 1 {
				t.Fatalf("fixture did not reach settlement cut: %v, faults=%d tools=%d models=%d", err, fault.failures, originalCalls, len(m.calls))
			}
			before, err := observer.Load(ctx, request.ID)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := observer.LookupChildExecution(ctx, request.ID)
			if err != nil {
				t.Fatal(err)
			}
			completion, err := observer.LookupOwnedPhysicalCallbackReturn(ctx, grant)
			if err != nil {
				t.Fatal(err)
			}
			binding, found, bindErr := completion.ToolReturns.Find(original.Call.ID)
			if variant != "opaque-callback" && (!completion.HostCompleted || completion.ControlCustody != kernel.OwnedCustodyScoped || bindErr != nil || !found || binding.ReceiptDigest == "") {
				t.Fatalf("original completion not sealed: %+v", completion)
			}
			saved, err := ledger.LoadBudget(ctx, "original-ledger")
			if err != nil {
				t.Fatal(err)
			}
			id := "tool:" + request.ID + ":" + original.Call.ID
			res := saved.Reservations[id]
			if res.Status != budget.ReservationSettled || saved.View("").Usage.ToolCalls != 1 {
				t.Fatalf("missing original accounting: %+v", res)
			}
			freshStore := reopen()
			freshLedger := reopenLedger()
			freshRT := ea1StartRuntime(t, freshStore)
			if variant == "foreign-ledger" {
				foreign := saved
				foreign.ID = "foreign-ledger"
				foreign.Revision = 1
				if err = freshLedger.SaveBudget(ctx, foreign, 0); err != nil {
					t.Fatal(err)
				}
				r = makeRunner(freshRT, freshLedger, "foreign-ledger", false, true)
			} else {
				r = makeRunner(freshRT, freshLedger, "original-ledger", false, true)
			}
			if variant == "changed-receipt" {
				var raw map[string]json.RawMessage
				if err = json.Unmarshal(res.Receipt, &raw); err != nil {
					t.Fatal(err)
				}
				raw["allowCompletion"] = json.RawMessage("false")
				res.Receipt, err = json.Marshal(raw)
				if err != nil {
					t.Fatal(err)
				}
				saved.Reservations[id] = res
				rev := saved.Revision
				saved.Revision++
				if err = freshLedger.SaveBudget(ctx, saved, rev); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "foreign-ledger" || variant == "changed-receipt" || variant == "opaque-callback" {
				if _, err = r.Resume(ctx, request.ID, before.Run.Revision); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
					t.Fatalf("foreign/opaque receipt advanced: %v", err)
				}
				current, e := freshRT.Load(ctx, request.ID)
				if e != nil || !reflect.DeepEqual(current, before) {
					t.Fatal("rejected recovery changed Run", e)
				}
				still, e := freshRT.LookupChildExecution(ctx, request.ID)
				if e != nil || still != grant {
					t.Fatal("rejected recovery changed grant", e)
				}
				if originalCalls != 1 || len(m.calls) != 1 || verifications != 0 {
					t.Fatal("negative recovery performed effects")
				}
				return
			}
			if variant == "lost-recovery-ack" {
				// A committed recovery CAS may lose its response. Re-open at
				// the Store's actual new revision, never replay the old intent.
				under := &ea1AgentReceiptFault{Store: freshStore, ExecutionStore: freshStore.(kernel.ExecutionStore), ControlStore: freshStore.(kernel.ControlStore), ChildExecutionStore: freshStore.(kernel.ChildExecutionStore), ExecutionQuarantineStore: freshStore.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: freshStore.(kernel.OwnedPhysicalExecutionStore), injected: injected}
				recovery := &ea1RootRecoveryFault{ea1AgentReceiptFault: under, phase: "P3"}
				r = makeRunner(ea1StartRuntime(t, recovery), freshLedger, "original-ledger", false, true)
				if _, err = r.ReconcileToolOutcome(ctx, request.ID, before.Run.Revision, original.Call.ID); !errors.Is(err, injected) {
					t.Fatal("missing recovery ACK loss", err)
				}
				r = makeRunner(freshRT, freshLedger, "original-ledger", false, true)
			} else {
				// Both public paths use the same original callback receipt.
				if committed {
					_, err = r.ReconcileToolOutcome(ctx, request.ID, before.Run.Revision, original.Call.ID)
				} else {
					_, err = r.Resume(ctx, request.ID, before.Run.Revision)
				}
				if err != nil {
					t.Fatal("receipt recovery", err)
				}
			}
			staged, err := freshRT.Load(ctx, request.ID)
			if err != nil || staged.Run.Revision != before.Run.Revision+1 {
				t.Fatal("not exactly one recovery CAS", err)
			}
			if _, pending, e := agent.FrozenPendingDispatch(staged); e != nil || pending {
				t.Fatal("original intent not transferred", e)
			}
			if originalCalls != 1 || len(m.calls) != 1 || verifications != 0 {
				t.Fatal("receipt-only recovery dispatched")
			}
			if _, err = r.ReconcileToolOutcome(ctx, request.ID, before.Run.Revision, original.Call.ID); !errors.Is(err, kernel.ErrConflict) {
				t.Fatal("stale source accepted", err)
			}
			// No Host provider verifier is required to consume the staged result.
			r = makeRunner(freshRT, freshLedger, "original-ledger", false, false)
			if variant == "cancel-before-consume" || variant == "cancel-during-project" {
				elect := func(ctx context.Context) error {
					l, e := (budget.Coordinator{Store: freshLedger}).CancelRoot(ctx, "original-ledger", request.ID)
					if e != nil || !l.RunCancelled(request.ID) {
						t.Fatalf("original cancellation not elected: %v", e)
					}
					return nil
				}
				if variant == "cancel-before-consume" {
					if e := elect(ctx); e != nil {
						t.Fatal(e)
					}
				} else {
					project = func(ctx context.Context, l budget.Ledger) error { project = nil; return elect(ctx) }
				}
				out, e := r.Resume(ctx, request.ID, staged.Run.Revision)
				current, readErr := freshRT.Load(ctx, request.ID)
				after, ledgerErr := freshLedger.LoadBudget(ctx, "original-ledger")
				if !errors.Is(e, budget.ErrExhausted) || readErr != nil || ledgerErr != nil || kernel.ManagedDigest(out) != kernel.ManagedDigest(staged) || kernel.ManagedDigest(current) != kernel.ManagedDigest(staged) ||
					!after.RunCancelled(request.ID) || after.FinishedRuns[request.ID] || !reflect.DeepEqual(after.Reservations[id], saved.Reservations[id]) || originalCalls != 1 || len(m.calls) != 1 || verifications != 0 {
					t.Fatalf("receipt consumption crossed elected cancellation: error=%v read=%v ledger=%v", e, readErr, ledgerErr)
				}
				if _, e := freshRT.LookupChildExecution(ctx, request.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("known denied receipt retained an unknown callback: %v", e)
				}
				return
			}
			final, err := r.Resume(ctx, request.ID, staged.Run.Revision)
			if err != nil || final.Run.Status != kernel.RunStatusCompleted || originalCalls != 1 {
				t.Fatalf("original outcome not consumed: status=%s err=%v calls=%d", final.Run.Status, err, originalCalls)
			}
			if _, e := freshRT.LookupChildExecution(ctx, request.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("original grant not retired", e)
			}
			if err := freshRT.VerifyOwnedRootSettlement(ctx, final); err != nil {
				t.Fatal("terminal original callback history not observable", err)
			}
			if !r.VerifiedDeferredRootControl(ctx, freshRT, final) {
				t.Fatal("qualified native Root control not observable")
			}
			after, err := freshLedger.LoadBudget(ctx, "original-ledger")
			if err != nil || after.Reservations[id].Status != budget.ReservationSettled || !reflect.DeepEqual(after.Reservations[id], saved.Reservations[id]) {
				t.Fatal("original ledger receipt changed", err)
			}
			if correctedCalls != map[bool]int{false: 0, true: 1}[m.strict] || after.View("").Usage.ToolCalls != (1+correctedCalls) {
				t.Fatal("usage/dispatch mismatch", correctedCalls, after.View("").Usage)
			}
			second := m.calls[1]
			toolMessages := 0
			for _, msg := range second.Messages {
				if msg.Role != model.RoleTool || msg.ToolCallID != original.Call.ID {
					continue
				}
				toolMessages++
				if committed {
					if model.TextValue(msg.Content) != rawResult {
						t.Fatalf("opaque original Tool bytes changed: %q", model.TextValue(msg.Content))
					}
				} else {
					var payload struct {
						Retryable bool                           `json:"retryable"`
						Error     struct{ Code, Message string } `json:"error"`
					}
					if json.Unmarshal([]byte(model.TextValue(msg.Content)), &payload) != nil || payload.Retryable != m.strict || payload.Error.Code != "tool.unavailable" || payload.Error.Message != "original refusal" {
						t.Fatal("original refusal semantics lost", model.TextValue(msg.Content))
					}
				}
			}
			if toolMessages != 1 {
				t.Fatal("original Tool transcript not consumed once", toolMessages)
			}
			if !committed && (second.RequireToolCall != m.strict || slices.ContainsFunc(second.Tools, func(d tools.Definition) bool { return d.Key == "read" })) {
				t.Fatal("original completion/blocked-keys policy changed", second.RequireToolCall, second.Tools)
			}
		})
	}
}
