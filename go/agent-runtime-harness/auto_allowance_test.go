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

type autoCorrectionPolicy struct {
	checks int
	reject int
}

func (policy *autoCorrectionPolicy) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	policy.checks++
	if policy.checks <= policy.reject {
		return agent.CompletionCorrection{Code: "repair_response", Message: "Provide a stronger final answer"}, nil
	}
	return agent.CompletionCorrection{}, nil
}

func testAutoHarness(t *testing.T, rejects int) (*harness.Runner, *kernel.Runtime, *agent.Runner, *autoCorrectionPolicy) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	policy := &autoCorrectionPolicy{reject: rejects}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: directModel{},
		CompletionPolicies: []agent.CompletionPolicy{policy},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: harness.NewMemoryStore(), Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return host, runtime, direct, policy
}

func TestHarnessAutomaticallyContinuesDirectAgentWithSameRunIdentity(t *testing.T) {
	host, runtime, _, policy := testAutoHarness(t, 1)
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1,
	}
	done, err := host.Start(t.Context(), request)
	if err != nil || done.Turn.Status != harness.TurnCompleted || policy.checks != 2 {
		t.Fatalf("automatic completion=%#v checks=%d err=%v", done.Turn, policy.checks, err)
	}
	invocation, ok := harness.TopLevelInvocation(done)
	if !ok || invocation.Status != harness.InvocationCompleted || invocation.Attempt != 1 {
		t.Fatalf("auto invocation was retried: %#v", invocation)
	}
	run, err := runtime.Load(t.Context(), invocation.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusCompleted || run.Run.EndedAt == nil {
		t.Fatalf("auto Run not completed: %#v err=%v", run.Run, err)
	}
	view, err := agent.ViewState(run)
	if err != nil || view.AutoAllowance.ModelRenewals != 1 ||
		view.CallAllowance.LLMCalls != 2 || view.Budget.Usage.LLMCalls != 2 ||
		view.Budget.Limits.MaxLLMCalls != 4 {
		t.Fatalf("policy and hard accounting diverged: %#v err=%v", view, err)
	}
}

func TestHarnessAutomaticSegmentsExhaustToOriginalManualContinuation(t *testing.T) {
	host, runtime, direct, policy := testAutoHarness(t, 2)
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 2,
	}
	paused, err := host.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget || policy.checks != 2 {
		t.Fatalf("auto exhaustion: %#v checks=%d err=%v", paused.Turn, policy.checks, err)
	}
	invocation, ok := harness.TopLevelInvocation(paused)
	if !ok || invocation.Status != harness.InvocationPausedBudget || invocation.Attempt != 1 {
		t.Fatalf("unexpected paused invocation: %#v", invocation)
	}
	before, err := runtime.Load(t.Context(), invocation.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(before)
	if err != nil || view.AutoAllowance.ModelRenewals != 1 ||
		view.CallAllowance.LLMCalls != 2 || view.Budget.Usage.LLMCalls != 2 {
		t.Fatalf("stored auto progress: %#v err=%v", view, err)
	}
	if _, err = host.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision+1,
		agent.CallAllowance{LLMCalls: 1}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale owner grant must not succeed: %v", err)
	}
	granted, err := host.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision,
		agent.CallAllowance{LLMCalls: 1})
	if err != nil || granted.Turn.Status != harness.TurnRunning ||
		granted.Turn.ID != paused.Turn.ID || granted.Invocations[0].ExecutionRefID != before.Run.ID {
		t.Fatalf("manual grant after auto: %#v err=%v", granted.Turn, err)
	}
	running, err := runtime.Load(t.Context(), invocation.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := direct.Resume(t.Context(), running.Run.ID, running.Run.Revision)
	if err != nil || terminal.Run.Status != kernel.RunStatusCompleted || policy.checks != 3 {
		t.Fatalf("manual completion: %#v err=%v checks=%d", terminal.Run, err, policy.checks)
	}
	updated, err := host.Refresh(t.Context(), paused.Turn.ID)
	if err != nil || updated.Turn.Status != harness.TurnCompleted ||
		updated.Invocations[0].Attempt != 1 || updated.Invocations[0].ExecutionRefID != before.Run.ID {
		t.Fatalf("manual continuation replaced the original invocation: %#v err=%v", updated.Turn, err)
	}
}

func TestHarnessUnconfiguredAutoPolicyPreservesManualPause(t *testing.T) {
	host, _, _, policy := testAutoHarness(t, 1)
	request := testStartRequest()
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	paused, err := host.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget || policy.checks != 1 {
		t.Fatalf("P1 manual mode regressed: %#v checks=%d err=%v", paused.Turn, policy.checks, err)
	}
}
