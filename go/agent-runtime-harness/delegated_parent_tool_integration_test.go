package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type parentHostedAssignmentModel struct {
	t                     *testing.T
	requested             []string
	expectedChildModel    string
	frozen                bool
	rootCalls, childCalls int
}

func (capture *parentHostedAssignmentModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if strings.HasPrefix(request.RunID, "hchild_") {
		capture.childCalls++
		if capture.frozen {
			if len(request.Messages) == 0 || request.Messages[0].Role != model.RoleSystem ||
				strings.Count(model.TextValue(request.Messages[0].Content), "You are a delegated specialist.") != 1 {
				capture.t.Fatalf("specialist instruction not frozen exactly once: %+v", request.Messages)
			}
			for _, message := range request.Messages {
				if strings.Contains(model.TextValue(message.Content), "prior supervisor conversation") {
					capture.t.Fatal("delegated child inherited supervisor-only Context")
				}
			}
		}
		if request.Model != capture.expectedChildModel {
			capture.t.Fatalf("child model %q != %q", request.Model, capture.expectedChildModel)
		}
		expected := len(capture.requested) > 0
		if expected && (len(request.HostedTools) != 1 || request.HostedTools[0].Key != scopedSearchKey || request.HostedTools[0].DefinitionVersion != "role-web-version-1") {
			capture.t.Fatalf("explicit compatible parent hosted grant lost: %+v", request.HostedTools)
		}
		if !expected && len(request.HostedTools) != 0 {
			capture.t.Fatalf("parent Hosted tools implicitly leaked to child: %+v", request.HostedTools)
		}
		return model.Response{Content: "specialist finished"}, nil
	}
	capture.rootCalls++
	if capture.frozen {
		found := false
		for _, message := range request.Messages {
			if model.TextValue(message.Content) == "prior supervisor conversation" {
				found = true
			}
		}
		if !found {
			capture.t.Fatal("managed Context prefix missing in root request")
		}
	}
	if request.Model != "specialist-model" || len(request.HostedTools) != 1 || request.HostedTools[0].Key != scopedSearchKey {
		capture.t.Fatalf("parent hosted evidence is missing: %+v", request)
	}
	if capture.rootCalls == 1 {
		var schema map[string]interface{}
		for _, tool := range request.Tools {
			if tool.Key == harness.DelegationToolKey {
				if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
					capture.t.Fatal(err)
				}
			}
		}
		if schema == nil {
			capture.t.Fatal("delegation Tool not offered")
		}
		fields := schema["properties"].(map[string]interface{})
		_, ok := fields["toolKeys"]
		if capture.expectedChildModel == "specialist-model" && !ok {
			capture.t.Fatal("parent Hosted capability unavailable in delegated tool schema")
		}
		if capture.expectedChildModel != "specialist-model" && ok {
			capture.t.Fatal("Hosted parent Tool offered where every child uses different model")
		}
		args, _ := json.Marshal(struct {
			RoleID   string   `json:"roleID"`
			Goal     string   `json:"goal"`
			ToolKeys []string `json:"toolKeys,omitempty"`
		}{
			RoleID: "researcher", Goal: "Find only verified current news", ToolKeys: capture.requested,
		})
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-news-1", ToolKey: harness.DelegationToolKey, Arguments: args}}}, nil
	}
	return model.Response{Content: "Parent finished after delegation result."}, nil
}

func runSelectedHostedParentTool(t *testing.T, childModel string, selected []string, wantChild bool) {
	runSelectedHostedParentToolWithFrozenContext(t, childModel, selected, wantChild, false)
}

