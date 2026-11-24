package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The standard Session must exercise the same frozen Skill Context as an
// explicitly assembled Runner, including after a repeated admission.
func TestAgentSessionProjectsSelectedSkillInstructions(t *testing.T) {
	const needed = "Check signed release artifacts"
	const other = "Sensitive alternate selected skill content"
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		for _, message := range request.Messages {
			if strings.Contains(model.TextValue(message.Content), other) {
				t.Fatal("unread Skill content leaked into the model prompt")
			}
		}
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				if !strings.Contains(model.TextValue(message.Content), needed) {
					t.Fatalf("exact frozen Skill content was not returned: %+v", message)
				}
				return model.Response{Content: "release verified"}, nil
			}
		}
		foundManifest, foundReader := false, false
		for _, message := range request.Messages {
			if message.Role == model.RoleSystem && strings.Contains(model.TextValue(message.Content), "release-review") &&
				!strings.Contains(model.TextValue(message.Content), needed) {
				foundManifest = true
			}
		}
		for _, definition := range request.Tools {
			if definition.Key == harness.SkillReadToolKey {
				foundReader = true
			}
		}
		if !foundManifest || !foundReader {
			t.Fatalf("Skill manifest and read Tool were not frozen into model request: manifest=%t reader=%t", foundManifest, foundReader)
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "skill-read-1", ToolKey: harness.SkillReadToolKey,
			Arguments: json.RawMessage(`{"id":"release-review"}`)}}}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.Session.Config.Skills = []harness.SkillSnapshot{
		{ID: "release-review", Revision: 2, Title: "Release review", Markdown: needed},
		{ID: "unrequested", Revision: 1, Title: "Unrequested instructions", Markdown: other},
	}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := session.Prompt(t.Context(), "release-1", "Review the release")
	if err != nil || completed.Turn.Status != harness.TurnCompleted || len(client.requests) != 2 {
		t.Fatalf("selected Skill execution: status=%s err=%v requests=%d", completed.Turn.Status, err, len(client.requests))
	}
	if _, err := session.Prompt(t.Context(), "release-1", "Review the release"); err != nil || len(client.requests) != 2 {
		t.Fatalf("idempotent Skill replay caused a new model effect: %v", err)
	}
}

func TestAgentSessionToolApprovalAndOwnerBoundResolution(t *testing.T) {
	runStore := memory.NewStore()
	store := harness.NewMemoryStore()
	calls := 0
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "approved result"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "write-1", ToolKey: "write-record", Arguments: json.RawMessage(`{}`)}}}, nil
	}}
	options := agentSessionOptions(t, runStore, store, client)
	options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("write-record", &calls), Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write"}}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := session.Prompt(t.Context(), "write-1", "Write a record")
	if err != nil || pending.Turn.Status != harness.TurnWaitingInput || calls != 0 {
		t.Fatalf("approval was not enforced before Tool effect: status=%s err=%v calls=%d", pending.Turn.Status, err, calls)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing frozen root")
	}
	run, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || run.Checkpoint == nil || run.Checkpoint.Status != kernel.CheckpointPending {
		t.Fatalf("missing pending checkpoint: %v", err)
	}
	request := harness.ResolveApprovalRequest{CheckpointID: run.Checkpoint.ID, Decision: harness.ApprovalApprove}
	impostor := options
	impostor.Session.Actor.ActorID = "foreign-actor"
	foreign, err := harness.OpenAgentSession(impostor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.ResolveApproval(t.Context(), "write-1", request); !errors.Is(err, harness.ErrConflict) || calls != 0 {
		t.Fatalf("foreign Session approved original effect: %v calls=%d", err, calls)
	}
	done, err := session.ResolveApproval(t.Context(), "write-1", request)
	if err != nil || done.Turn.Status != harness.TurnCompleted || calls != 1 || len(client.requests) != 2 {
		t.Fatalf("approval resolution status=%s err=%v tools=%d models=%d", done.Turn.Status, err, calls, len(client.requests))
	}
	if _, err := session.ResolveApproval(context.Background(), "write-1", request); err == nil || calls != 1 {
		t.Fatalf("replayed approval dispatched an effect: %v tools=%d", err, calls)
	}
}

