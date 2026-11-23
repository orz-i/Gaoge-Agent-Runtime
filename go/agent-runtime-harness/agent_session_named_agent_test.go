package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestNamedAgentCarriesOwnModelToolsSkillsAndFrozenAuthorization(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "shared-budget"}[shared], func(t *testing.T) {
			const rootSecret = "root-private-instructions-DO-NOT-READ"
			const childSecret = "child-private-instructions-ONLY-CHILD"
			const otherSecret = "other-member-instructions-NEVER-READ"
			const privateKey = "specialist.verify"
			childModelCalls, rootModelCalls, privateCalls := 0, 0, 0
			selected := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
				hasTool := func(key string) bool {
					for _, d := range r.Tools {
						if d.Key == key {
							return true
						}
					}
					return false
				}
				hasText := func(value string) bool {
					for _, m := range r.Messages {
						if strings.Contains(model.TextValue(m.Content), value) {
							return true
						}
					}
					return false
				}
				if r.Model == "specialist-model" {
					childModelCalls++
					if !hasTool(privateKey) || !hasTool(harness.SkillReadToolKey) || hasTool("root-only-tool") {
						t.Fatalf("child model lost its tool/skill authorization: %+v", r.Tools)
					}
					if !hasText("child-skill") || hasText("root-skill") || hasText("other-skill") || hasText(rootSecret) || hasText(otherSecret) {
						t.Fatal("child saw missing/foreign Skill manifest or leaked root/foreign Markdown")
					}
					for _, message := range r.Messages {
						if message.Role == model.RoleSystem && strings.Contains(model.TextValue(message.Content), childSecret) {
							t.Fatal("child Skill Markdown leaked into system prompt")
						}
					}
					if hasText(`"evidence":"verified"`) {
						return model.Response{Content: "specialist completed"}, nil
					}
					if hasText(childSecret) {
						return model.Response{ToolCalls: []tools.Call{{ID: "private-call", ToolKey: privateKey, Arguments: json.RawMessage(`{}`)}}}, nil
					}
					return model.Response{ToolCalls: []tools.Call{{ID: "skill-read", ToolKey: harness.SkillReadToolKey, Arguments: json.RawMessage(`{"id":"shared-skill"}`)}}}, nil
				}
				rootModelCalls++
				if !hasTool("root-only-tool") || hasTool(privateKey) || hasText("child-skill") || hasText(otherSecret) || hasText(childSecret) {
					t.Fatal("root inherited child-only Tool or Skill content")
				}
				for _, m := range r.Messages {
					if m.Role == model.RoleTool {
						return model.Response{Content: "root combined result"}, nil
					}
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "delegate-named", ToolKey: harness.DelegationToolKey,
					Arguments: json.RawMessage(`{"memberID":"specialist","goal":"Inspect signed evidence"}`)}}}, nil
			}}
			host := harness.NewMemoryStore()
			options := agentSessionOptions(t, memory.NewStore(), host, selected)
			relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			options.Relations = relations
			rootOnlyCalls := 0
			addAgentSessionTool(&options, "root-only-tool", &rootOnlyCalls)
			options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
			options.Session.Config.Skills = []harness.SkillSnapshot{{ID: "shared-skill", Revision: 1, Title: "root-skill", Markdown: rootSecret}}
			options.NamedAgents = []harness.NamedAgent{
				{MemberID: "specialist", MemberRevision: "r7", Model: "specialist-model", Instructions: "Inspect only exact evidence",
					Skills: []harness.SkillSnapshot{{ID: "shared-skill", Revision: 7, Title: "child-skill", Markdown: childSecret}},
					Tools: []harness.SessionTool{{Registration: agentSessionTool(privateKey, &privateCalls),
						Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "read", ApprovalMode: "never"}}}},
				{MemberID: "other", MemberRevision: "r1", Model: "other-model", Skills: []harness.SkillSnapshot{{ID: "other-id", Revision: 1, Title: "other-skill", Markdown: otherSecret}}},
			}
			if shared {
				options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 4, MaxChildRuns: 1, MaxConcurrentRuns: 2}
			}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			done, err := session.Prompt(t.Context(), "named-skill-task", "Use the specialist")
			if err != nil || done.Turn.Status != harness.TurnCompleted || privateCalls != 1 || childModelCalls != 3 || rootModelCalls != 2 || rootOnlyCalls != 0 {
				t.Fatalf("frozen named Agent run: status=%s err=%v private=%d child=%d root=%d", done.Turn.Status, err, privateCalls, childModelCalls, rootModelCalls)
			}
			options.Session.Transcript = nil
			reopened, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.Resume(t.Context(), "named-skill-task"); err != nil || privateCalls != 1 || childModelCalls != 3 {
				t.Fatalf("restart replayed original specialist Tool/Skill: err=%v private=%d child=%d", err, privateCalls, childModelCalls)
			}
			for _, tc := range []struct {
				name   string
				mutate func(*harness.AgentSessionOptions)
			}{
				{"changed specialist model", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].Model = "changed-model" }},
				{"changed specialist revision", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].MemberRevision = "r8" }},
				{"changed specialist Skill content", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].Skills[0].Markdown = "foreign" }},
				{"changed specialist Tool approval", func(o *harness.AgentSessionOptions) {
					o.NamedAgents[0].Tools[0].Policy.ApprovalMode = "always"
					o.NamedAgents[0].Tools[0].Policy.ApprovalCapability = "per_call"
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					changed := options
					changed.NamedAgents = append([]harness.NamedAgent(nil), options.NamedAgents...)
					changed.NamedAgents[0].Skills = append([]harness.SkillSnapshot(nil), changed.NamedAgents[0].Skills...)
					changed.NamedAgents[0].Tools = append([]harness.SessionTool(nil), changed.NamedAgents[0].Tools...)
					tc.mutate(&changed)
					restarted, e := harness.OpenAgentSession(changed)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = restarted.Resume(t.Context(), "named-skill-task"); !errors.Is(e, harness.ErrInvalidRequest) {
						t.Fatalf("mutated named Agent resumed: %v", e)
					}
				})
			}
		})
	}
}