func runSelectedHostedParentToolWithFrozenContext(t *testing.T, childModel string, selected []string, wantChild, frozen bool) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	approvals, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	handler := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(handler)})
	if err != nil {
		t.Fatal(err)
	}
	capture := &parentHostedAssignmentModel{t: t, requested: selected, expectedChildModel: childModel, frozen: frozen}
	dependencies := delegationAgentDependencies(runtime, capture, registry, approvals, 12, false)
	rolePrepare, err := harness.NewRoleModelRequestPreparer(store)
	if err != nil {
		t.Fatal(err)
	}
	dependencies.PrepareModelRequest = rolePrepare
	var manager *runtimecontext.Manager
	projectedRoleWindowChecked := false
	if frozen {
		manager = runtimecontext.NewManager(runtimecontext.Dependencies{})
		managed, prepareErr := harness.NewManagedContextPreparer(store, manager, func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
			for _, definition := range request.Tools {
				if definition.Key == harness.DelegationToolKey {
					if !strings.Contains(definition.Description, "Available roles:") || !strings.Contains(string(definition.InputSchema), "roleID") {
						t.Fatalf("managed Context measured unprojected delegation Tool: %+v", definition)
					}
					projectedRoleWindowChecked = true
				}
			}
			return runtimecontext.ModelWindow{ContextTokens: 20000, ReservedOutputTokens: 512}, nil
		})
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		dependencies.PrepareModelRequest = agent.ChainModelRequestPreparers(rolePrepare, managed)
		ctxRun, contextErr := harness.NewContextRunMiddleware(store, nil)
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		dependencies.RunMiddleware = append(dependencies.RunMiddleware, ctxRun)
	}
	dependencies.HostedTools = roleScopedHostedCatalog{}
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
	composed := delegationHarnessDependencies(runtime, direct, store, registry, handoffs, relations, false)
	if frozen {
		composed.Context = manager
	}
	runner, err := harness.NewRunner(composed)
	if err != nil {
		t.Fatal(err)
	}
	if err = handler.Bind(runner); err != nil {
		t.Fatal(err)
	}
	start := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "tool-selection-thread"},
		HostTurn:   harness.HostRef{Kind: "message", ID: "tool-selection-turn"},
		Actor:      kernel.ActorRef{TenantID: "test", ActorID: "parent"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "tool-selection-thread"},
		Goal:       "delegate researched news to specialist", Config: harness.ConfigSnapshot{
			Model: "specialist-model", ToolKeys: []string{harness.DelegationToolKey, scopedSearchKey},
			ToolPolicies: []harness.ToolPolicySnapshot{
				harness.DelegationToolPolicySnapshot(),
				{Key: scopedSearchKey, DefinitionVersion: "role-web-version-1", ExecutionMode: "provider_hosted"},
			}, Roles: []harness.RoleSnapshot{{ID: "researcher", Revision: 1, Name: "Researcher", Model: childModel}},
		},
	}
	if frozen {
		start.Context = contextSeed("",
			contextEntry("prev-user", "previous", model.RoleUser, "prior supervisor conversation", false),
			contextEntry("prev-answer", "previous", model.RoleAssistant, "supervisor earlier answer", false),
			contextEntry("current-user", "current", model.RoleUser, start.Goal, true),
		)
	}
	snapshot, err := runner.StartAgentTurn(t.Context(), start)
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted {
		t.Fatalf("delegation result=%s err=%v", snapshot.Turn.Status, err)
	}
	if frozen && !projectedRoleWindowChecked {
		t.Fatal("managed Context never measured frozen role Tool presentation")
	}
	if capture.rootCalls != 2 {
		t.Fatalf("root calls=%d", capture.rootCalls)
	}
	if wantChild && capture.childCalls != 1 || !wantChild && capture.childCalls != 0 {
		t.Fatalf("child calls=%d expectedChild=%v", capture.childCalls, wantChild)
	}
	if wantChild && len(snapshot.Subtasks) != 1 {
		t.Fatalf("authorized child missing: %+v", snapshot.Subtasks)
	}
	if !wantChild && len(snapshot.Subtasks) != 0 {
		t.Fatalf("rejected child left phantom subtask: %+v", snapshot.Subtasks)
	}
}

