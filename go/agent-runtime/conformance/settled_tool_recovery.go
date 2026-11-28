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

// RunSettledToolRecovery exercises original Tool accounting and successor
// consumption through one native Kernel Store, including independent reopen.
func RunSettledToolRecovery(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	t.Run("mixed-original-batch", func(t *testing.T) { mixedOriginalToolConsumption(t, factory) })
	t.Run("timeline-consumption", func(t *testing.T) { toolTimelineConsumption(t, factory) })
	t.Run("approval-recovery", func(t *testing.T) { originalToolApprovalRecovery(t, factory) })
	t.Run("batch-boundary", func(t *testing.T) { toolBatchBoundary(t, factory) })
	t.Run("capacity-admission", func(t *testing.T) { toolCapacityAdmission(t, factory) })
	t.Run("unsent-recovery", func(t *testing.T) { toolUnsentRecovery(t, factory) })
	t.Run("native-admission-return", func(t *testing.T) { nativeAdmissionReturns(t, factory) })
	t.Run("actual-dispatch-denial", func(t *testing.T) { toolDispatchDenial(t, factory) })
	for _, variant := range []string{"committed", "strict-rejection", "allow-completion", "foreign-ledger", "changed-receipt", "lost-recovery-ack", "cancel-before-consume", "cancel-during-project"} {
		t.Run(variant, func(t *testing.T) {
			ctx := t.Context()
			backing, peer, reopen := factory(t)
			ledger := newToolTestLedger(t, backing)
			event := "tool.correction_requested"
			committed := variant == "committed" || variant == "cancel-before-consume" || variant == "cancel-during-project"
			if committed {
				event = "tool.completed"
			}
			injected := errors.New("settled Tool result CAS cut")
			fault := &ea1AgentReceiptFault{Store: backing, LedgerStore: ledger, ExecutionStore: backing.(kernel.ExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: backing.(kernel.PhysicalExecutionStore), event: event, injected: injected}
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
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, scope string, verify bool) *agent.Runner {
				a, e := agent.NewBoundedBudgetAdapter(l, settledToolPorts{id: scope, project: func(ctx context.Context, l budget.Ledger) error {
					if project != nil {
						return project(ctx, l)
					}
					return nil
				}})
				if e != nil {
					t.Fatal(e)
				}
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry, BoundedBudget: a}
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
			r := makeRunner(rt, fault, "original-ledger", false)
			_, err = r.StartRun(ctx, request)
			if !errors.Is(err, injected) || fault.failures != 1 || originalCalls != 1 || len(m.calls) != 1 {
				t.Fatalf("fixture did not reach settlement cut: %v, faults=%d tools=%d models=%d", err, fault.failures, originalCalls, len(m.calls))
			}
			before, err := observer.Load(ctx, request.ID)
			if err != nil {
				t.Fatal(err)
			}
			// The native receipt CAS already committed BOTH accounting and
			// RecoveredTools. There is no split-only ledger-to-Run recovery
			// transaction, nor an unresolved physical grant to reconstruct.
			if _, pending, e := agent.FrozenPendingDispatch(before); e != nil || pending || before.Run.BudgetScopeID != "original-ledger" {
				t.Fatal("native receipt left split dispatch authority", e)
			}
			if _, e := observer.LookupChildExecution(ctx, request.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("staged native receipt retained physical grant", e)
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
			freshLedger := newToolTestLedger(t, freshStore)
			freshRT := ea1StartRuntime(t, freshStore)
			if variant == "foreign-ledger" {
				// Scope selection is creation-only: a replacement Host cannot
				// move this Run to a same-shaped foreign ledger on Resume.
				r = makeRunner(freshRT, freshLedger, "foreign-ledger", true)
			} else {
				r = makeRunner(freshRT, freshLedger, "original-ledger", true)
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
			if variant == "changed-receipt" {
				if _, err = r.Resume(ctx, request.ID, before.Run.Revision); !errors.Is(err, agent.ErrToolReceiptMismatch) {
					t.Fatalf("changed native receipt advanced: %v", err)
				}
				current, e := freshRT.Load(ctx, request.ID)
				if e != nil || !reflect.DeepEqual(current, before) {
					t.Fatal("rejected recovery changed Run", e)
				}
				if _, e := freshRT.LookupChildExecution(ctx, request.ID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatal("rejected consumption retained physical grant", e)
				}
				if originalCalls != 1 || len(m.calls) != 1 || verifications != 0 {
					t.Fatal("negative recovery performed effects")
				}
				return
			}
			staged := before
			if variant == "lost-recovery-ack" {
				// The old split recovery CAS cannot exist. Exercise ACK loss
				// at native receipt consumption instead; receipt-CAS ACK loss
				// itself is covered by runNativeToolBudget/receipt-ack.
				cut := &ea1AgentReceiptFault{Store: freshStore, LedgerStore: freshLedger, ExecutionStore: freshStore.(kernel.ExecutionStore), ExecutionQuarantineStore: freshStore.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: freshStore.(kernel.PhysicalExecutionStore), event: event, after: true, injected: injected}
				r = makeRunner(ea1StartRuntime(t, cut), cut, "original-ledger", true)
				if _, err = r.Resume(ctx, request.ID, before.Run.Revision); !errors.Is(err, injected) || cut.failures != 1 {
					t.Fatal("missing native consumption ACK loss", err)
				}
				staged, err = freshRT.Load(ctx, request.ID)
				if err != nil || staged.Run.Revision != before.Run.Revision+1 || originalCalls != 1 || len(m.calls) != 1 || verifications != 0 {
					t.Fatal("consumption ACK replayed original effects", err)
				}
				r = makeRunner(freshRT, freshLedger, "original-ledger", false)
			}
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
			if err := freshRT.VerifyExecutionCustody(ctx, final, kernel.CustodyRetired); err != nil {
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
			if verifications != 0 || final.Run.BudgetScopeID != "original-ledger" {
				t.Fatal("native consumption consulted provider or changed scope")
			}
			if variant == "foreign-ledger" {
				if _, e := freshLedger.LoadBudget(ctx, "foreign-ledger"); !errors.Is(e, budget.ErrLedgerNotFound) {
					t.Fatal("replacement Host enrolled foreign scope", e)
				}
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
