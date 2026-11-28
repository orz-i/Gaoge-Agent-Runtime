package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var errNativeChildCreateACKLost = errors.New("native child Create committed, ACK lost")
var errNativeChildCreateInterrupted = errors.New("native child Create interrupted before commit")

type childReturnCut struct {
	harness.Store
	budget.LedgerStore
	cut, beforeInvocation bool
	child                 harness.Invocation
}

func (s *childReturnCut) CreateInvocation(ctx context.Context, i harness.Invocation) (harness.Invocation, bool, error) {
	if s.beforeInvocation && i.ParentItemID != "" {
		s.beforeInvocation, s.child = false, i
		return harness.Invocation{}, false, errNativeChildCreateInterrupted
	}
	return s.Store.CreateInvocation(ctx, i)
}

var errNativeChildHostReturnCut = errors.New("native child Host return interrupted")

func (s *childReturnCut) AppendItem(ctx context.Context, i harness.Item) (harness.Item, bool, error) {
	if s.cut && i.Kind == harness.ItemDelegation && i.Status == harness.ItemCompleted {
		s.cut = false
		return harness.Item{}, false, errNativeChildHostReturnCut
	}
	return s.Store.AppendItem(ctx, i)
}

type nativeChildCreateProbe struct {
	nativeAuthorityKernelPorts
	t                  *testing.T
	mode, child, scope string
	creates            int
	negativeACKLost    bool
	record             kernel.Record
	events             []kernel.EventDraft
}

func (s *nativeChildCreateProbe) DecideBeforeCreate(ctx context.Context, q kernel.RunCreationDecision) (kernel.RunCreationDecision, error) {
	d, err := s.nativeAuthorityKernelPorts.DecideBeforeCreate(ctx, q)
	if err == nil && s.mode == "pre-invocation-cut" && q.LogicalParentRunID != "" && !s.negativeACKLost {
		s.negativeACKLost = true
		return kernel.RunCreationDecision{}, errNativeChildCreateACKLost
	}
	return d, err
}

func (s *nativeChildCreateProbe) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	if r.Run.Kind != "agent" || r.Run.LogicalParentRunID == "" {
		return s.nativeAuthorityKernelPorts.Create(ctx, r, events)
	}
	s.creates++
	if s.child != "" && s.child != r.Run.ID {
		s.t.Fatal("retry changed original child identity")
	}
	s.child, s.scope = r.Run.ID, r.Run.BudgetScopeID
	ledger, err := s.LoadBudget(ctx, s.scope)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, found := ledger.Runs[s.child]; found || r.BudgetEnrollment == nil {
		s.t.Fatal("child registered before native Create")
	}
	if s.creates == 1 && (s.mode == "capacity-race" || s.mode == "exhausted-race") {
		rt, err := kernel.New(kernel.Dependencies{Store: s.nativeAuthorityKernelPorts})
		if err != nil {
			s.t.Fatal(err)
		}
		binding := budget.RunEnrollment{ScopeID: s.scope, Limits: ledger.Limits, Binding: budget.RunBudget{ParentRunID: r.Run.LogicalParentRunID}}
		competitor, err := rt.Create(ctx, kernel.CreateRequest{ID: "capacity-competitor", Kind: "agent", Actor: r.Run.Actor, Thread: r.Run.Thread, Goal: "competing child", Budget: &binding, State: json.RawMessage("{}")})
		if err != nil {
			s.t.Fatal(err)
		}
		before, err := s.LoadBudget(ctx, s.scope)
		if err != nil {
			s.t.Fatal(err)
		}
		_, denied := s.nativeAuthorityKernelPorts.Create(ctx, r, events)
		if !kernel.IsRunBudgetEnrollmentDenied(denied) || (s.mode == "capacity-race" && !errors.Is(denied, budget.ErrWaiting)) || (s.mode == "exhausted-race" && !errors.Is(denied, budget.ErrExhausted)) {
			s.t.Fatalf("native Create denial not exact: %v", denied)
		}
		after, err := s.LoadBudget(ctx, s.scope)
		if err != nil || !reflect.DeepEqual(before, after) {
			s.t.Fatalf("denied Create changed ledger: %v", err)
		}
		if _, err = s.Load(ctx, s.child); !errors.Is(err, kernel.ErrNotFound) {
			s.t.Fatalf("denied Create left child Run: %v", err)
		}
		if _, err = rt.Cancel(ctx, competitor.Run.ID, competitor.Run.Revision, "release slot"); err != nil {
			s.t.Fatal(err)
		}
		return kernel.Snapshot{}, denied
	}
	if s.mode == "unknown-before-create" && s.creates == 1 {
		s.record, s.events = r, append([]kernel.EventDraft(nil), events...)
		return kernel.Snapshot{}, errNativeChildCreateInterrupted
	}
	out, err := s.nativeAuthorityKernelPorts.Create(ctx, r, events)
	if err == nil && s.mode == "unknown-after-create" {
		return kernel.Snapshot{}, errNativeChildCreateACKLost
	}
	return out, err
}

