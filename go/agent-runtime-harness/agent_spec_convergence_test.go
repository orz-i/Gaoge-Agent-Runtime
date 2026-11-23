package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const specAnswerSchema = `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`

// The child output schema is a provider capability, not a prompt convention.
// Only the selected named model has a certified structured-output transport.
type agentSpecStructuredModel struct{ *agentSessionModel }

func (*agentSpecStructuredModel) Capabilities(_ context.Context, name string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: name == "specialist-schema"}, nil
}

func TestRootAgentSpecSelectsVersionedHostedToolLikeNamedAgent(t *testing.T) {
	for _, tc := range []struct {
		name, catalogVersion string
		allowed              bool
	}{
		{"exact-authorization", "v3", true},
		{"stale-version", "v4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
				calls++
				if request.Model != "hosted-specialist" || len(request.HostedTools) != 1 ||
					request.HostedTools[0].Key != "provider.search" || request.HostedTools[0].DefinitionVersion != "v3" || len(request.Tools) != 0 {
					t.Fatalf("Root AgentSpec leaked a different hosted/local capability: %+v", request)
				}
				return model.Response{Content: "done"}, nil
			}}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			options.Session.Config.Model = "hosted-specialist"
			options.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v3"}}
			options.HostedTools = selectedNamedHostedCatalog{version: tc.catalogVersion}
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := session.Prompt(t.Context(), "root-hosted-"+tc.name, "search")
			if tc.allowed {
				if err != nil || result.Turn.Status != harness.TurnCompleted || calls != 1 {
					t.Fatalf("Root valid hosted grant lost: err=%v status=%s calls=%d", err, result.Turn.Status, calls)
				}
				// A new Host configuration can never add or replace a paused
				// Agent's original native Hosted Tool authorization.
				options.Session.Transcript = nil
				options.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v4"}}
				options.HostedTools = selectedNamedHostedCatalog{version: "v4"}
				reopened, openErr := harness.OpenAgentSession(options)
				if openErr != nil {
					t.Fatal(openErr)
				}
				if _, replayErr := reopened.Resume(t.Context(), "root-hosted-"+tc.name); !errors.Is(replayErr, harness.ErrInvalidRequest) || calls != 1 {
					t.Fatalf("Root stale hosted authorization could resume: %v calls=%d", replayErr, calls)
				}
			} else if calls != 0 || (err == nil && result.Turn.Status == harness.TurnCompleted) {
				t.Fatalf("Root used unversioned/stale hosted Tool: status=%s err=%v calls=%d", result.Turn.Status, err, calls)
			}
		})
	}
}