// Proves a delegated child with role-specific instructions can run through
// pre-intent Context and role Tool schema preparation in one frozen intent.
func TestDelegatedRoleCompatibleWithFrozenContextPreparation(t *testing.T) {
	for _, selected := range [][]string{nil, {scopedSearchKey}} {
		runSelectedHostedParentToolWithFrozenContext(t, "specialist-model", selected, true, true)
	}
}

func TestRootAgentCanExplicitlyAssignMatchingModelHostedTool(t *testing.T) {
	t.Parallel()
	runSelectedHostedParentTool(t, "specialist-model", []string{scopedSearchKey}, true)
}
func TestRootAgentCannotAssignHostedToolToDifferentChildModel(t *testing.T) {
	t.Parallel()
	runSelectedHostedParentTool(t, "other-model", []string{scopedSearchKey}, false)
}
func TestRootAgentDoesNotImplicitlyCopyHostedToolsToDelegatedChild(t *testing.T) {
	t.Parallel()
	runSelectedHostedParentTool(t, "specialist-model", nil, true)
}

type inheritedModelRoleHostedClient struct {
	t                     *testing.T
	rootCalls, childCalls int
}

func (client *inheritedModelRoleHostedClient) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if request.Model != "specialist-model" {
		client.t.Fatalf("inherited child model drift: %s", request.Model)
	}
	if strings.HasPrefix(request.RunID, "hchild_") {
		client.childCalls++
		if len(request.HostedTools) != 1 || request.HostedTools[0].Key != scopedSearchKey {
			client.t.Fatalf("inherited-role Hosted tool missing from child: %+v", request.HostedTools)
		}
		return model.Response{Content: "analysis with hosted research"}, nil
	}
	client.rootCalls++
	if len(request.HostedTools) != 0 {
		client.t.Fatalf("role Scoped Hosted Tool leaked into root: %+v", request.HostedTools)
	}
	if client.rootCalls == 1 {
		return model.Response{ToolCalls: []tools.Call{{ID: "inherit-role-model-1", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"roleID":"researcher","goal":"research current news with inherited model"}`)}}}, nil
	}
	return model.Response{Content: "the delegated specialist completed"}, nil
}
func TestRoleHostedToolCanUseExactInheritedParentModelWithoutParentActivation(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	approvals, err := harness.NewFrozenApprovalPolicy(store)
	if err != nil {
		t.Fatal(err)
	}
	handler := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(handler)})
	if err != nil {
		t.Fatal(err)
	}
	capture := &inheritedModelRoleHostedClient{t: t}
	dependencies := delegationAgentDependencies(runtime, capture, registry, approvals, 8, false)
	dependencies.HostedTools = roleScopedHostedCatalog{}
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
	if err = handler.Bind(runner); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartAgentTurn(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "inherit-hosted-tool-thread"}, HostTurn: harness.HostRef{Kind: "message", ID: "inherit-hosted-tool-message"},
		Actor: kernel.ActorRef{TenantID: "test", ActorID: "parent"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "inherit-hosted-tool-thread"},
		Goal: "ask inheriting model role to research", Config: harness.ConfigSnapshot{
			Model: "specialist-model", ToolKeys: []string{harness.DelegationToolKey},
			ToolPolicies: []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot()},
			Roles: []harness.RoleSnapshot{{ID: "researcher", Revision: 1, Name: "Researcher", Model: "",
				HostedToolGrants: []agent.HostedToolGrant{{Key: scopedSearchKey, DefinitionVersion: "role-web-version-1"}}}},
		},
	})
	if err != nil || snapshot.Turn.Status != harness.TurnCompleted || capture.rootCalls != 2 || capture.childCalls != 1 {
		t.Fatalf("inherited model hosted: status=%s err=%v root=%d child=%d", snapshot.Turn.Status, err, capture.rootCalls, capture.childCalls)
	}
}
