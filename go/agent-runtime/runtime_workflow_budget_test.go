package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type workflowRetryToolExecutor struct {
	calls      int
	failures   int
	outputJSON string
}

const (
	workflowTestProviderKey = "provider"
	workflowTestScopeName   = "scope"
)

func (e *workflowRetryToolExecutor) Execute(_ context.Context, _ ToolExecutionInput) (string, error) {
	e.calls++
	if e.calls <= e.failures {
		return "", errors.New("retryable tool failure")
	}
	return e.outputJSON, nil
}

func TestWorkflowToolRetryBudgetTightensToRemainingCalls(t *testing.T) {
	budget := model.WorkflowBudget{
		Limits:        model.WorkflowLimits{MaxTotalToolCalls: 3},
		UsedToolCalls: 1,
	}
	retries, remaining, err := workflowToolRetryBudget(effectiveRunToolPolicy{RetryCount: 5}, budget)
	if err != nil || retries != 1 || remaining != 2 {
		t.Fatalf("retry budget = (%d, %d, %v), want (1, 2, nil)", retries, remaining, err)
	}

	budget.UsedToolCalls = 3
	if _, _, err = workflowToolRetryBudget(effectiveRunToolPolicy{RetryCount: 1}, budget); !errors.Is(err, ErrWorkflowBudgetExceeded) {
		t.Fatalf("exhausted retry budget error = %v", err)
	}
}

func TestToolExecutionResultCountsActualRetryAttempts(t *testing.T) {
	executor := &workflowRetryToolExecutor{failures: 1, outputJSON: `{}`}
	engine := &Engine{
		cfg:          StaticConfigProvider(Config{}),
		toolExecutor: executor,
	}
	result, err := engine.executeToolCall(t.Context(), ExecuteToolInput{
		ToolKey:      "catalog.lookup",
		ProviderKind: valueMcp75675BED,
		ProviderKey:  workflowTestProviderKey,
		ToolName:     "lookup",
		ExecutionLimits: &TextRunExecutionLimits{
			ToolRetryCount:  2,
			ToolConcurrency: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 2 || result.Attempts != 2 || result.OutputJSON != `{}` {
		t.Fatalf("tool execution = calls:%d result:%+v", executor.calls, result)
	}
}

func TestWorkflowThreadSnapshotHashCoversModelProviderScopeAndEnvironment(t *testing.T) {
	const (
		snapshotModel    = "model-a"
		snapshotProvider = "provider-a"
		snapshotScope    = "general"
	)
	input := StartWorkflowInput{
		Actor:          model.ActorRef{TenantID: "tenant-a", ActorID: "actor-a"},
		Thread:         model.ThreadRef{Kind: valueThreadRefKey, ID: "thread-a"},
		Environment:    model.ResourceRef{Kind: resourceKindEnvironment, ID: "environment-a", Revision: "1"},
		ThreadModel:    snapshotModel,
		ThreadProvider: snapshotProvider,
	}
	environment := json.RawMessage(`{"revision":1}`)
	base, err := workflowThreadSnapshotHash(input, environment, nil, snapshotScope)
	if err != nil {
		t.Fatal(err)
	}
	variants := []struct {
		name        string
		input       StartWorkflowInput
		environment json.RawMessage
		scope       string
	}{
		{name: "model", input: func() StartWorkflowInput { value := input; value.ThreadModel = "model-b"; return value }(), environment: environment, scope: snapshotScope},
		{name: workflowTestProviderKey, input: func() StartWorkflowInput { value := input; value.ThreadProvider = "provider-b"; return value }(), environment: environment, scope: snapshotScope},
		{name: workflowTestScopeName, input: input, environment: environment, scope: "workspace"},
		{name: "environment snapshot", input: input, environment: json.RawMessage(`{"revision":2}`), scope: snapshotScope},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			hash, hashErr := workflowThreadSnapshotHash(variant.input, variant.environment, nil, variant.scope)
			if hashErr != nil {
				t.Fatal(hashErr)
			}
			if hash == base {
				t.Fatalf("snapshot hash did not change for %s", variant.name)
			}
		})
	}
}