func TestNamedAgentSpecOutputContractIsIndependentAndFrozen(t *testing.T) {
	rootCalls, childCalls := 0, 0
	client := &agentSpecStructuredModel{agentSessionModel: &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		if request.Model == "specialist-schema" {
			childCalls++
			if request.OutputContract == nil || request.OutputContract.Name != "specialist_answer" ||
				string(request.OutputContract.Schema) != specAnswerSchema || len(request.Tools) != 0 || len(request.HostedTools) != 0 {
				t.Fatalf("Named Agent did not get its own output contract/authority: %+v", request)
			}
			return model.Response{Content: `{"answer":"verified"}`}, nil
		}
		rootCalls++
		if request.OutputContract != nil {
			t.Fatal("Root inherited child-only output contract")
		}
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "root summarized verified child"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "named-output", ToolKey: harness.DelegationToolKey,
			Arguments: json.RawMessage(`{"memberID":"schema","goal":"produce a typed answer"}`)}}}, nil
	}}}
	store, runs := harness.NewMemoryStore(), memory.NewStore()
	options := agentSessionOptions(t, runs, store, client)
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Relations = relations
	options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	options.NamedAgents = []harness.NamedAgent{{MemberID: "schema", MemberRevision: "r1", AgentSpec: harness.AgentSpec{
		Model: "specialist-schema", OutputContract: &model.OutputContract{Name: "specialist_answer", Schema: json.RawMessage(specAnswerSchema)},
	}}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := session.Prompt(t.Context(), "named-schema", "delegate a typed answer")
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted || childCalls != 1 || rootCalls != 2 {
		t.Fatalf("Named Agent typed output did not complete independently: status=%s err=%v child=%d root=%d", snapshot.Turn.Status, err, childCalls, rootCalls)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Resume(t.Context(), "named-schema"); err != nil || childCalls != 1 || rootCalls != 2 {
		t.Fatalf("original typed child was replayed on restart: %v child=%d root=%d", err, childCalls, rootCalls)
	}
	// A future Host cannot replace the frozen child schema or its name.
	modified := options
	modified.NamedAgents = append([]harness.NamedAgent(nil), options.NamedAgents...)
	modified.NamedAgents[0].AgentSpec = harness.AgentSpec{Model: "specialist-schema", OutputContract: &model.OutputContract{
		Name: "foreign_schema", Schema: json.RawMessage(specAnswerSchema),
	}}
	foreign, err := harness.OpenAgentSession(modified)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.Resume(t.Context(), "named-schema"); !errors.Is(err, harness.ErrInvalidRequest) || childCalls != 1 {
		t.Fatalf("foreign child OutputContract borrowed frozen execution: err=%v child=%d", err, childCalls)
	}
}

func TestAgentSpecCompositionRejectsUntrustedOutputAndHostedGrants(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*harness.AgentSessionOptions)
	}{
		{"unversioned root hosted grant", func(o *harness.AgentSessionOptions) {
			o.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search"}}
		}},
		{"local and hosted same key", func(o *harness.AgentSessionOptions) {
			o.Session.Config.HostedToolGrants = []agent.HostedToolGrant{{Key: "lookup", DefinitionVersion: "v1"}}
		}},
		{"initial tool outside selected capability", func(o *harness.AgentSessionOptions) { o.Session.Config.InitialToolKeys = []string{"foreign"} }},
		{"invalid root output contract", func(o *harness.AgentSessionOptions) {
			o.Session.Config.OutputContract = &model.OutputContract{Name: "bad", Schema: json.RawMessage(`{"type":"string"}`)}
		}},
		{"invalid named output contract", func(o *harness.AgentSessionOptions) {
			o.NamedAgents = []harness.NamedAgent{{MemberID: "worker", MemberRevision: "r1", AgentSpec: harness.AgentSpec{Model: "worker", OutputContract: &model.OutputContract{Name: "", Schema: json.RawMessage(specAnswerSchema)}}}}
			o.Session.Config.DelegationPolicy.MaxDepth = 1
			r, e := runrelation.New(memory.NewRunRelationStore(), nil)
			if e != nil {
				panic(e)
			}
			o.Relations = r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
			addAgentSessionTool(&options, "lookup", new(int))
			tc.mutate(&options)
			if _, err := harness.OpenAgentSession(options); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("untrusted AgentSpec admitted: %v", err)
			}
			if len(client.requests) != 0 {
				t.Fatal("invalid AgentSpec physically dispatched Model")
			}
		})
	}
}

func TestAgentSpecFrozenConfigDistinctForHostedOrChildOutputChange(t *testing.T) {
	base := harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "same", MaxModelOutputTokens: 128}}
	first, err := harness.SealConfigSnapshot("same-turn", base, agentSpecTestTime())
	if err != nil {
		t.Fatal(err)
	}
	base.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search", DefinitionVersion: "v2"}}
	second, err := harness.SealConfigSnapshot("same-turn", base, agentSpecTestTime())
	if err != nil || second.ContentHash == first.ContentHash {
		t.Fatalf("root Hosted grants excluded from frozen Config: %v", err)
	}
	base.HostedToolGrants = nil
	base.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1, NamedAgents: []harness.NamedAgentSnapshot{{MemberID: "child", MemberRevision: "r1", AgentSpec: harness.AgentSpec{
		Model: "different", OutputContract: &model.OutputContract{Name: "one", Schema: json.RawMessage(specAnswerSchema)},
	}}}}
	third, err := harness.SealConfigSnapshot("same-turn", base, agentSpecTestTime())
	if err != nil {
		t.Fatal(err)
	}
	base.DelegationPolicy.NamedAgents[0].OutputContract.Name = "two"
	fourth, err := harness.SealConfigSnapshot("same-turn", base, agentSpecTestTime())
	if err != nil || fourth.ContentHash == third.ContentHash {
		t.Fatalf("named child output contract omitted from original Config: %v", err)
	}
}

func agentSpecTestTime() time.Time { return time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC) }