// A named specialist is still an Agent, not a second execution class. The
// Host explicitly supplies its child runner, indexed relations and policy;
// only the standard Session composition is implicit.
func TestAgentSessionDelegatesAuthorizedSubAgentWithoutAnotherInterpreter(t *testing.T) {
	runs := memory.NewStore()
	store := harness.NewMemoryStore()
	options := agentSessionOptions(t, runs, store, &agentSessionModel{})
	childCalls := 0
	childModel := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		childCalls++
		if request.Model != "specialist-model" {
			t.Fatalf("foreign model selected for child: %q", request.Model)
		}
		return model.Response{Content: "specialist evidence"}, nil
	}}
	child, err := agent.NewRunner(agent.Dependencies{Runtime: options.Runtime, Model: childModel})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := handoff.New(child)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	parentCalls := 0
	options.Model = &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		parentCalls++
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				if !strings.Contains(model.TextValue(message.Content), "specialist evidence") {
					t.Fatalf("parent did not receive child receipt: %+v", message)
				}
				return model.Response{Content: "combined specialist evidence"}, nil
			}
		}
		found := false
		for _, definition := range request.Tools {
			if definition.Key == harness.DelegationToolKey {
				found = true
			}
		}
		if !found {
			t.Fatal("explicit delegation Tool not available in parent Agent")
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-1", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"specialist","goal":"Find evidence"}`)}}}, nil
	}}
	options.Handoffs = routes
	options.Relations = relations
	options.DelegationPolicy = hostSelectedAgentPolicy{MemberID: "specialist", MemberRevision: "v1", Agent: agent.StartRequest{Model: "specialist-model"}}
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := session.Prompt(t.Context(), "delegate-1", "Collect specialist evidence")
	if err != nil || done.Turn.Status != harness.TurnCompleted || childCalls != 1 || parentCalls != 2 || len(done.Subtasks) != 1 {
		t.Fatalf("subagent session: status=%s err=%v childCalls=%d parentCalls=%d subtasks=%v", done.Turn.Status, err, childCalls, parentCalls, done.Subtasks)
	}
	inv, ok := harness.TopLevelInvocation(done)
	if !ok {
		t.Fatal("missing root")
	}
	children, err := relations.ListChildren(t.Context(), inv.ExecutionRefID)
	if err != nil || len(children) != 1 {
		t.Fatalf("missing original delegated relation: %+v err=%v", children, err)
	}
	// A reopened Session must resume against the original Host-selected route.
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.Resume(t.Context(), "delegate-1")
	if err != nil || again.Turn.Status != harness.TurnCompleted || childCalls != 1 {
		t.Fatalf("delegated recovery: status=%s err=%v childCalls=%d", again.Turn.Status, err, childCalls)
	}
	withoutRoute := options
	withoutRoute.Handoffs, withoutRoute.Relations, withoutRoute.DelegationPolicy = nil, nil, nil
	if _, err := harness.OpenAgentSession(withoutRoute); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("unconfigured delegate accepted: %v", err)
	}
	// A newly constructed minimal handle cannot resume a delegated original
	// Turn after losing the original Host route and delegation policy.
	withoutRoute.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{}
	bare, err := harness.OpenAgentSession(withoutRoute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Resume(t.Context(), "delegate-1"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("minimal handle resumed original delegated Turn: %v", err)
	}
}

