package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type externalCancelOwner struct {
	mu               sync.Mutex
	mode             string
	prepared, models int
	controls         map[string]agent.ExternalToolControl
	deadline         time.Time
	nested           bool
}

func (o *externalCancelOwner) Execute(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prepared++
	return tools.ExecutionResult{Content: json.RawMessage("null"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: tools.ReceiptDispositionPending},
		Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "operation-" + q.RunID, InputDigest: strings.Repeat("a", 64), DeadlineAt: &o.deadline}}, nil
}
func (o *externalCancelOwner) VerifyExternalToolNoEffect(_ context.Context, q tools.ExecutionRequest, c agent.ExternalToolControl) (agent.ExternalToolNoEffectReceipt, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.mode != "no-effect" && o.mode != "foreign" || o.controls[q.RunID] != c || q.Call.ID != "browser" {
		return agent.ExternalToolNoEffectReceipt{}, agent.ErrToolOutcomeUnknown
	}
	if o.mode == "foreign" {
		c.Actor.ActorID = "foreign"
	}
	return agent.ExternalToolNoEffectReceipt{ExternalToolControl: c, ReceiptID: "receipt-" + q.RunID}, nil
}
func (o *externalCancelOwner) VerifyCommittedTool(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.mode != "committed" || o.controls[q.RunID].CallID != q.Call.ID {
		return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
	}
	return tools.ExecutionResult{Content: json.RawMessage(`{"original":"result"}`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed", OwnerReceiptDigest: strings.Repeat("b", 64)}}, nil
}
func (o *externalCancelOwner) Generate(_ context.Context, q model.Request) (model.Response, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.models++
	response := model.Response{Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}
	if q.Model == "root" {
		response.ToolCalls = []tools.Call{{ID: "delegate-1", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"leaf","goal":"first"}`)}, {ID: "delegate-2", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"leaf","goal":"second"}`)}}
		if o.nested {
			response.ToolCalls[0].Arguments = json.RawMessage(`{"memberID":"branch","goal":"nested"}`)
		}
	} else {
		response.ToolCalls = []tools.Call{{ID: "browser", ToolKey: "fixture.browser", Arguments: json.RawMessage(`{}`)}}
	}
	return response, nil
}

type externalCancelPolicy struct{}

func (externalCancelPolicy) PrepareDelegation(_ context.Context, _ tools.ExecutionRequest, q harness.DelegateRequest) (harness.DelegateRequest, error) {
	if q.MemberID != "leaf" && q.MemberID != "branch" {
		return q, harness.ErrInvalidRequest
	}
	q.MemberRevision = "v1"
	q.Agent = agent.StartRequest{Model: "leaf", ToolKeys: []string{"fixture.browser"}}
	return q, nil
}

type externalCancelBundle struct {
	host    *harness.Runner
	agent   *agent.Runner
	runtime *kernel.Runtime
}

func composeExternalCancel(t *testing.T, f NativeAuthorityFixture, o *externalCancelOwner) externalCancelBundle {
	t.Helper()
	fail := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	rt, e := kernel.New(kernel.Dependencies{Store: f.Kernel})
	fail(e)
	relations, e := runrelation.New(f.Relations, nil)
	fail(e)
	d := harness.NewDelegationToolHandler()
	registry, e := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(d), {Definition: tools.Definition{Key: "fixture.browser", Name: "browser", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: o}})
	fail(e)
	authority, e := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: rt, Store: f.Host, Relations: relations, Clock: transitionClock{}})
	fail(e)
	b, e := authority.Adapter()
	fail(e)
	approval, e := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
		return plugin.ApprovalNotRequired, nil
	})
	fail(e)
	native, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: o, Catalog: registry, Executor: registry, BoundedBudget: b, ExternalToolNoEffectVerifier: o, ApprovalPolicies: []plugin.ApprovalPolicy{approval}, DelegatedToolResults: d,
		ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(ctx context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if q.Call.ToolKey == harness.DelegationToolKey {
				return d.VerifyCommittedTool(ctx, q)
			}
			return o.VerifyCommittedTool(ctx, q)
		})})
	fail(e)
	route, e := handoff.NewDeferredAgent(native)
	fail(e)
	host, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: f.Host, Clock: transitionClock{}, Catalog: registry, Handoffs: route, Relations: relations, Budget: authority, DelegationPolicy: externalCancelPolicy{}})
	fail(e)
	fail(d.Bind(host))
	return externalCancelBundle{host: host, agent: native, runtime: rt}
}
func externalCancelRequest(direct bool) harness.StartRequest {
	q := harness.StartRequest{HostThread: harness.HostRef{Kind: "conversation", ID: "external-cancel"}, HostTurn: harness.HostRef{Kind: "turn", ID: "original"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "external-cancel"}, Goal: "original", Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "root", ToolKeys: []string{harness.DelegationToolKey}}, SharedBudget: &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 8, MaxChildRuns: 4, MaxConcurrentRuns: 8}, DelegationPolicy: harness.DelegationPolicySnapshot{MaxDepth: 1, LocalToolGrants: []harness.DelegatedLocalToolGrant{{MemberID: "leaf", MemberRevision: "v1", ToolKey: "fixture.browser"}}}, ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(), {Key: "fixture.browser", DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: "always"}}}}
	if direct {
		q.Config.Model = "leaf"
		q.Config.ToolKeys = []string{"fixture.browser"}
		q.Config.DelegationPolicy = harness.DelegationPolicySnapshot{}
		q.Config.ToolPolicies = q.Config.ToolPolicies[1:]
	}
	return q
}