// A child without root Skills must still have its own reader. It cannot
// fetch another selected member's Markdown by guessing the globally unique ID.
func TestNamedAgentSkillReaderDeniesOtherMemberWithoutRootGrant(t *testing.T) {
	childCalls, rootCalls := 0, 0
	const otherSecret = "foreign member private markdown"
	client := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
		if r.Model == "worker-model" {
			childCalls++
			if len(r.Tools) != 1 || r.Tools[0].Key != harness.SkillReadToolKey {
				t.Fatalf("child has wrong Skill reader authority: %+v", r.Tools)
			}
			for _, m := range r.Messages {
				if strings.Contains(model.TextValue(m.Content), otherSecret) {
					t.Fatal("foreign member Skill leaked to child")
				}
			}
			if childCalls == 1 {
				return model.Response{ToolCalls: []tools.Call{{ID: "unselected-skill-read", ToolKey: harness.SkillReadToolKey,
					Arguments: json.RawMessage(`{"id":"other-only"}`)}}}, nil
			}
			return model.Response{Content: "worker completed without foreign instructions"}, nil
		}
		rootCalls++
		for _, d := range r.Tools {
			if d.Key == harness.SkillReadToolKey {
				t.Fatal("root without Skills obtained member-only reader")
			}
		}
		for _, m := range r.Messages {
			if m.Role == model.RoleTool {
				return model.Response{Content: "received worker evidence"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "call-worker", ToolKey: harness.DelegationToolKey,
			Arguments: json.RawMessage(`{"memberID":"worker","goal":"read only authorized instructions"}`)}}}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Relations = relations
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	options.NamedAgents = []harness.NamedAgent{
		{MemberID: "worker", MemberRevision: "r1", Model: "worker-model", Skills: []harness.SkillSnapshot{{ID: "worker-only", Revision: 1, Title: "worker instructions", Markdown: "permitted worker facts"}}},
		{MemberID: "other", MemberRevision: "r1", Model: "other-model", Skills: []harness.SkillSnapshot{{ID: "other-only", Revision: 1, Title: "other instructions", Markdown: otherSecret}}},
	}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := session.Prompt(t.Context(), "member-skill-denial", "Use worker")
	if err != nil || snap.Turn.Status != harness.TurnCompleted || rootCalls != 2 || childCalls != 2 {
		t.Fatalf("member scope denial did not remain recoverable: status=%s err=%v root=%d child=%d", snap.Turn.Status, err, rootCalls, childCalls)
	}
}

