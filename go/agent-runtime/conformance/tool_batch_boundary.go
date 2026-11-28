package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type batchBoundaryModel struct{ requests []model.Request }

func (m *batchBoundaryModel) Generate(_ context.Context, q model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(q))
	if len(m.requests) == 1 {
		return model.Response{ToolCalls: []tools.Call{
			{ID: "child-a", ToolKey: "read", Arguments: json.RawMessage(`{"q":1}`)},
			{ID: "child-b", ToolKey: "read", Arguments: json.RawMessage(`{"q":2}`)},
			{ID: "next-call", ToolKey: "read", Arguments: json.RawMessage(`{"q":3}`)},
		}}, nil
	}
	return model.Response{Content: "done"}, nil
}

type batchBoundaryFault struct {
	*ea1AgentReceiptFault
	after      bool
	persistent bool
	hits       int
}

func (s *batchBoundaryFault) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		if (s.hits == 0 || s.persistent) && bytes.Contains(commitMutation.Record.State, []byte(`"phase":"required"`)) {
			for _, ev := range commitMutation.Events {
				if ev.Type == agent.EventToolPending && ev.Message == "child_batch" {
					s.hits++
					if s.after {
						if _, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth); err != nil {
							return kernel.Snapshot{}, false, err
						}
					}
					return kernel.Snapshot{}, false, s.injected
				}
			}
		}
		return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
	}
	return s.ea1AgentReceiptFault.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