// Standard Session uses the same original Harness ledger as an explicitly
// composed Runner. The local Tool and both Model turns charge it exactly once.
func TestAgentSessionSharedBudgetAccountsOriginalModelAndToolEffects(t *testing.T) {
	runStore := memory.NewStore()
	store := harness.NewMemoryStore()
	calls := 0
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "budgeted result"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "read-1", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
	}}
	options := agentSessionOptions(t, runStore, store, client)
	addAgentSessionTool(&options, "lookup", &calls)
	options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := session.Prompt(t.Context(), "budget-1", "Read one fact")
	if err != nil || done.Turn.Status != harness.TurnCompleted || calls != 1 || len(client.requests) != 2 {
		t.Fatalf("standard shared budget: status=%s err=%v tool=%d model=%d", done.Turn.Status, err, calls, len(client.requests))
	}
	ledger, err := store.LoadBudget(t.Context(), done.Turn.ID)
	if err != nil || ledger.View("").Usage.LLMCalls != 2 || ledger.View("").Usage.ToolCalls != 1 {
		t.Fatalf("accounting was not durable: %+v err=%v", ledger.View("").Usage, err)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.Resume(t.Context(), "budget-1")
	if err != nil || again.Turn.Status != harness.TurnCompleted || calls != 1 || len(client.requests) != 2 {
		t.Fatalf("reopen charged effects again: status=%s err=%v", again.Turn.Status, err)
	}
	withoutBudget := options
	withoutBudget.Session.Config.SharedBudget = nil
	unsafe, err := harness.OpenAgentSession(withoutBudget)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unsafe.Resume(t.Context(), "budget-1"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("unmetered handle resumed metered original: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*harness.AgentSessionOptions)
	}{
		{"original shared limit changed", func(o *harness.AgentSessionOptions) {
			copy := *o.Session.Config.SharedBudget
			copy.MaxLLMCalls++
			o.Session.Config.SharedBudget = &copy
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := options
			tc.mutate(&changed)
			other, err := harness.OpenAgentSession(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := other.Resume(t.Context(), "budget-1"); !errors.Is(err, harness.ErrInvalidRequest) || calls != 1 || len(client.requests) != 2 {
				t.Fatalf("changed physical Budget topology continued original Run: err=%v tools=%d models=%d", err, calls, len(client.requests))
			}
		})
	}
}

// A specialist's selected Tool is available to exactly that Host-authorized
// Agent revision, not to its supervisor or another named Agent. The physical
// Tool handler and definition are the same concrete Tool instance.
func TestAgentSessionMemberOnlyToolsAreNotOfferedToRoot(t *testing.T) {
	const privateKey = "specialist.private_read"
	runStore, store := memory.NewStore(), harness.NewMemoryStore()
	options := agentSessionOptions(t, runStore, store, &agentSessionModel{})
	calls := 0
	registration := agentSessionTool(privateKey, &calls)
	private := harness.SessionTool{Registration: registration, Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never", SideEffectLevel: "read",
	}}
	memberRegistry, err := tools.NewRegistry([]tools.Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	childCalls := 0
	childModel := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		childCalls++
		if request.Model != "specialist-model" || len(request.Tools) != 1 || request.Tools[0].Key != privateKey {
			t.Fatalf("specialist received wrong model/tool grant: model=%q tools=%+v", request.Model, request.Tools)
		}
		for _, m := range request.Messages {
			if m.Role == model.RoleTool {
				return model.Response{Content: "private evidence verified"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "private-tool-1", ToolKey: privateKey, Arguments: json.RawMessage(`{}`)}}}, nil
	}}
	child, err := agent.NewRunner(agent.Dependencies{Runtime: options.Runtime, Model: childModel, Catalog: memberRegistry, Executor: memberRegistry})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := handoff.New(child)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Model = &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		if len(request.Tools) != 1 || request.Tools[0].Key != harness.DelegationToolKey {
			t.Fatalf("root inherited specialist-private Tool: %+v", request.Tools)
		}
		for _, m := range request.Messages {
			if m.Role == model.RoleTool {
				return model.Response{Content: "review complete"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-specialist", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"specialist","goal":"Read the secret evidence"}`)}}}, nil
	}}
	options.Handoffs, options.Relations = routes, relations
	options.DelegationPolicy = hostSelectedAgentPolicy{MemberID: "specialist", MemberRevision: "v3", Agent: agent.StartRequest{Model: "specialist-model", ToolKeys: []string{privateKey}}}
	options.MemberTools = []harness.SessionTool{private}
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1, LocalToolGrants: []harness.DelegatedLocalToolGrant{{MemberID: "specialist", MemberRevision: "v3", ToolKey: privateKey}}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := session.Prompt(t.Context(), "member-1", "Ask the specialist")
	if err != nil || completed.Turn.Status != harness.TurnCompleted || calls != 1 || childCalls != 2 {
		t.Fatalf("member Tool task: status=%s err=%v calls=%d childSteps=%d", completed.Turn.Status, err, calls, childCalls)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := reopened.Resume(t.Context(), "member-1")
	if err != nil || done.Turn.Status != harness.TurnCompleted || calls != 1 {
		t.Fatalf("member Tool recovery replayed effects: status=%s err=%v calls=%d", done.Turn.Status, err, calls)
	}
	withoutPrivateTool := options
	withoutPrivateTool.MemberTools = nil
	if _, err := harness.OpenAgentSession(withoutPrivateTool); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("member grant without registered Tool was admitted: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*harness.AgentSessionOptions)
	}{
		{"private definition changed", func(o *harness.AgentSessionOptions) { o.MemberTools[0].Policy.DefinitionVersion = "v2" }},
		{"original member revision changed", func(o *harness.AgentSessionOptions) {
			o.Session.Config.DelegationPolicy.LocalToolGrants[0].MemberRevision = "v4"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updated := options
			updated.MemberTools = append([]harness.SessionTool(nil), options.MemberTools...)
			updated.Session.Config = options.Session.Config
			updated.Session.Config.DelegationPolicy.LocalToolGrants = append([]harness.DelegatedLocalToolGrant(nil), options.Session.Config.DelegationPolicy.LocalToolGrants...)
			tc.change(&updated)
			changed, err := harness.OpenAgentSession(updated)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := changed.Resume(t.Context(), "member-1"); !errors.Is(err, harness.ErrInvalidRequest) || calls != 1 {
				t.Fatalf("changed member Tool policy resumed original physical task: err=%v tools=%d", err, calls)
			}
		})
	}
}

// No separate child Runner can escape the root's shared Ledger. The standard
// Session builds one native Agent for root and local specialists, with the
// same scoped Budget adapter and original Run relation authority.
func TestAgentSessionSharedBudgetCoversSelectedSubAgent(t *testing.T) {
	store := harness.NewMemoryStore()
	runs := memory.NewStore()
	options := agentSessionOptions(t, runs, store, &agentSessionModel{})
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	steps, children, privateCalls := 0, 0, 0
	const privateKey = "private.budgeted_read"
	options.MemberTools = []harness.SessionTool{{Registration: agentSessionTool(privateKey, &privateCalls), Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never", SideEffectLevel: "read",
	}}}
	options.Model = &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		steps++
		if request.Model == "specialist-model" {
			children++
			if children == 1 && (len(request.Tools) != 1 || request.Tools[0].Key != privateKey) {
				t.Fatalf("budgeted specialist's original Tool grant was wrong: %+v", request.Tools)
			}
			for _, m := range request.Messages {
				if m.Role == model.RoleTool {
					return model.Response{Content: "specialist proof"}, nil
				}
			}
			return model.Response{ToolCalls: []tools.Call{{ID: "specialist-read-1", ToolKey: privateKey, Arguments: json.RawMessage(`{}`)}}}, nil
		}
		for _, m := range request.Messages {
			if m.Role == model.RoleTool {
				return model.Response{Content: "collected specialist proof"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-1", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"specialist","goal":"Find a fact"}`)}}}, nil
	}}
	options.Relations = relations
	options.DelegationPolicy = hostSelectedAgentPolicy{MemberID: "specialist", MemberRevision: "v1", Agent: agent.StartRequest{Model: "specialist-model", ToolKeys: []string{privateKey}}}
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1, LocalToolGrants: []harness.DelegatedLocalToolGrant{{MemberID: "specialist", MemberRevision: "v1", ToolKey: privateKey}}}
	options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 2, MaxChildRuns: 1, MaxConcurrentRuns: 2}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	done, err := session.Prompt(t.Context(), "budget-child-1", "Ask the specialist")
	if err != nil || done.Turn.Status != harness.TurnCompleted || steps != 4 || children != 2 || privateCalls != 1 {
		t.Fatalf("budgeted subagent: status=%s err=%v modelSteps=%d children=%d", done.Turn.Status, err, steps, children)
	}
	ledger, err := store.LoadBudget(t.Context(), done.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	usage := ledger.View("").Usage
	if usage.LLMCalls != 4 || usage.ToolCalls != 2 || usage.ChildRuns != 1 {
		t.Fatalf("specialist model/tool effects did not join root ledger: %+v", usage)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.Resume(t.Context(), "budget-child-1")
	if err != nil || again.Turn.Status != harness.TurnCompleted || steps != 4 || privateCalls != 1 {
		t.Fatalf("budgeted child restart changed physical execution: status=%s err=%v steps=%d", again.Turn.Status, err, steps)
	}
	foreign := options
	foreign.Handoffs, _ = handoff.New(&unmeteredTestChild{runtime: options.Runtime})
	if _, err := harness.OpenAgentSession(foreign); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("unmetered foreign child route admitted: %v", err)
	}
}

type unmeteredTestChild struct{ runtime *kernel.Runtime }

func (child *unmeteredTestChild) StartRun(_ context.Context, _ agent.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, nil
}
func (child *unmeteredTestChild) LoadRun(_ context.Context, _ string) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, nil
}