type selectedNamedHostedCatalog struct{ version string }

func (c selectedNamedHostedCatalog) Resolve(_ context.Context, key, modelID string) (model.HostedTool, bool, error) {
	if key != "provider.search" || modelID != "hosted-specialist" {
		return model.HostedTool{}, false, nil
	}
	return model.HostedTool{Key: key, DefinitionVersion: c.version, Target: json.RawMessage(`{"search":"limited"}`)}, true, nil
}

func TestNamedAgentHostedToolGrantUsesNativeCatalogAndModelVersion(t *testing.T) {
	for _, tc := range []struct {
		name, actual string
		allowed      bool
	}{{"granted", "v3", true}, {"stale-version", "v4", false}} {
		t.Run(tc.name, func(t *testing.T) {
			rootCalls, childCalls := 0, 0
			client := &agentSessionModel{generate: func(r model.Request) (model.Response, error) {
				if r.Model == "hosted-specialist" {
					childCalls++
					if len(r.HostedTools) != 1 || r.HostedTools[0].Key != "provider.search" || r.HostedTools[0].DefinitionVersion != "v3" || len(r.Tools) != 0 {
						t.Fatalf("provider-hosted grant was not isolated and exact: %+v", r)
					}
					return model.Response{Content: "provider selection frozen"}, nil
				}
				rootCalls++
				if len(r.HostedTools) != 0 {
					t.Fatalf("root inherited private provider Hosted Tool: %+v", r.HostedTools)
				}
				for _, m := range r.Messages {
					if m.Role == model.RoleTool {
						return model.Response{Content: "complete"}, nil
					}
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "hosted-delegate", ToolKey: harness.DelegationToolKey,
					Arguments: json.RawMessage(`{"memberID":"hosted","goal":"inspect current evidence"}`)}}}, nil
			}}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			options.Relations = rel
			options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
			options.HostedTools = selectedNamedHostedCatalog{version: tc.actual}
			options.NamedAgents = []harness.NamedAgent{{MemberID: "hosted", MemberRevision: "r1", Model: "hosted-specialist",
				HostedToolGrants: []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v3"}}}}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			snap, err := session.Prompt(t.Context(), "hosted-selection", "Choose the specialist")
			if tc.allowed {
				if err != nil || snap.Turn.Status != harness.TurnCompleted || rootCalls != 2 || childCalls != 1 {
					t.Fatalf("valid hosted named grant failed: status=%s err=%v root=%d child=%d", snap.Turn.Status, err, rootCalls, childCalls)
				}
			} else if childCalls != 0 || (err == nil && snap.Turn.Status == harness.TurnCompleted) {
				t.Fatalf("stale hosted definition executed: status=%s err=%v calls=%d", snap.Turn.Status, err, childCalls)
			}
		})
	}
}

func TestNamedAgentHostSelectionRejectsIncompleteOrConflictingAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*harness.AgentSessionOptions)
	}{
		{"missing model", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].Model = "" }},
		{"unknown Tool key", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].ToolKeys = []string{"private.unknown"} }},
		{"reserved Skill reader manually assigned", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].ToolKeys = []string{harness.SkillReadToolKey} }},
		{"duplicate selected member", func(o *harness.AgentSessionOptions) { o.NamedAgents = append(o.NamedAgents, o.NamedAgents[0]) }},
		{"different model output ceiling", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].MaxModelOutputTokens = -1 }},
		{"bad Skill content hash", func(o *harness.AgentSessionOptions) { o.NamedAgents[0].Skills[0].ContentHash = "forged" }},
		{"untrusted hosted declaration", func(o *harness.AgentSessionOptions) {
			o.NamedAgents[0].HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search"}}
		}},
		{"conflicting Host policy", func(o *harness.AgentSessionOptions) { o.DelegationPolicy = hostSelectedAgentPolicy{MemberID: "worker"} }},
		{"missing relation index", func(o *harness.AgentSessionOptions) { o.Relations = nil }},
		{"missing delegation depth", func(o *harness.AgentSessionOptions) { o.Session.Config.DelegationPolicy.MaxDepth = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			options.Relations = relations
			options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
			options.NamedAgents = []harness.NamedAgent{{MemberID: "worker", MemberRevision: "r1", Model: "worker-model",
				Skills: []harness.SkillSnapshot{{ID: "worker", Revision: 1, Title: "Worker", Markdown: "original"}}}}
			tc.mutate(&options)
			if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("invalid Host Agent composition admitted: %v", err)
			}
			if len(client.requests) != 0 {
				t.Fatal("rejected Named Agent issued a model call")
			}
		})
	}
}

