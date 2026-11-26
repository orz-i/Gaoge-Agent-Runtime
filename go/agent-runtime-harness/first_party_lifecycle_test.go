package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type lifecycleInputs struct {
	*agent.MemoryRunInputs
	reads map[string]int
}

func (q *lifecycleInputs) ReadRunInputs(ctx context.Context, run kernel.Run, after uint64, limit int) ([]agent.RunInput, error) {
	q.reads[run.ID]++
	return q.MemoryRunInputs.ReadRunInputs(ctx, run, after, limit)
}

func TestFirstPartyLifecycleNamedApprovalInputsAndBudget(t *testing.T) {
	for _, profile := range []string{"ordinary", "managed", "successor"} {
		for _, mode := range []string{"approve", "cancel child", "approve returned-cut", "approve unknown-cut"} {
			t.Run(profile+"/"+mode, func(t *testing.T) { exerciseFirstPartyLifecycle(t, mode, profile) })
		}
	}
}

func exerciseFirstPartyLifecycle(t *testing.T, mode, profile string) {
	const private = "specialist.commit"
	const steering = "include the verified child result"
	ctx := t.Context()
	clock := &managedObligationClock{now: time.Now().UTC()}
	base, store := memory.NewStoreWithExecutionClock(clock), harness.NewMemoryStore()
	var runs kernel.Store = base
	if profile == "successor" {
		runs = &managedObligationStore{Store: base, event: "agent.model_invocation.pending", count: 1}
	}
	queue, err := agent.NewMemoryRunInputs("lifecycle-inputs")
	if err != nil {
		t.Fatal(err)
	}
	input := &lifecycleInputs{MemoryRunInputs: queue, reads: map[string]int{}}
	rootCalls, childCalls, writes := 0, 0, 0
	client := &agentSessionModel{}
	options := agentSessionOptions(t, runs, store, client)
	if profile != "ordinary" {
		options.Runtime, err = kernel.New(kernel.Dependencies{Store: runs, ManagedRecoveryHost: store})
		if err != nil {
			t.Fatal(err)
		}
		options.Context.ManagedRecovery = true
	}
	client.generate = func(req model.Request) (model.Response, error) {
		if req.Model == "specialist-model" {
			childCalls++
			for _, m := range req.Messages {
				if strings.Contains(model.TextValue(m.Content), steering) {
					t.Fatal("root steering leaked into child")
				}
				if m.Role == model.RoleTool {
					return model.Response{Content: "verified child result"}, nil
				}
			}
			return model.Response{ToolCalls: []tools.Call{{ID: "approved-child-write", ToolKey: private, Arguments: json.RawMessage(`{}`)}}}, nil
		}
		rootCalls++
		if rootCalls == 1 {
			current, e := options.Runtime.Load(ctx, req.RunID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = queue.Submit(ctx, current.Run, "steering-1", steering); e != nil {
				t.Fatal(e)
			}
			return model.Response{ToolCalls: []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"specialist","goal":"perform approved write"}`)}}}, nil
		}
		inputCount, resultCount := 0, 0
		for _, m := range req.Messages {
			if m.Role == model.RoleUser && model.TextValue(m.Content) == steering {
				inputCount++
			}
			if m.Role == model.RoleTool && (mode != "cancel child" && strings.Contains(model.TextValue(m.Content), "verified child result") || mode == "cancel child" && strings.Contains(model.TextValue(m.Content), `"status":"cancelled"`)) {
				resultCount++
			}
		}
		if inputCount != 1 || resultCount != 1 {
			t.Fatalf("original input/result missing or duplicated: input=%d result=%d messages=%+v", inputCount, resultCount, req.Messages)
		}
		return model.Response{Content: "complete"}, nil
	}
	options.Inputs = input
	options.Relations, err = runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 2, MaxChildRuns: 1, MaxConcurrentRuns: 2}
	options.NamedAgents = []harness.NamedAgent{{MemberID: "specialist", MemberRevision: "v1", AgentSpec: harness.AgentSpec{Model: "specialist-model"}, Tools: []harness.SessionTool{{Registration: agentSessionTool(private, &writes), Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "write", ApprovalMode: "always", ApprovalCapability: "per_call"}}}}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	var pending harness.Snapshot
	if profile == "successor" {
		crashManagedObligation(t, func() { _, _ = session.Prompt(ctx, "full", "perform the original task") })
		options.Runtime, err = kernel.New(kernel.Dependencies{Store: base, ManagedRecoveryHost: store})
		if err != nil {
			t.Fatal(err)
		}
		options.Session.Transcript = nil
		session, err = harness.OpenAgentSession(options)
		if err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(25 * time.Hour)
		pending, err = session.Resume(ctx, "full")
	} else {
		pending, err = session.Prompt(ctx, "full", "perform the original task")
	}
	if err != nil || len(pending.Subtasks) != 1 || pending.Subtasks[0].Approval == nil || writes != 0 {
		t.Fatalf("original child approval missing: status=%s err=%v tasks=%+v writes=%d", pending.Turn.Status, err, pending.Subtasks, writes)
	}
	task := pending.Subtasks[0]
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing root")
	}
	child, err := options.Runtime.Load(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	childView, err := agent.ViewState(child)
	if err != nil {
		t.Fatal(err)
	}
	if childView.InputBinding != "" || input.reads[task.RunID] != 0 {
		t.Fatalf("child inherited Root input source: binding=%q reads=%d", childView.InputBinding, input.reads[task.RunID])
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := options.Runtime.VerifyBoundedToolControl(ctx, child); err != nil {
		t.Fatalf("original child approval return was not qualified: %v", err)
	}
	var approved harness.Snapshot
	wantChildCalls, wantWrites, wantLLM, wantTools := 2, 1, 4, 2
	if mode == "cancel child" {
		approved, err = reopened.CancelSubtask(ctx, "full", task.ID, "cancel before approval")
		if err != nil || len(approved.Subtasks) != 1 || approved.Subtasks[0].Status != "cancelled" || writes != 0 {
			t.Fatalf("known child wait could not be cancelled: status=%s err=%v tasks=%+v writes=%d", approved.Turn.Status, err, approved.Subtasks, writes)
		}
		if _, err := reopened.ResolveSubtaskApproval(ctx, "full", task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}); err == nil || writes != 0 {
			t.Fatal("cancelled child accepted stale approval")
		}
		wantChildCalls, wantWrites, wantLLM, wantTools = 1, 0, 3, 1
	} else {
		if mode == "approve returned-cut" || mode == "approve unknown-cut" {
			cut := &firstPartyChildReturnCut{Store: base, before: mode == "approve unknown-cut"}
			var host kernel.ManagedRecoveryHostStore
			if profile != "ordinary" {
				host = store
			}
			options.Runtime, err = kernel.New(kernel.Dependencies{Store: cut, ManagedRecoveryHost: host})
			if err != nil {
				t.Fatal(err)
			}
			reopened, err = harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			crashManagedObligation(t, func() {
				_, _ = reopened.ResolveSubtaskApproval(ctx, "full", task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
			})
			if writes != 1 || cut.grant.ChildRunID != task.RunID {
				t.Fatal("child return cut missed")
			}
			options.Runtime, err = kernel.New(kernel.Dependencies{Store: base, ManagedRecoveryHost: host})
			if err != nil {
				t.Fatal(err)
			}
			reopened, err = harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			approved, err = reopened.Refresh(ctx, "full")
			if mode == "approve unknown-cut" {
				if !errors.Is(err, harness.ErrExecutionOutcomeUnknown) {
					t.Fatalf("unknown child projected: %v", err)
				}
				if _, err = reopened.Resume(ctx, "full"); !errors.Is(err, harness.ErrExecutionOutcomeUnknown) {
					t.Fatalf("unknown child continued parent: %v", err)
				}
				if rootCalls != 1 || childCalls != 2 || writes != 1 {
					t.Fatal("unknown child reentered or advanced parent")
				}
				if g, e := base.LookupChildExecution(ctx, task.RunID); e != nil || g != cut.grant {
					t.Fatalf("unknown child fence lost: %v", e)
				}
				facts, e := store.ListItems(ctx, pending.Turn.ID, 0, 1000)
				if e != nil {
					t.Fatal(e)
				}
				for _, i := range facts {
					if i.Kind == harness.ItemApproval && i.Status == harness.ItemCompleted && i.RunID == task.RunID {
						t.Fatal("unknown child callback fabricated approval closure")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		} else {
			approved, err = reopened.ResolveSubtaskApproval(ctx, "full", task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
			if err != nil || writes != 1 {
				t.Fatalf("original child approval did not finish: status=%s err=%v tasks=%+v writes=%d", approved.Turn.Status, err, approved.Subtasks, writes)
			}
		}
	}
	for i := 0; i < 4 && approved.Turn.Status != harness.TurnCompleted; i++ {
		approved, err = reopened.Resume(ctx, "full")
		if err != nil {
			t.Fatalf("reopen could not continue original parent: status=%s err=%v", approved.Turn.Status, err)
		}
	}
	facts, e := store.ListItems(ctx, approved.Turn.ID, 0, 1000)
	if e != nil {
		t.Fatal(e)
	}
	decisions := 0
	for _, i := range facts {
		if i.Kind != harness.ItemApproval || i.Status != harness.ItemCompleted || i.RunID != task.RunID {
			continue
		}
		var decision struct {
			Subtask                         bool
			CheckpointID, Decision, Comment string
		}
		if json.Unmarshal(i.Payload, &decision) != nil || !decision.Subtask || decision.CheckpointID != task.Approval.CheckpointID || decision.Decision != "approve" || decision.Comment != "" {
			t.Fatalf("child decision lost: %s", i.Payload)
		}
		decisions++
	}
	wantDecisions := 1
	if mode == "cancel child" {
		wantDecisions = 0
	}
	if decisions != wantDecisions {
		t.Fatalf("child approval decision count=%d want=%d", decisions, wantDecisions)
	}
	if input.reads[task.RunID] != 0 {
		t.Fatal("child read supervisor input during resumed approval")
	}
	if approved.Turn.Status != harness.TurnCompleted || rootCalls != 2 || childCalls != wantChildCalls || writes != wantWrites {
		t.Fatalf("complete original task: status=%s calls=%d/%d writes=%d", approved.Turn.Status, rootCalls, childCalls, writes)
	}
	final, err := options.Runtime.Load(ctx, root.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(final)
	if err != nil || view.InputCursor != 1 {
		t.Fatalf("input cursor: %d %v", view.InputCursor, err)
	}
	l, err := store.LoadBudget(ctx, approved.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u := l.View("").Usage; u.LLMCalls != wantLLM || u.ToolCalls != wantTools || u.ChildRuns != 1 {
		t.Fatalf("unexpected shared accounting: %+v", u)
	}
}

type firstPartyChildReturnCut struct {
	*memory.Store
	grant  kernel.ChildExecutionGrant
	before bool
}

func (s *firstPartyChildReturnCut) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if s.before && g.AdmissionOperation == "approval" && o.HostCompleted {
		s.grant = g
		panic(managedObligationCrash{})
	}
	err := s.Store.RecordOwnedPhysicalCallbackReturn(ctx, g, rev, o, p)
	if err == nil && g.AdmissionOperation == "approval" && o.HostCompleted {
		s.grant = g
		panic(managedObligationCrash{})
	}
	return err
}