func TestAgentSessionLifecycleControlsRequireOriginalActor(t *testing.T) {
	store := harness.NewMemoryStore()
	toolCalls := 0
	options := agentSessionOptions(t, memory.NewStore(), store, &agentSessionModel{generate: func(model.Request) (model.Response, error) {
		return model.Response{ToolCalls: []tools.Call{{ID: "control-tool", ToolKey: "requires-approval", Arguments: json.RawMessage(`{}`)}}}, nil
	}})
	options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("requires-approval", &toolCalls), Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write",
	}}}
	owner, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := owner.Prompt(t.Context(), "control-turn", "Approve one local action")
	if err != nil || wait.Turn.Status != harness.TurnWaitingInput || toolCalls != 0 {
		t.Fatalf("original wait: status=%s err=%v", wait.Turn.Status, err)
	}
	foreignOptions := options
	foreignOptions.Session.Actor.ActorID = "impostor"
	foreign, err := harness.OpenAgentSession(foreignOptions)
	if err != nil {
		t.Fatal(err)
	}
	// These would otherwise call the underlying Runner with a valid persisted
	// Turn ID. The Session must deny them before a physical effect/transition.
	actions := []struct {
		name   string
		invoke func() (harness.Snapshot, error)
	}{
		{"cancel", func() (harness.Snapshot, error) { return foreign.Cancel(t.Context(), "control-turn", "impostor") }},
		{"retry", func() (harness.Snapshot, error) { return foreign.Retry(t.Context(), "control-turn") }},
		{"allowance", func() (harness.Snapshot, error) {
			return foreign.GrantCallAllowance(t.Context(), "control-turn", wait.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
		}},
		{"child-approval", func() (harness.Snapshot, error) {
			return foreign.ResolveSubtaskApproval(t.Context(), "control-turn", "child", "checkpoint", harness.ResolveApprovalRequest{})
		}},
		{"child-cancel", func() (harness.Snapshot, error) {
			return foreign.CancelSubtask(t.Context(), "control-turn", "child", "impostor")
		}},
	}
	for _, test := range actions {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.invoke(); !errors.Is(err, harness.ErrConflict) || toolCalls != 0 {
				t.Fatalf("foreign control crossed owner proof: err=%v tools=%d", err, toolCalls)
			}
		})
	}
	done, err := owner.Cancel(t.Context(), "control-turn", "original owner stop")
	if err != nil || done.Turn.Status != harness.TurnCancelled || toolCalls != 0 {
		t.Fatalf("original protective stop: status=%s err=%v calls=%d", done.Turn.Status, err, toolCalls)
	}
	retried, err := owner.Retry(t.Context(), "control-turn")
	if err != nil || retried.Turn.Status != harness.TurnWaitingInput || toolCalls != 0 {
		t.Fatalf("original owner retry: status=%s err=%v calls=%d", retried.Turn.Status, err, toolCalls)
	}
}

