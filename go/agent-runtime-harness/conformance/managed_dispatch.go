package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type dispatchFrontierModel struct {
	approval              bool
	continueAfterApproval bool
	late                  bool
	requests              []model.Request
}

func (m *dispatchFrontierModel) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	if len(m.requests) == 1 {
		if m.approval {
			return model.Response{ToolCalls: []tools.Call{{ID: "original-write", ToolKey: "fixture.write", Arguments: json.RawMessage(`{}`)}}}, nil
		}
		return model.Response{Content: "continue original", Continue: true}, nil
	}
	if m.continueAfterApproval && len(m.requests) == 2 {
		return model.Response{Content: "approved result, continue", Continue: true}, nil
	}
	if m.late {
		<-ctx.Done()
	}
	return model.Response{Content: "original result"}, nil
}

// The original callback's record fails before commit after the native
// Control, Model and Tool obligations already settled at a safe point.
type approvalReturnFailure struct{ preparationKernelPorts }

func (s approvalReturnFailure) RecordOwnedPhysicalCallbackReturn(context.Context, kernel.ChildExecutionGrant, uint64, kernel.OwnedPhysicalCallbackOutcome, kernel.OwnedPhysicalCallbackReturn) error {
	return errors.New("original callback record refused before commit")
}

// ManagedPhysicalDispatch exercises a new independently admitted native
// dispatch on the same Run after ordinary or managed Start retirement. A
// successor still cannot mint another point inside the same dispatch.
func ManagedPhysicalDispatch(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store))) {
	for _, c := range []struct {
		name, event string
		after       bool
	}{
		{"resume/intent-before", "agent.model_invocation.pending", false},
		{"resume/intent-after", "agent.model_invocation.pending", true},
		{"resume/receipt-after", "agent.model_invocation.completed", true},
		{"resume/late-successor", "agent.model_invocation.pending", true},
		{"resume/cancel", "agent.model_invocation.pending", true},
		{"resume/unknown", "agent.model_invocation.dispatch_intent", true},
		{"repeat/intent-after", "agent.model_invocation.pending", true},
		{"approval/inline", "tool.completed", true},
		{"approval/return-record-failed", "agent.model_continued", true},
		{"approval/prior-projection", "agent.model_continued", true},
		{"approval/conflicting-projection", "agent.model_continued", true},
		{"approval/reject", "agent.model_invocation.pending", true},
		{"approval/deferred", "tool.completed", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			base, host, reopen := factory(t)
			h := host.(preparationHostPorts)
			approval := strings.HasPrefix(c.name, "approval/")
			inline := approval && c.name != "approval/deferred"
			repeated := strings.HasPrefix(c.name, "repeat/")
			returnFailure := c.name == "approval/return-record-failed" || c.name == "approval/prior-projection" || c.name == "approval/conflicting-projection"
			client := &dispatchFrontierModel{approval: approval, continueAfterApproval: returnFailure}
			inputs, e := agent.NewMemoryRunInputs("dispatch-input")
			if e != nil {
				t.Fatal(e)
			}
			execution, leaseDuration := ManagedFixtureExecutionPolicy()
			effects := 0
			opts := harness.AgentSessionOptions{Store: h, Model: client, Inputs: inputs, Context: harness.AgentSessionContextOptions{ManagedRecovery: true, ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 128}, nil
			}},
				Session: harness.SessionOptions{HostThread: harness.HostRef{Kind: "conversation", ID: "dispatch-frontier"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "dispatch-frontier"}, Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 128}, SharedBudget: &budget.Limits{MaxLLMCalls: 3, MaxToolCalls: 2}}, Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
					return nil, nil
				})},
				Execution: execution}
			if !inline {
				opts.RunExecution = &agent.RunExecution{OwnerID: "original-start", LeaseDuration: leaseDuration}
			}
			if approval {
				opts.LocalTools = []harness.SessionTool{{Registration: tools.Registration{Definition: tools.Definition{Key: "fixture.write", Name: "fixture write", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}, Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
					effects++
					return tools.ExecutionResult{Content: json.RawMessage(`{"original":"effect"}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
				})}, Policy: harness.ToolPolicySnapshot{Key: "fixture.write", DefinitionVersion: "fixture-v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write"}}}
			}
			open := func(k kernel.Store) (*harness.SessionClient, *kernel.Runtime) {
				t.Helper()
				rt, err := kernel.New(kernel.Dependencies{Store: k, ManagedRecoveryHost: h})
				if err != nil {
					t.Fatal(err)
				}
				opts.Runtime, opts.Store = rt, h
				session, err := harness.OpenAgentSession(opts)
				if err != nil {
					t.Fatal(err)
				}
				return session, rt
			}
			crash := func(f func()) {
				t.Helper()
				defer func() {
					if v := recover(); v == nil {
						t.Fatal("cut not reached")
					} else if _, ok := v.(preparationInterrupted); !ok {
						panic(v)
					}
				}()
				f()
			}
			var first harness.Snapshot
			var session *harness.SessionClient
			var rt *kernel.Runtime
			if repeated {
				cut := &preparationCut{preparationKernelPorts: base.(preparationKernelPorts), event: "agent.model_invocation.pending", remaining: 1, after: true}
				session, rt = open(cut)
				crash(func() { _, _ = session.Prompt(ctx, "original", "original task") })
				initial, e := session.Load(ctx, "original")
				if e != nil {
					t.Fatal(e)
				}
				root, _ := harness.TopLevelInvocation(initial)
				point, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				source, e := base.Load(ctx, root.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				AwaitManagedOriginalLeaseExpiry(t, base, source, point, 2*leaseDuration+5*time.Second)
				opts.Session.Transcript = nil
				session, rt = open(base)
				first, e = session.Resume(ctx, "original")
				if e != nil {
					t.Fatalf("initial managed yield: %v", e)
				}
			} else {
				session, rt = open(base)
				first, e = session.Prompt(ctx, "original", "original task")
				if e != nil {
					t.Fatalf("start: %v", e)
				}
			}
			root, ok := harness.TopLevelInvocation(first)
			if !ok {
				t.Fatal("missing root")
			}
			source, e := base.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			var checkpoint string
			if approval {
				if source.Checkpoint == nil || source.Run.Status != kernel.RunStatusWaitingInput {
					t.Fatal("missing original approval")
				}
				checkpoint = source.Checkpoint.ID
				if !inline {
					first, e = session.ResolveApproval(ctx, "original", harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "original grant"})
					if e != nil || first.Turn.Status != harness.TurnRunning || effects != 0 {
						t.Fatalf("control-only approval: %v %s", e, first.Turn.Status)
					}
				}
			} else if first.Turn.Status != harness.TurnRunning {
				t.Fatalf("first dispatch did not yield: %s", first.Turn.Status)
			}
			prior, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			if repeated && prior.State != kernel.ManagedClosed {
				t.Fatalf("first selected transfer not closed: %s", prior.State)
			}
			if _, e = rt.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("first grant not retired: %v", e)
			}
			if harness.PendingExecutionDispatch(first.Items, root.ID) {
				t.Fatal("first physical dispatch not closed")
			}
			cut := &preparationCut{preparationKernelPorts: base.(preparationKernelPorts), event: c.event, remaining: 1, after: c.after}
			opts.Session.Transcript = nil
			if !inline {
				opts.RunExecution = &agent.RunExecution{OwnerID: "new-original", LeaseDuration: leaseDuration}
			}
			if returnFailure {
				session, rt = open(approvalReturnFailure{base.(preparationKernelPorts)})
			} else {
				session, rt = open(cut)
			}
			decision := harness.ApprovalApprove
			if c.name == "approval/reject" {
				decision = harness.ApprovalReject
			}
			unknown := c.name == "resume/unknown"
			if returnFailure {
				_, err := session.ResolveApproval(ctx, "original", harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: decision, Comment: "original grant"})
				if !errors.Is(err, agent.ErrRootOutcomeUnknown) {
					t.Fatalf("expected original callback record failure: %v", err)
				}
			} else if unknown {
				// The native safety wrapper catches a panic inside the final
				// Model dispatch guard. Its committed dispatch marker must
				// retain uncertainty, not become a replayable receipt.
				if _, e = session.Resume(ctx, "original"); !errors.Is(e, agent.ErrModelOutcomeUnknown) {
					t.Fatalf("unknown cut returned: %v", e)
				}
			} else {
				crash(func() {
					if inline {
						got, err := session.ResolveApproval(ctx, "original", harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: decision, Comment: "original grant"})
						t.Logf("uncut approval: %s %v", got.Turn.Status, err)
					} else {
						got, err := session.Resume(ctx, "original")
						t.Logf("uncut resume: %s %v", got.Turn.Status, err)
					}
				})
			}
			pending, e := session.Load(ctx, "original")
			if e != nil {
				t.Fatal(e)
			}
			original, e := base.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			view, e := agent.ViewState(original)
			if e != nil {
				t.Fatal(e)
			}
			point, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			b := point.SafePoint.Host
			wantPhase := "resume"
			if inline {
				wantPhase = "resolve-approval"
			}
			g, e := rt.LookupChildExecution(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			if b.Phase != wantPhase || b.DispatchID == prior.SafePoint.Host.DispatchID || g != point.SafePoint.OriginalGrant || !b.MatchesOriginalGrant(g) || g.ID == prior.SafePoint.OriginalGrant.ID || !harness.PendingExecutionDispatch(pending.Items, root.ID) {
				t.Fatalf("new dispatch did not own its proof: phase=%s grant=%+v", b.Phase, g)
			}
			if inline && (b.Approval == nil || b.Approval.CheckpointID != checkpoint || b.Approval.Decision != string(decision) || b.Approval.Comment != "original grant") {
				t.Fatalf("approval source lost: %+v", b.Approval)
			}
			if !unknown && !kernel.ManagedSourceMatches(point.SafePoint, original) {
				t.Fatal("point lost exact source")
			}
			wantBefore := 1
			if strings.Contains(c.name, "receipt") || returnFailure {
				wantBefore = 2
			}
			wantTools := 0
			if approval && decision == harness.ApprovalApprove {
				wantTools = 1
			}
			ledger, e := h.LoadBudget(ctx, pending.Turn.ID)
			if e != nil || len(client.requests) != wantBefore || effects != wantTools || ledger.View("").Usage.LLMCalls != wantBefore || ledger.View("").Usage.ToolCalls != wantTools || (!unknown && ledger.View("").Reserved != (budget.Usage{})) {
				t.Fatalf("wrong original effects: %v %+v calls=%d tools=%d", e, ledger.View(""), len(client.requests), effects)
			}
			if returnFailure {
				// New normal execution cannot publish a standalone decision
				// after a failed retirement. Exercise compatibility with
				// the old partially projected exact fact and its conflict.
				assertOriginalApprovalDecision(t, pending.Items, checkpoint, "approve", "original grant", 0)
				if point.State != kernel.ManagedAvailable || point.Selection.ID != "" {
					t.Fatal("wrong original return failure cut")
				}
				if _, err := rt.LookupOwnedPhysicalCallbackReturn(ctx, g); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("unexpected original callback evidence: %v", err)
				}
				if c.name != "approval/return-record-failed" {
					dispatch, err := h.GetItem(ctx, b.DispatchID)
					if err != nil {
						t.Fatal(err)
					}
					parentID, ok := harness.OriginalApprovalRequestID(dispatch)
					if !ok {
						t.Fatal("missing original approval request")
					}
					parent, err := h.GetItem(ctx, parentID)
					if err != nil {
						t.Fatal(err)
					}
					prospective := harness.Item{ID: fixtureStableID("hdispatchdone", dispatch.ID), TurnID: dispatch.TurnID, InvocationID: dispatch.InvocationID, RunID: dispatch.RunID, Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: dispatch.ID, Seq: dispatch.Seq + 1, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`), CreatedAt: dispatch.CreatedAt, UpdatedAt: dispatch.UpdatedAt}
					fact, insert, err := harness.PlanOriginalApprovalSettlement(dispatch, prospective, parent, nil)
					if err != nil || !insert {
						t.Fatalf("missing prior projection fixture: %v", err)
					}
					if c.name == "approval/conflicting-projection" {
						fact.Payload = json.RawMessage(strings.ReplaceAll(string(fact.Payload), "original grant", "substituted grant"))
					}
					if _, _, err = h.AppendItem(ctx, fact); err != nil {
						t.Fatal(err)
					}
				}
				_, err := h.ReadManagedOriginalDispatch(ctx, b)
				if (err != nil) != (c.name == "approval/conflicting-projection") {
					t.Fatalf("wrong prior decision qualification: %v", err)
				}
			}
			// Prior grant remains history, even when the current point is new.
			old, e := rt.LoadManagedRecoveryByID(ctx, prior.SafePoint.ID)
			if e != nil || old.SafePoint.OriginalGrant != prior.SafePoint.OriginalGrant {
				t.Fatalf("prior point lost: %v", e)
			}
			if repeated && kernel.ManagedDigest(old) != kernel.ManagedDigest(prior) {
				t.Fatal("closed historical transfer was rewritten")
			}
			if _, e = h.ReadManagedOriginalDispatch(ctx, prior.SafePoint.Host); e == nil {
				t.Fatal("historical dispatch acquired current Host authority")
			}
			if inline {
				bad := b
				decision := *bad.Approval
				decision.Comment = "substituted decision"
				bad.Approval = &decision
				if _, e = h.ReadManagedOriginalDispatch(ctx, bad); e == nil {
					t.Fatal("substituted approval acquired original Host authority")
				}
			}
			base, host = reopen()
			h = host.(preparationHostPorts)
			if !inline {
				opts.RunExecution = &agent.RunExecution{OwnerID: "new-successor", LeaseDuration: leaseDuration}
			}
			session, rt = open(base)
			AwaitManagedOriginalLeaseExpiry(t, base, original, point, 2*leaseDuration+10*time.Second)
			if c.name == "approval/conflicting-projection" {
				if _, e = session.Resume(ctx, "original"); e == nil {
					t.Fatal("conflicting approval was resumed")
				}
				after, e := base.Load(ctx, root.ExecutionRefID)
				if e != nil || kernel.ManagedDigest(after) != kernel.ManagedDigest(original) || len(client.requests) != 2 || effects != 1 {
					t.Fatal("conflicting approval changed original effects")
				}
				g2, e := rt.LookupChildExecution(ctx, root.ExecutionRefID)
				if e != nil || g2 != g {
					t.Fatal("conflicting approval lost original grant")
				}
				return
			}
			if unknown {
				if _, e = session.Resume(ctx, "original"); e == nil {
					t.Fatal("unknown dispatch was resumed")
				}
				after, e := base.Load(ctx, root.ExecutionRefID)
				if e != nil || kernel.ManagedDigest(after) != kernel.ManagedDigest(original) || len(client.requests) != wantBefore || effects != wantTools {
					t.Fatal("unknown dispatch changed or repeated")
				}
				return
			}
			if c.name == "resume/late-successor" {
				client.late = true
				// Preserve the fixture's bounded call window so only the
				// deliberately late Provider (not native admission) times out.
				_, e = session.Resume(ctx, "original")
				client.late = false
				if !errors.Is(e, agent.ErrModelLateResult) || errors.Is(e, harness.ErrConflict) {
					t.Fatalf("late known receipt lost or projected as conflict: %v", e)
				}
				got, e := session.Load(ctx, "original")
				if e != nil || harness.PendingExecutionDispatch(got.Items, root.ID) || len(client.requests) != 2 {
					t.Fatalf("returned successor did not close its original dispatch: %v", e)
				}
				closed, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
				if e != nil || closed.State != kernel.ManagedClosed || closed.SafePoint.Host.DispatchID != b.DispatchID {
					t.Fatalf("late successor not retired: %v", e)
				}
			}
			var done harness.Snapshot
			if c.name == "resume/cancel" {
				done, e = session.Cancel(ctx, "original", "cancel next dispatch")
			} else {
				done, e = session.Resume(ctx, "original")
			}
			wantStatus, wantCalls := harness.TurnCompleted, 2
			if returnFailure {
				wantCalls = 3
			}
			if c.name == "resume/cancel" {
				wantStatus, wantCalls = harness.TurnCancelled, 1
			}
			if e != nil || done.Turn.Status != wantStatus || len(client.requests) != wantCalls || effects != wantTools {
				t.Fatalf("new original dispatch recovery: %v status=%s calls=%d tools=%d", e, done.Turn.Status, len(client.requests), effects)
			}
			after, e := base.Load(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			end, e := agent.ViewState(after)
			if e != nil {
				t.Fatal(e)
			}
			ledger, e = h.LoadBudget(ctx, done.Turn.ID)
			if e != nil {
				t.Fatal(e)
			}
			wantInv := wantCalls
			if wantStatus == harness.TurnCancelled {
				wantInv = len(view.ModelInvocations)
			}
			if end.ModelInvocations[0].ID != view.ModelInvocations[0].ID || len(end.ModelInvocations) != wantInv || ledger.View("").Usage.LLMCalls != wantCalls || ledger.View("").Usage.ToolCalls != wantTools || !ledger.FinishedRuns[root.ExecutionRefID] || ledger.View("").Reserved != (budget.Usage{}) {
				t.Fatalf("original identity or accounting lost: %+v", ledger.View(""))
			}
			if _, e = rt.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("original grant retained: %v", e)
			}
			closed, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
			if e != nil {
				t.Fatal(e)
			}
			if closed.SafePoint.Host.DispatchID != b.DispatchID || closed.Selection.Binding.DispatchID != b.DispatchID {
				t.Fatal("closed wrong dispatch")
			}
			if approval {
				decisions := 0
				for _, i := range done.Items {
					if i.Kind == harness.ItemApproval && i.Status == harness.ItemCompleted {
						var payload struct {
							CheckpointID string `json:"checkpointID"`
							Decision     string `json:"decision"`
							Comment      string `json:"comment"`
						}
						if json.Unmarshal(i.Payload, &payload) != nil || payload.CheckpointID != checkpoint || payload.Decision != string(decision) || payload.Comment != "original grant" {
							t.Fatalf("wrong approval projection: %s", i.Payload)
						}
						decisions++
					}
				}
				if decisions != 1 {
					t.Fatalf("approval decision count %d", decisions)
				}
			}
			if harness.PendingExecutionDispatch(done.Items, root.ID) {
				t.Fatal("original dispatch remains open")
			}
			replay, e := session.Resume(ctx, "original")
			if e != nil || replay.Turn.Status != wantStatus || len(client.requests) != wantCalls || effects != wantTools {
				t.Fatalf("terminal replay changed effects: %v", e)
			}
		})
	}
}

func fixtureStableID(prefix string, values ...string) string {
	h := sha256.New()
	for _, v := range values {
		_, _ = h.Write([]byte(strings.TrimSpace(v)))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil))[:24]
}