// All sibling effects must drain before publication of a new Approval or a
// local allowance pause. With no boundary, independent children retain their
// original parallel admission. These are real store/reopen paths.
func toolBatchBoundary(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	// There is no speculative reservation/no-send closure CAS in the native
	// batch. Its cut is the final child receipt CAS (before/ACK) below.
	for _, mode := range []string{"approval-approve", "approval-reject", "approval-frontier-before", "approval-frontier-ack", "approval-frontier-persistent", "allowance", "parallel", "unsent-before-chain", "actual-dispatch-denial", "actual-dispatch-denial-final-child-before", "actual-dispatch-denial-final-child-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			backing, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger := newToolTestLedger(t, backing)
			children := map[string]kernel.Snapshot{}
			for _, id := range []string{"child-a", "child-b"} {
				c, e := observer.Create(ctx, kernel.CreateRequest{ID: id, Kind: "fixture.child", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "child", State: json.RawMessage("{}")})
				if e != nil {
					t.Fatal(e)
				}
				children[id] = c
			}
			m := &batchBoundaryModel{}
			physical, policies, reads := map[string]int{}, map[string]int{}, map[string]int{}
			result := func(id string) tools.ExecutionResult {
				return tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{ "original": %q }`, id)), Receipt: tools.Receipt{ExecutionID: id, Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("owner:" + id)}}
			}
			reg, e := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical[q.Call.ID]++
				if c, ok := children[q.Call.ID]; ok {
					return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "pending"}, Pending: &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: q.Call.ID, ChildRunID: c.Run.ID}}, nil
				}
				return result(q.Call.ID), nil
			})}})
			if e != nil {
				t.Fatal(e)
			}
			actualDenial := mode == "actual-dispatch-denial" || mode == "actual-dispatch-denial-final-child-before" || mode == "actual-dispatch-denial-final-child-ack"
			approvalMode := mode != "allowance" && mode != "parallel" && mode != "unsent-before-chain" && !actualDenial
			var cancelBeforeChain *unsentCancelObserver
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, withPolicy bool) *agent.Runner {
				t.Helper()
				b, e := agent.NewBoundedBudgetAdapter(l, settledToolPorts{id: "original-ledger"})
				if e != nil {
					t.Fatal(e)
				}
				p, e := agent.NewBoundedToolApproval(func(_ context.Context, q tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					policies[q.Call.ID]++
					if approvalMode && q.Call.ID == "next-call" {
						return plugin.ApprovalRequired, nil
					}
					return plugin.ApprovalNotRequired, nil
				})
				if e != nil {
					t.Fatal(e)
				}
				interactions, e := interaction.New(rt)
				if e != nil {
					t.Fatal(e)
				}
				reader := agent.ToolOutcomeVerifierFunc(func(ctx context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads[q.Call.ID]++
					if q.RunID != "batch-boundary" || physical[q.Call.ID] != 1 {
						return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
					}
					if _, ok := children[q.Call.ID]; ok {
						c, e := rt.Load(ctx, q.Call.ID)
						if e != nil || c.Run.Status != kernel.RunStatusCompleted {
							return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
						}
					}
					return result(q.Call.ID), nil
				})
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, Approvals: interactionadapter.New(interactions), BoundedBudget: b, ToolOutcomeVerifier: reader, DelegatedToolResults: reader, DeferResumption: true}
				if cancelBeforeChain != nil {
					d.Observers = []plugin.Observer{cancelBeforeChain}
				}
				if withPolicy {
					d.ApprovalPolicies = []plugin.ApprovalPolicy{p}
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			var fault *batchBoundaryFault
			initial := backing
			if mode == "approval-frontier-before" || mode == "approval-frontier-ack" || mode == "approval-frontier-persistent" {
				fault = &batchBoundaryFault{ea1AgentReceiptFault: &ea1AgentReceiptFault{Store: backing, LedgerStore: ledger, ExecutionStore: backing.(kernel.ExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: backing.(kernel.PhysicalExecutionStore), injected: errors.New("batch frontier commit fault")}, after: mode == "approval-frontier-ack", persistent: mode == "approval-frontier-persistent"}
				initial = fault
			}
			var denialFault *toolDispatchDenialStore
			if actualDenial {
				denialFault = &toolDispatchDenialStore{ea1AgentReceiptFault: &ea1AgentReceiptFault{Store: backing, LedgerStore: ledger, ExecutionStore: backing.(kernel.ExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: backing.(kernel.PhysicalExecutionStore), injected: errors.New("batch no-send CAS cut")}, afterCallID: "next-call"}
				denialFault.afterCheck = func() {
					if _, err := (budget.Coordinator{Store: ledger}).CancelRoot(ctx, "original-ledger", "batch-boundary"); err != nil {
						t.Fatal(err)
					}
				}
				initial = denialFault
			}
			rt := ea1StartRuntime(t, initial)
			callCtx := ctx
			if mode == "unsent-before-chain" {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(ctx)
				defer cancel()
				cancelBeforeChain = &unsentCancelObserver{cancel: cancel, callID: "next-call"}
			}
			r := makeRunner(rt, newToolTestLedger(t, initial), true)
			req := agent.StartRequest{ID: "batch-boundary", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "three calls", ToolKeys: []string{"read"}}
			if mode == "allowance" {
				req.CallAllowance = &agent.CallAllowance{ToolCalls: 2}
			}
			started, e := r.StartRun(callCtx, req)
			if mode == "unsent-before-chain" {
				if !errors.Is(e, context.Canceled) || cancelBeforeChain.calls != 1 {
					t.Fatal("unsent sibling cut", e)
				}
				cancelBeforeChain = nil
				e = nil
			}
			if mode == "approval-frontier-persistent" {
				if !errors.Is(e, fault.injected) || fault.hits != 2 || physical["child-a"] != 1 || physical["child-b"] != 1 || physical["next-call"] != 0 || policies["next-call"] != 1 {
					t.Fatal("persistent write fault not reached", e, fault.hits, physical, policies)
				}
				before, e := observer.Load(ctx, req.ID)
				if e != nil {
					t.Fatal(e)
				}
				g, e := observer.LookupChildExecution(ctx, req.ID)
				if e != nil {
					t.Fatal(e)
				}
				c, e := observer.LookupPhysicalCallbackReturn(ctx, g)
				if e != nil || c.HostCompleted || !bytes.Contains(before.State, []byte(`"phase":"evaluating"`)) {
					t.Fatal("uncommitted policy obtained completed custody", c, e)
				}
				for _, child := range children {
					if _, e = observer.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")}}); e != nil {
						t.Fatal(e)
					}
				}
				fresh := reopen()
				rt = ea1StartRuntime(t, fresh)
				l := newToolTestLedger(t, fresh)
				r = makeRunner(rt, l, false)
				if _, e = r.ReconcileRootDeferredChildren(ctx, req.ID, before.Run.Revision); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
					t.Fatal("unknown original authorization allowed child cleanup", e)
				}
				if _, e = r.Resume(ctx, req.ID, before.Run.Revision); !errors.Is(e, agent.ErrApprovalRequired) {
					t.Fatal("unknown original authorization was bypassed", e)
				}
				if e = r.ReconcileRootPhysicalExecution(ctx, req.ID); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
					t.Fatal("unknown authorization retired", e)
				}
				after, e := rt.Load(ctx, req.ID)
				if e != nil || !reflect.DeepEqual(after, before) || physical["next-call"] != 0 || len(m.requests) != 1 || len(reads) != 0 {
					t.Fatal("replacement changed original unresolved authority", e, physical, reads)
				}
				return
			}
			if actualDenial {
				// Native cancellation blocks the unsent sibling while the
				// earlier physical children still drain, rather than creating
				// a speculative no-send reservation for that sibling.
				if e != nil || denialFault.hits != 1 || physical["child-a"] != 1 || physical["child-b"] != 1 || physical["next-call"] != 0 || policies["next-call"] != 1 || len(m.requests) != 1 {
					t.Fatal("actual batch denial cut not reached", e, physical, policies)
				}
				g, err := observer.LookupChildExecution(ctx, req.ID)
				if err != nil {
					t.Fatal(err)
				}
				c, err := observer.LookupPhysicalCallbackReturn(ctx, g)
				if err != nil || !c.HostCompleted {
					t.Fatal("earlier sibling obligations lost", c, err)
				}
				bindings, err := c.ToolReturns.Bindings()
				if err != nil {
					t.Fatal(err)
				}
				if len(bindings) != 2 {
					t.Fatal("wrong original obligations", bindings)
				}
				for _, b := range bindings {
					if b.CallID == "next-call" || b.PendingKind != "child_terminal" || b.Unsent || b.ApprovalDigest == "" {
						t.Fatal("earlier physical receipt lost", b)
					}
				}
				for _, child := range children {
					if _, err = observer.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")}}); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "actual-dispatch-denial-final-child-before" || mode == "actual-dispatch-denial-final-child-ack" {
					stage := "recovery-ack"
					if mode == "actual-dispatch-denial-final-child-before" {
						stage = "recovery-before"
					}
					fault := &returnedRootRetirementFault{managedStorePorts: backing.(managedStorePorts), LedgerStore: ledger, mode: stage, recoveryCallID: "child-b"}
					r = makeRunner(ea1StartRuntime(t, fault), fault, false)
					original, err := r.RootDeferredCleanupEligibility(ctx, req.ID, started.Run.Revision)
					if err != nil || original.RetirementOnly() {
						t.Fatal("original child scope missing", err)
					}
					_, err = r.ReconcileRootDeferredChildren(ctx, req.ID, started.Run.Revision, original)
					if !errors.Is(err, errReturnedRootRetirementCut) || fault.hits != 1 || fault.grant != g || reads["child-a"] != 1 || reads["child-b"] != 1 {
						t.Fatal("last original child ACK cut missing", err, reads)
					}
					fresh := reopen()
					rt = ea1StartRuntime(t, fresh)
					l := newToolTestLedger(t, fresh)
					r = makeRunner(rt, l, false)
					after, err := rt.Load(ctx, req.ID)
					if err != nil {
						t.Fatal(err)
					}
					ready, err := r.RootDeferredCleanupEligibility(ctx, req.ID, after.Run.Revision)
					if err != nil || ready.RetirementOnly() != (stage == "recovery-ack") || !original.Matches(ready) {
						t.Fatal("last child receipt did not yield same original retirement", err)
					}
					after, err = r.ReconcileRootDeferredChildren(ctx, req.ID, after.Run.Revision, original)
					wantReads := 1
					if stage == "recovery-before" {
						wantReads = 2
					}
					if err != nil || reads["child-a"] != 1 || reads["child-b"] != wantReads || reads["next-call"] != 0 || len(m.requests) != 1 {
						t.Fatal("retirement re-read original child effects", err, reads)
					}
					if _, err = rt.LookupChildExecution(ctx, req.ID); !errors.Is(err, kernel.ErrNotFound) {
						t.Fatal("retirement retained batch grant", err)
					}
					final, err := rt.Cancel(ctx, req.ID, after.Run.Revision, "finish original elected cancellation")
					if err != nil || final.Run.Status != kernel.RunStatusCancelled {
						t.Fatal("elected cancellation blocked after last receipt", err, final.Run)
					}
					charged, err := l.LoadBudget(ctx, "original-ledger")
					if err != nil || charged.View("").Usage.ToolCalls != 2 || charged.View("").Reserved.ToolCalls != 0 || physical["next-call"] != 0 {
						t.Fatal("retirement changed original accounting", err, charged.View(""), physical)
					}
					return
				}
				fresh := reopen()
				rt = ea1StartRuntime(t, fresh)
				l := newToolTestLedger(t, fresh)
				r = makeRunner(rt, l, false)
				cur, err := r.ReconcileRootDeferredChildren(ctx, req.ID, started.Run.Revision)
				if err != nil || reads["child-a"] != 1 || reads["child-b"] != 1 || reads["next-call"] != 0 || physical["next-call"] != 0 || len(m.requests) != 1 {
					t.Fatal("original siblings/no-send recovery failed", err, reads, physical)
				}
				if _, err = rt.LookupChildExecution(ctx, req.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("batch grant not retired", err)
				}
				final, err := rt.Cancel(ctx, req.ID, cur.Run.Revision, "finish original elected cancellation")
				if err != nil || final.Run.Status != kernel.RunStatusCancelled {
					t.Fatal("elected cancellation blocked", err, final.Run)
				}
				saved, err := l.LoadBudget(ctx, "original-ledger")
				if err != nil {
					t.Fatal(err)
				}
				third, exists := saved.Reservations["tool:batch-boundary:next-call"]
				if saved.View("").Usage.ToolCalls != 2 || saved.View("").Reserved.ToolCalls != 0 || exists {
					t.Fatal("wrong original accounting", saved.View(""), third)
				}
				return
			}
			if e != nil || started.Run.Status != kernel.RunStatusRunning || started.Checkpoint != nil || physical["child-a"] != 1 || physical["child-b"] != 1 || len(m.requests) != 1 {
				t.Fatal("batch did not reach original physical frontier", e, started.Run.Status, physical)
			}
			if fault != nil && fault.hits != 1 {
				t.Fatal("original frontier fault not exercised", fault.hits)
			}
			g, e := observer.LookupChildExecution(ctx, req.ID)
			if e != nil {
				t.Fatal(e)
			}
			c, e := observer.LookupPhysicalCallbackReturn(ctx, g)
			wantCustody := kernel.OwnedBoundedApprovalReturn
			if mode == "unsent-before-chain" {
				// The live cancellation observer is opaque, even though the
				// third Call never committed a native dispatch obligation.
				wantCustody = ""
			}
			if e != nil || !c.HostCompleted || !c.MatchesReturnSnapshot(started) || c.ControlCustody != wantCustody {
				t.Fatal("wrong original completion", c, e)
			}
			bindings, e := c.ToolReturns.Bindings()
			if e != nil || len(bindings) != 2 {
				t.Fatal("original physical siblings not bound", bindings, e)
			}
			for _, b := range bindings {
				if (b.ApprovalDigest != "") != (mode != "unsent-before-chain") || b.PendingKind != "child_terminal" || b.ReceiptDigest != "" || b.Unsent || b.LedgerID != "original-ledger" {
					t.Fatal("invalid sibling evidence", b)
				}
			}
			if mode != "parallel" && physical["next-call"] != 0 {
				t.Fatal("new Call passed pause boundary")
			}
			if mode == "parallel" && physical["next-call"] != 1 {
				t.Fatal("unrestricted sibling did not execute")
			}
			if mode == "unsent-before-chain" {
				saved, err := ledger.LoadBudget(ctx, "original-ledger")
				if err != nil {
					t.Fatal(err)
				}
				r, exists := saved.Reservations["tool:batch-boundary:next-call"]
				if exists || policies["next-call"] != 1 {
					t.Fatal("unsent sibling capacity not parked", r, policies)
				}
			}
			if approvalMode && policies["next-call"] != 1 {
				t.Fatal("required policy was not evaluated once", policies)
			}
			if mode == "allowance" && policies["next-call"] != 0 {
				t.Fatal("allowance boundary entered a new policy", policies)
			}
			for _, child := range children {
				if _, e = observer.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")}}); e != nil {
					t.Fatal(e)
				}
			}
			fresh := reopen()
			rt = ea1StartRuntime(t, fresh)
			l := newToolTestLedger(t, fresh)
			// Replacement without policies must honor the originally parked
			// restrictive decision, never repeat it or silently authorize.
			r = makeRunner(rt, l, !approvalMode)
			if _, e = r.ResolveApproval(ctx, req.ID, started.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove}); !errors.Is(e, agent.ErrApprovalRequired) {
				t.Fatal("approval accepted before physical settlement", e)
			}
			cur := started
			for _, id := range []string{"child-a", "child-b"} {
				view, e := agent.ViewState(cur)
				deferred := mode == "unsent-before-chain" && id == "child-a"
				if e != nil || (view.PendingTool == nil) != deferred || !deferred && view.PendingTool.CallID != id {
					t.Fatal("wrong physical frontier", id, view.PendingTool, e)
				}
				if _, e = r.ReconcileToolOutcome(ctx, req.ID, cur.Run.Revision, "next-call"); !errors.Is(e, agent.ErrToolOutcomeUnknown) {
					t.Fatal("unsent call recovered", e)
				}
				cur, e = r.ReconcileToolOutcome(ctx, req.ID, cur.Run.Revision, id)
				if e != nil {
					t.Fatal("original sibling recovery", id, e)
				}
				if len(m.requests) != 1 || reads[id] != 1 {
					t.Fatal("recovery performed new effects", reads)
				}
			}
			if _, e = rt.LookupChildExecution(ctx, req.ID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("original grant not retired", e)
			}
			cur, e = r.Resume(ctx, req.ID, cur.Run.Revision)
			if e != nil {
				t.Fatal("consume original receipts", e)
			}
			switch mode {
			case "approval-approve", "approval-reject", "approval-frontier-before", "approval-frontier-ack":
				if cur.Run.Status != kernel.RunStatusWaitingInput || cur.Checkpoint == nil || physical["next-call"] != 0 || policies["next-call"] != 1 || len(m.requests) != 1 {
					t.Fatal("parked approval not published exactly once after retirement", cur.Run.Status, physical, policies)
				}
				decision := plugin.ApprovalApprove
				if mode == "approval-reject" {
					decision = plugin.ApprovalReject
				}
				r = makeRunner(rt, l, true)
				cur, e = r.ResolveApproval(ctx, req.ID, cur.Run.Revision, plugin.ApprovalResponse{Decision: decision})
				if e != nil {
					t.Fatal(e)
				}
				if cur.Run.Status == kernel.RunStatusRunning {
					cur, e = r.Resume(ctx, req.ID, cur.Run.Revision)
					if e != nil {
						t.Fatal(e)
					}
				}
			case "allowance":
				if cur.Run.Status != kernel.RunStatusPausedBudget || physical["next-call"] != 0 || len(m.requests) != 1 {
					t.Fatal("allowance pause crossed physical frontier", cur.Run.Status, physical)
				}
				cur, e = r.GrantCallAllowance(ctx, req.ID, cur.Run.Revision, agent.CallAllowance{ToolCalls: 1})
				if e != nil {
					t.Fatal(e)
				}
				cur, e = r.Resume(ctx, req.ID, cur.Run.Revision)
				if e != nil {
					t.Fatal(e)
				}
			}
			if cur.Run.Status != kernel.RunStatusCompleted || len(m.requests) != 2 {
				t.Fatal("batch did not complete", cur.Run.Status, len(m.requests))
			}
			want := map[string]int{"child-a": 1, "child-b": 1, "next-call": 1}
			charges := 3
			if mode == "approval-reject" {
				delete(want, "next-call")
				charges = 2
			}
			if !reflect.DeepEqual(physical, want) {
				t.Fatal("wrong physical effects", physical, want)
			}
			charged, e := l.LoadBudget(ctx, "original-ledger")
			if e != nil || charged.View("").Usage.ToolCalls != charges || charged.View("").Reserved.ToolCalls != 0 {
				t.Fatal("original settlement mismatch", e, charged.View(""))
			}
			wantControl := mode != "unsent-before-chain"
			if r.VerifyDeferredToolResults(ctx, rt, cur) != wantControl || r.VerifiedDeferredRootControl(ctx, rt, cur) != wantControl {
				t.Fatal("final approval/owner custody changed", mode, wantControl)
			}
			seen := map[string]int{}
			for _, msg := range m.requests[1].Messages {
				if msg.Role == model.RoleTool {
					seen[msg.ToolCallID]++
				}
			}
			if !reflect.DeepEqual(seen, map[string]int{"child-a": 1, "child-b": 1, "next-call": 1}) {
				t.Fatal("model-facing batch outcomes lost or duplicated", seen)
			}
		})
	}
}
