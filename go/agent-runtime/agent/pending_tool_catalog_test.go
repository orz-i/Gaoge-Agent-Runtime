package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestPendingToolBatchRejectsCatalogReplacementAcrossRestart(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*tools.Definition)
	}{
		{"same definition", func(*tools.Definition) {}},
		{"name", func(d *tools.Definition) { d.Name = "replacement" }},
		{"terminal", func(d *tools.Definition) { d.Terminal = true }},
		{"schema", func(d *tools.Definition) {
			d.InputSchema = json.RawMessage(`{"type":"object","properties":{"extra":{"type":"string"}}}`)
		}},
		{"description", func(d *tools.Definition) { d.Description = "new implementation contract" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			base := memory.NewStore()
			fault := &modelInvocationFaultStore{Store: base, eventType: "tool.batch_requested", afterCommit: true}
			provider := &autonomousContinuationModel{}
			executions := 0
			original := autonomousContinuationRegistry(t, &executions)
			first := newAutonomousAgentRunner(t, fault, provider, original)
			request := startRequest("pending-catalog", "pending-catalog-request", "publish", "test.autonomous")
			request.MaxModelOutputTokens = 128
			if _, err := first.StartRun(t.Context(), request); !errors.Is(err, errInjectedModelInvocationCrash) || executions != 0 || provider.calls != 1 {
				t.Fatalf("did not stop at committed unsent Tool batch: %v tools=%d models=%d", err, executions, provider.calls)
			}
			definition, _ := original.Resolve("test.autonomous")
			change.mutate(&definition)
			replacement := mustRegistry(t, []tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				executions++
				return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{ExecutionID: "autonomous-call", Disposition: "committed"}}, nil
			})}})
			runtime := newModelInvocationRuntime(t, base)
			pending := mustLoadModelInvocationRun(t, runtime, request.ID)
			restarted := newAutonomousAgentRunnerWithRuntime(t, runtime, provider, replacement)
			completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if change.name == "same definition" {
				if err != nil || executions != 1 || completed.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("matching original catalog did not resume: %v tools=%d status=%s", err, executions, completed.Run.Status)
				}
			} else if err == nil || executions != 0 || completed.Run.Status != kernel.RunStatusFailed {
				t.Fatalf("replacement catalog executed old pending Tool call: %v tools=%d status=%s", err, executions, completed.Run.Status)
			}
		})
	}
}

func TestPendingToolCatalogUsesOriginalDefinitionBeforeHostProjection(t *testing.T) {
	executions := 0
	registry := autonomousContinuationRegistry(t, &executions)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: &autonomousContinuationModel{},
		Catalog: registry, Executor: registry,
		PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) {
			request.Tools[0].Description = "narrow model-facing presentation"
			request.Tools[0].InputSchema = json.RawMessage(`{"type":"object","additionalProperties":false}`)
			return request, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("pending-projected-catalog", "pending-projected-request", "publish", "test.autonomous")
	request.MaxModelOutputTokens = 128
	done, err := runner.StartRun(t.Context(), request)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || executions != 1 {
		t.Fatalf("original Catalog fingerprint was confused with the projected schema: %v tools=%d status=%s", err, executions, done.Run.Status)
	}
}

func TestPendingToolCatalogRechecksAfterApprovalPolicy(t *testing.T) {
	executions := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: discoveryChosenToolKey, Name: "chosen", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			executions++
			return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	catalog := &executionDriftCatalog{Registry: registry}
	policy := &driftAtApprovalPolicy{catalog: catalog}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: newModelInvocationRuntime(t, memory.NewStore()), Model: executionSafeResponseModel{},
		Catalog: catalog, Executor: registry, ApprovalPolicies: []plugin.ApprovalPolicy{policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("pending-approval-catalog", "pending-approval-request", "publish", discoveryChosenToolKey)
	request.MaxModelOutputTokens = 128
	done, err := runner.StartRun(t.Context(), request)
	if err == nil || done.Run.Status != kernel.RunStatusFailed || executions != 0 || policy.calls != 1 {
		t.Fatalf("post-approval eager Catalog drift reached execution: %v tools=%d approvals=%d status=%s", err, executions, policy.calls, done.Run.Status)
	}
}

func TestPendingToolCatalogMissingProofRejectsEveryCeiling(t *testing.T) {
	for _, ceiling := range []int64{0, 128} {
		base := memory.NewStore()
		fault := &modelInvocationFaultStore{Store: base, eventType: "tool.batch_requested", afterCommit: true}
		executions := 0
		runner := newAutonomousAgentRunner(t, fault, &autonomousContinuationModel{}, autonomousContinuationRegistry(t, &executions))
		request := startRequest("pending-missing-catalog", "pending-missing-request", "publish", "test.autonomous")
		request.MaxModelOutputTokens = ceiling
		if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errInjectedModelInvocationCrash) {
			t.Fatal(err)
		}
		pending := mustLoadModelInvocationRun(t, newModelInvocationRuntime(t, base), request.ID)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(pending.State, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields["pendingToolCatalog"]) == 0 {
			t.Fatal("new batch did not atomically freeze Catalog evidence")
		}
		delete(fields, "pendingToolCatalog")
		var err error
		pending.State, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		_, err = agent.ViewState(pending)
		if !errors.Is(err, agent.ErrInvalidRequest) {
			t.Fatalf("missing original Tool Catalog proof was accepted at ceiling=%d: %v", ceiling, err)
		}
	}
}

func TestPendingToolCatalogMissingProofCannotAuthorizeOldApproval(t *testing.T) {
	runner, runtime, approvals, waiting, executions := newApprovalRecoveryRun(t)
	legacy := approvalStateEdit(t, waiting.State, func(fields map[string]json.RawMessage) {
		delete(fields, "pendingToolCatalog")
	})
	resolved, err := approvals.Resolve(waiting.Checkpoint, interaction.ApprovalResponse{Decision: interaction.DecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := seedApprovalRecoveryFixture(t.Context(), runtime, waiting, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: legacy, Checkpoint: resolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.Resume(t.Context(), admitted.Run.ID, admitted.Run.Revision)
	if !errors.Is(err, agent.ErrInvalidRequest) || *executions != 0 {
		t.Fatalf("unsealed zero-ceiling approval created a Tool effect: %v tools=%d status=%s", err, *executions, done.Run.Status)
	}
}