// A named Agent's private write Tool uses the existing frozen per-call
// approval. Neither root visibility nor a foreign Host Session can authorize
// its physical effect. The original child execution/receipt is not replayed.
func TestNamedAgentPrivateWriteRequiresExactChildApprovalAndOriginalHost(t *testing.T) {
	writeCalls, childModelCalls, rootModelCalls := 0, 0, 0
	const privateWrite = "specialist.commit"
	client := &agentSessionModel{generate: func(req model.Request) (model.Response, error) {
		if req.Model == "writer-model" {
			childModelCalls++
			if len(req.Tools) != 1 || req.Tools[0].Key != privateWrite {
				t.Fatalf("child received wrong write authority: %+v", req.Tools)
			}
			for _, message := range req.Messages {
				if message.Role == model.RoleTool {
					return model.Response{Content: "write committed"}, nil
				}
			}
			return model.Response{ToolCalls: []tools.Call{{ID: "write-child-call", ToolKey: privateWrite, Arguments: json.RawMessage(`{}`)}}}, nil
		}
		rootModelCalls++
		for _, tool := range req.Tools {
			if tool.Key == privateWrite {
				t.Fatal("root inherited child-only write Tool")
			}
		}
		for _, message := range req.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "authorized task completed"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-child-write", ToolKey: harness.DelegationToolKey,
			Arguments: json.RawMessage(`{"memberID":"writer","goal":"commit approved write"}`)}}}, nil
	}}
	store := harness.NewMemoryStore()
	options := agentSessionOptions(t, memory.NewStore(), store, client)
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Relations = relations
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	options.NamedAgents = []harness.NamedAgent{{MemberID: "writer", MemberRevision: "r1", Model: "writer-model",
		Tools: []harness.SessionTool{{Registration: agentSessionTool(privateWrite, &writeCalls),
			Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "write", ApprovalMode: "always", ApprovalCapability: "per_call"}}}}}
	owner, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := owner.Prompt(t.Context(), "child-write", "Request an authorized write")
	if err != nil || writeCalls != 0 || len(pending.Subtasks) != 1 || pending.Subtasks[0].Approval == nil {
		t.Fatalf("missing delegated approval without side effects: status=%s err=%v subtasks=%+v writes=%d", pending.Turn.Status, err, pending.Subtasks, writeCalls)
	}
	task := pending.Subtasks[0]
	if task.MemberID != "writer" || task.MemberRevision != "r1" || task.Status != "waiting_input" {
		t.Fatalf("approval was issued to wrong Named Agent: %+v", task)
	}
	foreignOptions := options
	foreignOptions.NamedAgents = append([]harness.NamedAgent(nil), options.NamedAgents...)
	foreignOptions.Session.Actor.ActorID = "foreign"
	foreign, err := harness.OpenAgentSession(foreignOptions)
	if err != nil {
		t.Fatal(err)
	}
	decision := harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove}
	if _, err = foreign.ResolveSubtaskApproval(t.Context(), "child-write", task.ID, task.Approval.CheckpointID, decision); !errors.Is(err, harness.ErrConflict) || writeCalls != 0 {
		t.Fatalf("foreign Host authorized private write: %v calls=%d", err, writeCalls)
	}
	if _, err = owner.ResolveSubtaskApproval(t.Context(), "child-write", task.ID, "wrong-checkpoint", decision); !errors.Is(err, harness.ErrConflict) || writeCalls != 0 {
		t.Fatalf("stale approval authorized private write: %v calls=%d", err, writeCalls)
	}
	approved, err := owner.ResolveSubtaskApproval(t.Context(), "child-write", task.ID, task.Approval.CheckpointID, decision)
	if err != nil || writeCalls != 1 || len(approved.Subtasks) != 1 || approved.Subtasks[0].Status != "completed" {
		t.Fatalf("original Host approval did not commit one child Tool: err=%v writes=%d subtasks=%+v", err, writeCalls, approved.Subtasks)
	}
	if _, err = owner.ResolveSubtaskApproval(t.Context(), "child-write", task.ID, task.Approval.CheckpointID, decision); err == nil || writeCalls != 1 {
		t.Fatalf("old approval repeated physical write: %v calls=%d", err, writeCalls)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	// Only the original remaining root continuation may make progress. It
	// cannot redispatch the settled member Tool/approval after reopening.
	_, _ = reopened.Refresh(t.Context(), "child-write")
	if writeCalls != 1 || childModelCalls != 2 || rootModelCalls != 1 {
		t.Fatalf("read-only recovery reissued child effect: writes=%d child-model=%d root-model=%d", writeCalls, childModelCalls, rootModelCalls)
	}
}

