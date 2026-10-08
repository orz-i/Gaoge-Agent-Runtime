package harness_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const scopedSearchKey = "minimax.web_search"

type roleScopedHostedCatalog struct{}

func (roleScopedHostedCatalog) Resolve(_ context.Context, key, modelName string) (model.HostedTool, bool, error) {
	if key != scopedSearchKey || modelName != "specialist-model" {
		return model.HostedTool{}, false, nil
	}
	return model.HostedTool{
		Key: scopedSearchKey, DefinitionVersion: "role-web-version-1",
		Target: json.RawMessage(`{"variants":[{"protocol":"minimax_anthropic","payload":{"type":"web_search"}}]}`),
	}, true, nil
}

type roleScopedHostedModel struct {
	rootCalls  int
	childCalls int
	discovery  bool
	t          *testing.T
}

func (capture *roleScopedHostedModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	switch request.Model {
	case "specialist-model":
		capture.childCalls++
		if len(request.Tools) != 0 || len(request.HostedTools) != 1 ||
			request.HostedTools[0].Key != scopedSearchKey ||
			request.HostedTools[0].DefinitionVersion != "role-web-version-1" {
			capture.t.Fatalf("role lost its own hosted search, or local Tools leaked: %+v", request)
		}
		return model.Response{Content: "found evidence", Usage: &model.Usage{InputTokens: 10, OutputTokens: 2}}, nil
	case "root-model":
		capture.rootCalls++
		if capture.discovery {
			if len(request.Tools) != 2 ||
				request.Tools[0].Key != harness.DelegationToolKey ||
				request.Tools[1].Key != agent.ToolDiscoveryKey {
				capture.t.Fatalf("first-party delegation invisible with discovery: %+v", request.Tools)
			}
		}
		if len(request.HostedTools) != 0 {
			capture.t.Fatalf("role hosted Tool leaked into root: %+v", request.HostedTools)
		}
		if capture.rootCalls == 1 {
			return model.Response{ToolCalls: []tools.Call{{
				ToolKey:   harness.DelegationToolKey,
				Arguments: json.RawMessage(`{"roleID":"researcher","goal":"search the latest information"}`),
			}}}, nil
		}
		return model.Response{Content: "research summarized"}, nil
	default:
		capture.t.Fatalf("unexpected delegated model %q", request.Model)
		return model.Response{}, nil
	}
}

func TestRoleScopedHostedToolWithoutParentActivation(t *testing.T) {
	runRoleScopedHostedTool(t, false)
}

func TestRoleScopedHostedToolWith128ScaleSearchInitial(t *testing.T) {
	runRoleScopedHostedTool(t, true)
}

func runRoleScopedHostedTool(t *testing.T, discovery bool) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	policy, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	delegation := harness.NewDelegationToolHandler()
	registrations := []tools.Registration{harness.DelegationToolRegistration(delegation)}
	keys := []string{harness.DelegationToolKey}
	policies := []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()}
	if discovery {
		// Model-owned Search must not conceal delegation when a user has
		// all 128 MCP tools configured in addition to this first-party tool.
		for i := range 128 {
			key := fmt.Sprintf("mcp.synthetic_%03d", i)
			keys = append(keys, key)
			registrations = append(registrations, tools.Registration{
				Definition: tools.Definition{
					Key: key, Name: fmt.Sprintf("synthetic_tool_%03d", i),
					InputSchema: json.RawMessage(`{"type":"object"}`),
				}, Handler: roleEvidenceHandler{},
			})
			policies = append(policies, harness.ToolPolicySnapshot{Key: key, DefinitionVersion: "fixture-v1"})
		}
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	capture := &roleScopedHostedModel{t: t, discovery: discovery}
	dependencies := delegationAgentDependencies(runtime, capture, registry, policy, 8, false)
	dependencies.HostedTools = roleScopedHostedCatalog{}
	if discovery {
		dependencies.ToolDiscovery = agent.LexicalToolDiscovery{}
	}
	direct, err := agent.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(direct)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), delegationTestClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(delegationHarnessDependencies(runtime, direct, store, registry, handoffs, relations, false))
	if err != nil {
		t.Fatal(err)
	}
	if err = delegation.Bind(runner); err != nil {
		t.Fatal(err)
	}
	initial := []string(nil)
	if discovery {
		initial = []string{harness.DelegationToolKey}
	}
	started, err := runner.Start(t.Context(), harness.StartRequest{
		InitialToolKeys: initial,
		HostThread:      harness.HostRef{Kind: "conversation", ID: "thread-scoped-hosted"},
		HostTurn:        harness.HostRef{Kind: "conversation", ID: "turn-scoped-hosted"},
		Actor:           kernel.ActorRef{TenantID: "test", ActorID: "actor"},
		Thread:          kernel.ThreadRef{Kind: "conversation", ID: "thread-scoped-hosted"},
		Goal:            "delegate research then summarize",
		Config: harness.ConfigSnapshot{
			Model:        "root-model",
			ToolKeys:     keys,
			ToolPolicies: policies,
			Roles: []harness.RoleSnapshot{{
				ID: "researcher", Revision: 1, Name: "Researcher",
				Model: "specialist-model",
				HostedToolGrants: []agent.HostedToolGrant{{
					Key: scopedSearchKey, DefinitionVersion: "role-web-version-1",
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("scoped hosted delegation failed: %v", err)
	}
	if started.Turn.Status != harness.TurnCompleted {
		t.Fatalf("unexpected harness status: %s", started.Turn.Status)
	}
	if capture.rootCalls != 2 || capture.childCalls != 1 {
		t.Fatalf("root calls=%d child calls=%d", capture.rootCalls, capture.childCalls)
	}
}

func TestRoleHostedGrantSealedWithoutParentToolKey(t *testing.T) {
	_, err := harness.SealConfigSnapshot("turn-grant", harness.ConfigSnapshot{
		Model: "root-model", ToolKeys: []string{harness.DelegationToolKey},
		Roles: []harness.RoleSnapshot{{
			ID: "researcher", Revision: 1, Name: "Researcher", Model: "specialist-model",
			HostedToolGrants: []agent.HostedToolGrant{{Key: scopedSearchKey, DefinitionVersion: "v1"}},
		}},
	}, time.Now())
	if err != nil {
		t.Fatalf("scoped provider Tool must not require parent activation: %v", err)
	}

	_, err = harness.SealConfigSnapshot("turn-invalid", harness.ConfigSnapshot{
		Model: "root-model", ToolKeys: []string{harness.DelegationToolKey},
		Roles: []harness.RoleSnapshot{{
			ID: "researcher", Revision: 1, Name: "Researcher", Model: "specialist-model",
			HostedToolGrants: []agent.HostedToolGrant{{Key: scopedSearchKey, DefinitionVersion: ""}},
		}},
	}, time.Now())
	if err == nil {
		t.Fatal("unversioned role Hosted grant accepted")
	}

	_, err = harness.SealConfigSnapshot("turn-unpinned-role-model", harness.ConfigSnapshot{
		Model: "root-model", ToolKeys: []string{harness.DelegationToolKey},
		Roles: []harness.RoleSnapshot{{
			ID: "researcher", Revision: 1, Name: "Researcher",
			HostedToolGrants: []agent.HostedToolGrant{{Key: scopedSearchKey, DefinitionVersion: "v1"}},
		}},
	}, time.Now())
	if err == nil {
		t.Fatal("Hosted role grant without a pinned role model accepted")
	}
}
