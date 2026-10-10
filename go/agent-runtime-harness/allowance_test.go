package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// A completion correction forces a second logical model call without needing
// any artificially authorized tools. Only the first proposed answer is rejected.
type allowanceCompletionPolicy struct{ checks int }

func (policy *allowanceCompletionPolicy) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	policy.checks++
	if policy.checks == 1 {
		return agent.CompletionCorrection{Code: "retry_answer", Message: "Repair the terminal answer"}, nil
	}
	return agent.CompletionCorrection{}, nil
}

func TestHarnessProjectsBudgetPauseAndGrantsOwnerBoundDirectRun(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	completion := &allowanceCompletionPolicy{}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: directModel{},
		CompletionPolicies: []agent.CompletionPolicy{completion},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	host, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := host.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("Turn paused=%#v checks=%d err=%v", paused.Turn, completion.checks, err)
	}
	invocation, ok := harness.TopLevelInvocation(paused)
	if !ok || invocation.Status != harness.InvocationPausedBudget || invocation.Attempt != 1 {
		t.Fatalf("invocation paused=%#v", invocation)
	}
	run, err := runtime.Load(t.Context(), invocation.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusPausedBudget || completion.checks != 1 {
		t.Fatalf("runtime pause=%#v checks=%d err=%v", run.Run, completion.checks, err)
	}
	if _, err = host.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{ToolCalls: 1}); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("wrong dimension was admitted: %v", err)
	}
	if _, err = host.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision+1, agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale revision was admitted: %v", err)
	}
	granted, err := host.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || granted.Turn.Status != harness.TurnRunning || len(granted.Invocations) != 1 ||
		granted.Invocations[0].Attempt != 1 || granted.Invocations[0].ExecutionRefID != run.Run.ID {
		t.Fatalf("granted Turn=%#v Invocations=%#v err=%v", granted.Turn, granted.Invocations, err)
	}
	if _, err = host.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("duplicate grant admitted: %v", err)
	}
	afterGrant, err := runtime.Load(t.Context(), run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := direct.Resume(t.Context(), run.Run.ID, afterGrant.Run.Revision)
	if err != nil || terminal.Run.Status != kernel.RunStatusCompleted || completion.checks != 2 {
		t.Fatalf("resumed Run=%#v err=%v checks=%d", terminal.Run, err, completion.checks)
	}
	refreshed, err := host.Refresh(t.Context(), paused.Turn.ID)
	if err != nil || refreshed.Turn.Status != harness.TurnCompleted ||
		refreshed.Invocations[0].Attempt != 1 || refreshed.Invocations[0].ExecutionRefID != run.Run.ID {
		t.Fatalf("completion projection=%#v err=%v", refreshed.Turn, err)
	}
}