// Token ceilings require a selected, authoritative planner; the standard
// Session must never fall through to a metering-free physical Model request.
func TestAgentSessionTokenBudgetWithoutMeterDoesNotDispatch(t *testing.T) {
	client := &agentSessionModel{}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2, MaxInputTokens: 128, MaxOutputTokens: 128}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Prompt(t.Context(), "unmetered-1", "Request a token-priced operation")
	if err == nil || len(client.requests) != 0 {
		t.Fatalf("token-priced task entered Provider without certified metering: err=%v calls=%d", err, len(client.requests))
	}
}

// Session protective controls must authenticate the frozen Actor without
// treating auxiliary presentation metadata as a prerequisite to Kernel stop.
type damagedSessionOutputStore struct {
	harness.Store
	fail bool
}

func (store *damagedSessionOutputStore) GetInvocation(ctx context.Context, id string) (harness.Invocation, error) {
	value, err := store.Store.GetInvocation(ctx, id)
	if err == nil && store.fail && value.ParentItemID == "" {
		value.OutputRefs = []harness.HostRef{{Kind: "", ID: ""}}
	}
	return value, err
}
func (store *damagedSessionOutputStore) ListInvocations(ctx context.Context, turnID string) ([]harness.Invocation, error) {
	values, err := store.Store.ListInvocations(ctx, turnID)
	if err == nil && store.fail {
		for i := range values {
			if values[i].ParentItemID == "" {
				values[i].OutputRefs = []harness.HostRef{{Kind: "", ID: ""}}
			}
		}
	}
	return values, err
}