// NativeExternalCancellation exercises actual accounting CAS and original
// retirement on each native backend, including reconstruction without a Runner.
func NativeExternalCancellation(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, scope := range []string{"direct", "member", "parent", "nested"} {
		for _, mode := range []string{"no-effect", "committed"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				f := factory(t)
				o := &externalCancelOwner{mode: mode, controls: map[string]agent.ExternalToolControl{}, deadline: time.Now().UTC().Add(time.Hour)}
				o.nested = scope == "nested"
				b := composeExternalCancel(t, f, o)
				ctx := t.Context()
				req := externalCancelRequest(scope == "direct")
				if o.nested {
					req.Config.DelegationPolicy.MaxDepth = 2
					req.Config.DelegationPolicy.LocalToolGrants = append(req.Config.DelegationPolicy.LocalToolGrants, harness.DelegatedLocalToolGrant{MemberID: "branch", MemberRevision: "v1", ToolKey: "fixture.browser"})
				}
				start, e := b.host.StartAgentTurn(ctx, req)
				if e != nil {
					t.Fatal(e)
				}
				root, _ := harness.TopLevelInvocation(start)
				target := root.ExecutionRefID
				selected := []string{root.ExecutionRefID}
				if scope != "direct" {
					if len(start.Subtasks) != 2 {
						t.Fatalf("siblings: %+v", start.Subtasks)
					}
					selected = nil
					for _, task := range start.Subtasks {
						s, e := b.agent.LoadRun(ctx, task.RunID)
						if e != nil {
							t.Fatal(e)
						}
						if _, e = b.agent.Resume(ctx, task.RunID, s.Run.Revision); e != nil {
							t.Fatal(e)
						}
						selected = append(selected, task.RunID)
					}
					if scope == "member" {
						target = selected[0]
						selected = selected[:1]
					}
					if o.nested {
						for _, task := range start.Subtasks {
							if task.MemberID == "branch" {
								target = task.RunID
							}
						}
						if _, e = b.host.DelegateByExecutionRefID(ctx, target, harness.DelegateRequest{MemberID: "leaf", Goal: "nested leaf"}); e != nil {
							t.Fatal(e)
						}
						start, e = b.host.Load(ctx, start.Turn.ID)
						if e != nil {
							t.Fatal(e)
						}
						selected = []string{target}
						for _, task := range start.Subtasks {
							if task.MemberID == "branch" {
								target = task.RunID
							}
							if task.ParentID != "" {
								s, e := b.agent.LoadRun(ctx, task.RunID)
								if e != nil {
									t.Fatal(e)
								}
								if _, e = b.agent.Resume(ctx, task.RunID, s.Run.Revision); e != nil {
									t.Fatal(e)
								}
								selected = append(selected, task.RunID)
							}
						}
						if len(selected) != 2 {
							t.Fatalf("nested leaf: %+v", start.Subtasks)
						}
					}
				}
				before, e := b.runtime.BudgetStore().LoadBudget(ctx, start.Turn.ID)
				if e != nil {
					t.Fatal(e)
				}
				waits := map[string]kernel.Snapshot{}
				grants := map[string]kernel.ChildExecutionGrant{}
				for _, id := range selected {
					s, e := b.agent.LoadRun(ctx, id)
					if e != nil {
						t.Fatal(e)
					}
					waits[id] = s
					g, e := b.runtime.LookupChildExecution(ctx, id)
					if e != nil {
						t.Fatal(e)
					}
					grants[id] = g
				}
				if scope == "member" || scope == "nested" {
					for _, task := range start.Subtasks {
						if task.RunID == target {
							_, e = b.host.CancelSubtask(ctx, start.Turn.ID, task.ID, "original")
						}
					}
				} else {
					_, e = b.host.Cancel(ctx, start.Turn.ID, "original")
				}
				if !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatalf("initial election: %v", e)
				}
				for _, id := range selected {
					s := waits[id]
					parent := ""
					if scope != "direct" {
						parent = root.ExecutionRefID
					}
					if o.nested && id != target {
						parent = target
					}
					c := agent.ExternalToolControl{Actor: s.Run.Actor, ParentRunID: parent, RunID: id, CallID: "browser", OperationID: "operation-" + id, InputDigest: strings.Repeat("a", 64), ControlID: "original-control"}
					if e = b.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, target, c); e != nil {
						t.Fatalf("owner scope: %v", e)
					}
					o.controls[id] = c
					for _, bad := range []string{"missing", "started", "denied", "foreign"} {
						o.mode = bad
						if _, e = b.agent.ReconcileExternalToolNoEffectForControl(ctx, s.Run.Revision, c); e == nil {
							t.Fatalf("accepted %s", bad)
						}
					}
					o.mode = mode
					if mode == "no-effect" {
						_, e = b.agent.ReconcileExternalToolNoEffectForControl(ctx, s.Run.Revision, c)
					} else {
						_, e = b.agent.ReconcileExternalToolForControl(ctx, id, s.Run.Revision, "browser")
					}
					if e != nil {
						t.Fatalf("original %s receipt: %v", mode, e)
					}
				}
				if f.Reopen != nil {
					f.Kernel, f.Host, f.Relations = f.Reopen()
				}
				b = composeExternalCancel(t, f, o)
				for _, id := range selected {
					s, e := b.agent.LoadRun(ctx, id)
					if e != nil {
						t.Fatal(e)
					}
					parked, e := b.agent.Resume(ctx, id, s.Run.Revision)
					if e != nil || parked.Run.Revision != s.Run.Revision {
						t.Fatalf("Resume after reopen: %v", e)
					}
					if e = b.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, target, o.controls[id]); e != nil {
						t.Fatalf("same owner after receipt/reopen: %v", e)
					}
					if mode == "no-effect" {
						_, e = b.agent.ReconcileExternalToolNoEffectForControl(ctx, waits[id].Run.Revision, o.controls[id])
					} else {
						_, e = b.agent.ReconcileExternalToolForControl(ctx, id, waits[id].Run.Revision, "browser")
					}
					if e != nil {
						t.Fatalf("receipt replay: %v", e)
					}
					retired, e := b.runtime.LoadOwnedRetirement(ctx, grants[id].ID)
					if e != nil || !retired.Valid() || retired.Evidence.Grant != grants[id] {
						t.Fatalf("original retirement: %v", e)
					}
				}
				if _, e = b.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, target, "foreign-control", "foreign"); e == nil {
					t.Fatal("foreign control completed")
				}
				if _, e = b.host.ContinueExternalToolCancellation(ctx, start.Turn.ID, target, "original-control", "original"); e != nil {
					t.Fatalf("original continuation: %v", e)
				}
				after, e := b.runtime.BudgetStore().LoadBudget(ctx, start.Turn.ID)
				if e != nil {
					t.Fatal(e)
				}
				for _, id := range selected {
					s, e := b.agent.LoadRun(ctx, id)
					if e != nil || s.Run.Status != kernel.RunStatusCancelled {
						t.Fatalf("cancelled original: %+v %v", s.Run, e)
					}
					res := after.Reservations["tool:"+id+":browser"]
					want := 0
					if mode == "committed" {
						want = 1
					}
					if res.Status != budget.ReservationSettled || res.Usage.ToolCalls != want {
						t.Fatalf("original charge: %+v", res)
					}
				}
				if scope == "member" {
					sibling := start.Subtasks[1].RunID
					id := "tool:" + sibling + ":browser"
					if !reflect.DeepEqual(before.Reservations[id], after.Reservations[id]) || after.RunCancelled(sibling) {
						t.Fatal("sibling hold changed")
					}
					s, e := b.agent.LoadRun(ctx, sibling)
					if e != nil || s.Run.Status != kernel.RunStatusRunning {
						t.Fatalf("sibling cancelled: %v", e)
					}
				}
				wantModels, wantPreparations := 3, 2
				if scope == "direct" {
					wantModels, wantPreparations = 1, 1
				}
				if o.nested {
					wantModels, wantPreparations = 4, 3
				}
				if o.models != wantModels || o.prepared != wantPreparations {
					t.Fatalf("replayed effects: models=%d tools=%d", o.models, o.prepared)
				}
			})
		}
	}
}