// Tool discovery remains the original Agent's read-only search. It can reveal
// only member-authorized, frozen registrations and cannot grant a new Tool.
type namedDiscoveryPort struct {
	calls      int
	candidates int
	chosen     string
}

func (d *namedDiscoveryPort) Search(_ context.Context, request agent.ToolDiscoveryRequest) ([]string, error) {
	d.calls++
	d.candidates = len(request.Candidates)
	if request.Model != "discovery-model" || request.RunID == "" || request.Query != "find verified evidence" {
		return nil, agent.ErrToolDiscoveryInvalid
	}
	return []string{d.chosen}, nil
}

func TestNamedAgentToolDiscoveryIsLimitedToItsFrozenToolSet(t *testing.T) {
	const chosen = "specialist.read.02"
	executions := 0
	search := &namedDiscoveryPort{chosen: chosen}
	rootCalls, childCalls := 0, 0
	modelClient := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		if request.Model == "discovery-model" {
			childCalls++
			switch childCalls {
			case 1:
				if len(request.Tools) != 1 || request.Tools[0].Key != agent.ToolDiscoveryKey {
					t.Fatalf("child initial discovery did not defer schemas: %+v", request.Tools)
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "search-child", ToolKey: agent.ToolDiscoveryKey, Arguments: json.RawMessage(`{"query":"find verified evidence"}`)}}}, nil
			case 2:
				if len(request.Tools) != 2 || request.Tools[0].Key != agent.ToolDiscoveryKey && request.Tools[1].Key != agent.ToolDiscoveryKey {
					t.Fatalf("discovery did not preserve bounded search control: %+v", request.Tools)
				}
				seen := false
				for _, key := range request.Tools {
					if key.Key == chosen {
						seen = true
					}
				}
				if !seen {
					t.Fatalf("selected member tool was not discovered: %+v", request.Tools)
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "read-chosen", ToolKey: chosen, Arguments: json.RawMessage(`{}`)}}}, nil
			case 3:
				return model.Response{Content: "authorized evidence retrieved"}, nil
			default:
				t.Fatalf("unbounded child discovery execution: %d", childCalls)
			}
		}
		rootCalls++
		for _, tool := range request.Tools {
			if tool.Key == chosen || tool.Key == agent.ToolDiscoveryKey {
				t.Fatal("root inherited child-only discovery grant")
			}
		}
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "complete"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-search", ToolKey: harness.DelegationToolKey,
			Arguments: json.RawMessage(`{"memberID":"researcher","goal":"find verified evidence"}`)}}}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), modelClient)
	rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Relations = rel
	options.ToolDiscovery = search
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	member := harness.NamedAgent{MemberID: "researcher", MemberRevision: "v1", Model: "discovery-model"}
	for i := 0; i < 16; i++ {
		key := fmt.Sprintf("specialist.read.%02d", i)
		member.Tools = append(member.Tools, harness.SessionTool{Registration: agentSessionTool(key, &executions),
			Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "read", ApprovalMode: "never"}})
	}
	options.NamedAgents = []harness.NamedAgent{member}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := session.Prompt(t.Context(), "discover-named", "Search using named researcher")
	if err != nil || completed.Turn.Status != harness.TurnCompleted || search.calls != 1 || search.candidates != 16 || executions != 1 || childCalls != 3 || rootCalls != 2 {
		t.Fatalf("native member discovery failed: status=%s err=%v search=%d candidates=%d executes=%d child=%d root=%d", completed.Turn.Status, err, search.calls, search.candidates, executions, childCalls, rootCalls)
	}
}
