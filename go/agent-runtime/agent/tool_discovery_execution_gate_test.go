package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The live catalog can change after the model has seen a valid loaded schema,
// but before the already-queued Tool call is executed. The pending execution
// boundary must fail closed rather than execute the replacement definition.
type executionDriftCatalog struct {
	*tools.Registry
	changed bool
	removed bool
}

func (catalog *executionDriftCatalog) Resolve(key string) (tools.Definition, bool) {
	definition, found := catalog.Registry.Resolve(key)
	if key == discoveryChosenToolKey && catalog.removed {
		return tools.Definition{}, false
	}
	if key == discoveryChosenToolKey && found && catalog.changed {
		definition.Description = "new schema owner or revoked definition"
	}
	return definition, found
}

type executionDriftModel struct {
	catalog     *executionDriftCatalog
	searchFirst bool
	remove      bool
	calls       int
}

func (stub *executionDriftModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	stub.calls++
	if stub.searchFirst && stub.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ID:        "discovery-before-drift",
			ToolKey:   agent.ToolDiscoveryKey,
			Arguments: json.RawMessage(`{"query":"look up documents"}`),
		}}}, nil
	}
	// The model received a legitimate tool declaration, but the catalog is
	// revoked/replaced before execution. This is not a new Model Request.
	if stub.remove {
		stub.catalog.removed = true
	} else {
		stub.catalog.changed = true
	}
	return model.Response{ToolCalls: []tools.Call{{
		ID:        "business-after-catalog-drift",
		ToolKey:   discoveryChosenToolKey,
		Arguments: json.RawMessage(`{}`),
	}}}, nil
}

func TestRuntimeToolDiscoveryRechecksLoadedDefinitionAtBusinessExecution(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		searchFirst bool
		remove      bool
	}{
		{name: "initial tool replaced after model response"},
		{name: "initial tool removed after model response", remove: true},
		{name: "discovered tool replaced after model response", searchFirst: true},
		{name: "discovered tool removed after model response", searchFirst: true, remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime, _ := newTestRuntimeAndApprovals(t)
			executions := 0
			registry, keys := discoveryTestRegistry(t, 32, &executions)
			catalog := &executionDriftCatalog{Registry: registry}
			provider := &executionDriftModel{catalog: catalog, searchFirst: tc.searchFirst, remove: tc.remove}
			deps := agent.Dependencies{
				Runtime: runtime, Model: provider, Catalog: catalog, Executor: registry,
				ToolDiscovery: &testToolDiscovery{returned: []string{discoveryChosenToolKey}},
			}
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			request := startRequest("run_execution_drift", "request_execution_drift", "Review documents", keys...)
			if !tc.searchFirst {
				request.InitialToolKeys = []string{discoveryChosenToolKey}
			}
			snapshot, err := runner.StartRun(t.Context(), request)
			if err == nil || snapshot.Run.Status != kernel.RunStatusFailed ||
				snapshot.Run.ErrorCode != "agent.discovery_invalid" || executions != 0 {
				t.Fatalf("stale Tool passed execution boundary: status=%s code=%s err=%v businessCalls=%d",
					snapshot.Run.Status, snapshot.Run.ErrorCode, err, executions)
			}
			wantCalls := 1
			if tc.searchFirst {
				wantCalls = 2
			}
			if provider.calls != wantCalls {
				t.Fatalf("unexpected model retries or call count: got %d want %d", provider.calls, wantCalls)
			}
		})
	}
}

type executionSafeResponseModel struct{}

func (executionSafeResponseModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	return model.Response{ToolCalls: []tools.Call{{
		ID:        "approved-then-drifted",
		ToolKey:   discoveryChosenToolKey,
		Arguments: json.RawMessage(`{}`),
	}}}, nil
}

type driftAtApprovalPolicy struct {
	catalog *executionDriftCatalog
	calls   int
}

func (*driftAtApprovalPolicy) Name() string { return "drift-after-model-before-execute" }
func (p *driftAtApprovalPolicy) Approval(_ context.Context, invocation plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	if invocation.Definition.Key == discoveryChosenToolKey {
		p.calls++
		p.catalog.changed = true
	}
	return plugin.ApprovalNotRequired, nil
}

// The first pending-call pass revalidates the frozen catalog and then evaluates
// approval. Changing the definition in that approval hook proves the final
// execution preflight independently blocks a stale or revoked Tool.
func TestRuntimeToolDiscoveryRechecksAfterApprovalPolicyBeforeExecutor(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 32, &executions)
	catalog := &executionDriftCatalog{Registry: registry}
	policy := &driftAtApprovalPolicy{catalog: catalog}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: executionSafeResponseModel{},
		Catalog: catalog, Executor: registry, ToolDiscovery: agent.LexicalToolDiscovery{},
		ApprovalPolicies: []plugin.ApprovalPolicy{policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("run_approval_catalog_drift", "req_approval_catalog_drift", "Find documents", keys...)
	request.InitialToolKeys = []string{discoveryChosenToolKey}
	snapshot, err := runner.StartRun(t.Context(), request)
	if err == nil || snapshot.Run.Status != kernel.RunStatusFailed ||
		snapshot.Run.ErrorCode != "agent.discovery_invalid" || executions != 0 || policy.calls != 1 {
		t.Fatalf("post-approval Tool drift reached Executor: status=%s code=%s err=%v executions=%d approvalChecks=%d",
			snapshot.Run.Status, snapshot.Run.ErrorCode, err, executions, policy.calls)
	}
}