func nativeChildEnrollment(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, mode := range []string{"normal", "capacity-race", "exhausted-race", "unknown-after-create", "unknown-before-create", "host-return-cut", "pre-invocation-cut"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			probe := &nativeChildCreateProbe{nativeAuthorityKernelPorts: f.Kernel.(nativeAuthorityKernelPorts), t: t, mode: mode}
			childCalls := 0
			client := &nativeAuthorityModel{generate: func(r model.Request) (model.Response, error) {
				if r.Model == "child-model" {
					childCalls++
					return model.Response{Content: "child finished"}, nil
				}
				for _, m := range r.Messages {
					if m.Role == model.RoleTool {
						return model.Response{Content: "done"}, nil
					}
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"child","goal":"finish"}`)}}}, nil
			}}
			host := f.Host
			cut := &childReturnCut{Store: f.Host, LedgerStore: f.Host.(budget.LedgerStore), cut: mode == "host-return-cut", beforeInvocation: mode == "pre-invocation-cut"}
			if cut.cut || cut.beforeInvocation {
				host = cut
			}
			options := nativeAuthorityOptions(t, probe, host, client)
			limits := budget.Limits{MaxLLMCalls: 5, MaxToolCalls: 3, MaxChildRuns: 3, MaxConcurrentDescendants: 1, MaxConcurrentRuns: 2}
			if mode == "exhausted-race" {
				limits.MaxChildRuns = 1
				limits.MaxConcurrentDescendants = 0
			}
			options.Session.Config.SharedBudget = &limits
			options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
			options.NamedAgents = []harness.NamedAgent{{MemberID: "child", MemberRevision: "v1", AgentSpec: harness.AgentSpec{Model: "child-model"}}}
			var err error
			options.Relations, err = runrelation.New(f.Relations, nil)
			if err != nil {
				t.Fatal(err)
			}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := session.Prompt(ctx, "binding", "original")
			if mode == "pre-invocation-cut" {
				if !errors.Is(err, errNativeChildCreateInterrupted) || cut.child.ID == "" || childCalls != 0 || probe.creates != 0 {
					t.Fatalf("pre-Invocation cut not observed: %v %+v", err, cut.child)
				}
				id := cut.child.ExecutionRefID
				before, e := probe.LoadBudget(ctx, cut.child.TurnID)
				if e != nil {
					t.Fatal(e)
				}
				snapshot, e := session.Load(ctx, "binding")
				if e != nil || len(snapshot.Subtasks) != 1 {
					t.Fatalf("missing admission: %v %+v", e, snapshot.Subtasks)
				}
				if _, e = session.CancelSubtask(ctx, "binding", snapshot.Subtasks[0].ID, "stop before Invocation"); !errors.Is(e, errNativeChildCreateACKLost) {
					t.Fatalf("missing negative decision ACK loss: %v", e)
				}
				committed, e := probe.LoadCreationDecision(ctx, id)
				if e != nil || committed.Status != kernel.RunStatusCancelled {
					t.Fatalf("negative decision did not commit: %+v %v", committed, e)
				}
				stopped, e := session.CancelSubtask(ctx, "binding", snapshot.Subtasks[0].ID, "retry original cancellation")
				if e != nil || len(stopped.Subtasks) != 1 || stopped.Subtasks[0].Status != "cancelled" {
					t.Fatalf("cancel before Invocation: %v %+v", e, stopped.Subtasks)
				}
				d, e := probe.LoadCreationDecision(ctx, id)
				if e != nil || d.Status != kernel.RunStatusCancelled || d.OriginFingerprint != agent.FrozenOriginFingerprint(cut.child.Input) || !reflect.DeepEqual(d, committed) {
					t.Fatalf("negative identity differs from frozen positive input: %+v %v", d, e)
				}
				if _, e = f.Host.GetInvocation(ctx, cut.child.ID); !errors.Is(e, harness.ErrNotFound) {
					t.Fatalf("invented Invocation: %v", e)
				}
				if _, e = probe.Load(ctx, id); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("invented Run: %v", e)
				}
				after, e := probe.LoadBudget(ctx, cut.child.TurnID)
				if e != nil || !after.RunCancelled(id) || after.View("").Usage.ChildRuns != before.View("").Usage.ChildRuns {
					t.Fatalf("negative enrollment: %v %+v", e, after)
				}
				if _, ok := after.Runs[id]; ok {
					t.Fatal("negative child enrolled")
				}
				return
			}
			if probe.child == "" || probe.creates != 1 {
				t.Fatalf("missing child Create: %v calls=%d", err, probe.creates)
			}
			before, e := probe.LoadBudget(ctx, probe.scope)
			if e != nil {
				t.Fatal(e)
			}
			_, registered := before.Runs[probe.child]
			switch mode {
			case "host-return-cut":
				if !errors.Is(err, errNativeChildHostReturnCut) || !registered || childCalls != 1 || !before.FinishedRuns[probe.child] {
					t.Fatalf("return cut not observed: %v calls=%d", err, childCalls)
				}
				owner, e := f.Host.GetInvocationByExecutionRefID(ctx, probe.child)
				if e != nil {
					t.Fatal(e)
				}
				items, e := f.Host.ListItems(ctx, probe.scope, 0, 1000)
				if e != nil || harness.PendingExecutionDispatch(items, owner.ID) {
					t.Fatalf("native child acquired Host dispatch: %v", e)
				}
				receipt, e := probe.LookupOwnedRetirementForRun(ctx, probe.child)
				if e != nil || !receipt.Valid() || !receipt.Evidence.Completion.HostCompleted || receipt.Evidence.Snapshot.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("lost product ACK lost native return: %v", e)
				}
				if f.Reopen != nil {
					k, h, rel := f.Reopen()
					f.Host = h
					probe.nativeAuthorityKernelPorts = k.(nativeAuthorityKernelPorts)
					options.Store = h
					options.Runtime, e = kernel.New(kernel.Dependencies{Store: probe})
					if e != nil {
						t.Fatal(e)
					}
					options.Relations, e = runrelation.New(rel, nil)
					if e != nil {
						t.Fatal(e)
					}
				}
				options.Session.Transcript = nil
				session, e = harness.OpenAgentSession(options)
				if e != nil {
					t.Fatal(e)
				}
				_, refreshErr := session.Refresh(ctx, "binding")
				items, e = f.Host.ListItems(ctx, probe.scope, 0, 1000)
				if e != nil || harness.PendingExecutionDispatch(items, owner.ID) || childCalls != 1 || probe.creates != 1 {
					t.Fatalf("exact native return not recovered: %v / %v calls=%d", refreshErr, e, childCalls)
				}
				found := false
				for _, i := range items {
					if i.Kind == harness.ItemDelegation && i.Status == harness.ItemCompleted && i.RunID == probe.child {
						found = true
					}
				}
				if !found {
					t.Fatalf("product receipt not recovered: %v", refreshErr)
				}
				again, e := probe.LookupOwnedRetirementForRun(ctx, probe.child)
				if e != nil || kernel.ManagedDigest(again) != kernel.ManagedDigest(receipt) {
					t.Fatalf("native receipt changed on recovery: %v", e)
				}
				return
			case "unknown-before-create":
				if !errors.Is(err, errNativeChildCreateInterrupted) || registered || childCalls != 0 {
					t.Fatalf("original pre-Create fault: %v", err)
				}
				snapshot, e := session.Load(ctx, "binding")
				if e != nil || len(snapshot.Subtasks) != 1 {
					t.Fatalf("original frozen subtask missing: %v %+v", e, snapshot.Subtasks)
				}
				task := snapshot.Subtasks[0]
				stopped, e := session.CancelSubtask(ctx, "binding", task.ID, "stop uncreated child")
				if e != nil || len(stopped.Subtasks) != 1 || stopped.Subtasks[0].Status != "cancelled" {
					t.Fatalf("native uncreated cancellation: %v %+v", e, stopped.Subtasks)
				}
				d, e := probe.LoadCreationDecision(ctx, probe.child)
				if e != nil || d.Status != kernel.RunStatusCancelled || d.LogicalParentRunID != probe.record.Run.LogicalParentRunID || d.OriginFingerprint == "" {
					t.Fatalf("negative child identity missing: %+v %v", d, e)
				}
				if _, e = probe.nativeAuthorityKernelPorts.Create(ctx, probe.record, probe.events); !errors.Is(e, kernel.ErrCreationCancelled) {
					t.Fatalf("negative child admitted delayed Create: %v", e)
				}
				after, e := probe.LoadBudget(ctx, probe.scope)
				if e != nil || !after.RunCancelled(probe.child) || after.View("").Usage.ChildRuns != before.View("").Usage.ChildRuns {
					t.Fatalf("negative child fabricated accounting: %v %+v", e, after)
				}
				if _, ok := after.Runs[probe.child]; ok {
					t.Fatal("negative child registered")
				}
				if _, e = probe.Load(ctx, probe.child); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("negative child Run exists: %v", e)
				}
				if _, e = probe.LookupChildExecution(ctx, probe.child); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("negative child Grant exists: %v", e)
				}
				root, ok := harness.TopLevelInvocation(stopped)
				if !ok {
					t.Fatal("missing original parent")
				}
				g, ge := probe.LookupChildExecution(ctx, root.ExecutionRefID)
				completion, ce := probe.LookupPhysicalCallbackReturn(ctx, g)
				if ge != nil || ce != nil || !completion.HostCompleted || completion.GrantID != g.ID {
					t.Fatalf("original parent callback not returned: %v / %v", ge, ce)
				}
				calls := len(client.requests)
				repaired, e := session.Resume(ctx, "binding")
				if e != nil || repaired.Turn.Status != harness.TurnRunning || len(client.requests) != calls || probe.creates != 1 {
					t.Fatalf("receipt-only parent recovery: %s %v", repaired.Turn.Status, e)
				}
				finished, e := session.Resume(ctx, "binding")
				if e != nil || finished.Turn.Status != harness.TurnCompleted || childCalls != 0 || probe.creates != 1 {
					t.Fatalf("parent could not consume native negative receipt: %s %v", finished.Turn.Status, e)
				}
				return
			case "unknown-after-create":
				if !errors.Is(err, errNativeChildCreateACKLost) || !registered || childCalls != 0 {
					t.Fatalf("unknown Create not fenced: %v calls=%d registered=%v", err, childCalls, registered)
				}
				_, _ = session.Resume(ctx, "binding")
				if probe.creates != 1 || childCalls != 0 {
					t.Fatal("unknown child was dispatched again")
				}
				return
			case "capacity-race":
				if err != nil || out.Turn.Status != harness.TurnRunning || registered || childCalls != 0 {
					t.Fatalf("capacity rejection not parked: %s %v registered=%v childCalls=%d", out.Turn.Status, err, registered, childCalls)
				}
				out, err = session.Resume(ctx, "binding")
				if probe.creates != 2 {
					t.Fatalf("same native child not retried: %v creates=%d", err, probe.creates)
				}
			case "exhausted-race":
				if err != nil || out.Turn.Status != harness.TurnCompleted || registered || childCalls != 0 {
					t.Fatalf("child denial not settled: %s %v registered=%v childCalls=%d", out.Turn.Status, err, registered, childCalls)
				}
				return
			}
			if err != nil || out.Turn.Status != harness.TurnCompleted || childCalls != 1 {
				t.Fatalf("native child did not complete: %s %v childCalls=%d", out.Turn.Status, err, childCalls)
			}
			after, e := probe.LoadBudget(ctx, probe.scope)
			binding, ok := after.Runs[probe.child]
			want := 1
			if mode == "capacity-race" {
				want = 2
			}
			if e != nil || !ok || binding.ParentRunID == "" || !after.FinishedRuns[probe.child] || after.View("").Usage.ChildRuns != want {
				t.Fatalf("child enrollment or retirement wrong: %+v %v", after, e)
			}
			items, e := f.Host.ListItems(ctx, probe.scope, 0, 1000)
			if e != nil {
				t.Fatal(e)
			}
			for _, item := range items {
				if item.RunID == probe.child && item.ParentItemID == harness.ExecutionDispatchParentItemID {
					t.Fatal("qualified native child appended Host dispatch")
				}
			}
			receipt, e := probe.LookupOwnedRetirementForRun(ctx, probe.child)
			if e != nil || !receipt.Valid() || !strings.HasPrefix(receipt.Evidence.Grant.ID, "owneddispatch_native_") ||
				receipt.Evidence.Grant.AdmissionOperation != "start" || !receipt.Evidence.Completion.HostCompleted ||
				receipt.Evidence.Snapshot.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("exact native child retirement missing: %v", e)
			}
		})
	}
}