func TestAgentSessionProtectiveCancelIgnoresOutputMetadataButNotActor(t *testing.T) {
	store := &damagedSessionOutputStore{Store: harness.NewMemoryStore()}
	calls := 0
	options := agentSessionOptions(t, memory.NewStore(), store, &agentSessionModel{generate: func(model.Request) (model.Response, error) {
		return model.Response{ToolCalls: []tools.Call{{ID: "never-write", ToolKey: "write", Arguments: json.RawMessage(`{}`)}}}, nil
	}})
	options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("write", &calls), Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write",
	}}}
	original, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := original.Prompt(t.Context(), "damaged-1", "Approve the write")
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput {
		t.Fatalf("wait: status=%s err=%v", waiting.Turn.Status, err)
	}
	root, ok := harness.TopLevelInvocation(waiting)
	if !ok {
		t.Fatal("no original Run")
	}
	store.fail = true
	foreignOptions := options
	foreignOptions.Session.Actor.ActorID = "foreign"
	foreign, err := harness.OpenAgentSession(foreignOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Cancel(t.Context(), "damaged-1", "foreign stop"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("foreign Actor could control corrupted original: %v", err)
	}
	before, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || before.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("foreign control changed Run: status=%s err=%v", before.Run.Status, err)
	}
	_, cancelErr := original.Cancel(t.Context(), "damaged-1", "authorized protective stop")
	after, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || after.Run.Status != kernel.RunStatusCancelled || calls != 0 {
		t.Fatalf("presentation metadata blocked original Kernel stop: cancelErr=%v physical=%s err=%v calls=%d", cancelErr, after.Run.Status, err, calls)
	}
}
