package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestAutoAllowanceCompletesMultipleModelSegmentsWithoutManualGrant(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{
		{allowanceTool("manifest-a")}, {allowanceTool("manifest-b")},
	}}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	request := startRequest("auto-model-segments", "auto-model-request", "read twice", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1, ToolCalls: 5}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, MaxModelRenewals: 2, MaxNoProgressWindow: 1,
	}
	done, err := runner.StartRun(t.Context(), request)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted ||
		model.calls != 3 || len(executed) != 2 || done.Run.EndedAt == nil {
		t.Fatalf("auto model completion: %#v model=%d tool=%v err=%v", done.Run, model.calls, executed, err)
	}
	view, err := agent.ViewState(done)
	if err != nil || view.AutoAllowance == nil || view.AutoAllowance.ModelRenewals != 2 ||
		view.CallAllowance.LLMCalls != 3 || view.Budget.Usage.LLMCalls != 3 ||
		view.Budget.Usage.ToolCalls != 2 {
		t.Fatalf("auto model usage: %#v err=%v", view, err)
	}
	for _, modelRequest := range model.requests {
		if len(modelRequest.Messages) == 0 || modelRequest.Messages[0].Role != runtimemodel.RoleSystem ||
			!strings.Contains(modelRequest.Messages[0].Content, "Runtime execution window") ||
			strings.Contains(modelRequest.Messages[0].Content, "increase the hard limit") {
			t.Fatalf("soft guidance must not assert privilege: %#v", modelRequest.Messages)
		}
	}
	for _, persisted := range view.Messages {
		if strings.Contains(persisted.Content, "Runtime execution window") {
			t.Fatal("ephemeral model guidance must not pollute persisted transcript")
		}
	}
}

func TestAutoAllowanceResumesPendingToolBatchWithoutModelReplay(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{batches: [][]tools.Call{{
		allowanceTool("read-a"), allowanceTool("read-b"), allowanceTool("read-c"),
	}}}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	request := startRequest("auto-tool-batch", "auto-tool-request", "read a batch", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{ToolCalls: 1}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ToolStep: 1, MaxToolRenewals: 2, MaxNoProgressWindow: 1,
	}
	done, err := runner.StartRun(t.Context(), request)
	if err != nil || done.Run.Status != kernel.RunStatusCompleted || model.calls != 2 ||
		len(executed) != 3 || executed[0] != "read-a" || executed[1] != "read-b" || executed[2] != "read-c" {
		t.Fatalf("auto Tool batch: status=%v model=%d executed=%v err=%v", done.Run.Status, model.calls, executed, err)
	}
	view, err := agent.ViewState(done)
	if err != nil || view.AutoAllowance.ToolRenewals != 2 ||
		view.CallAllowance.ToolCalls != 3 || view.Budget.Usage.ToolCalls != 3 ||
		view.Budget.Usage.LLMCalls != 2 {
		t.Fatalf("auto Tool accounting: %#v err=%v", view, err)
	}
}

func TestAutoAllowancePausesOnRepeatedUnchangedWorkThenAllowsManualGrant(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: manifestToolKey, Name: manifestToolName, InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"same":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
			}, nil
		}),
	}})
	model := &allowanceModel{batches: [][]tools.Call{
		{allowanceTool("first-id")}, {allowanceTool("different-id-same-result")},
	}}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Limits: agent.Limits{MaxLLMCalls: 6, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("auto-stall", "auto-stall-request", "repeat work", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 1, MaxModelRenewals: 4, MaxNoProgressWindow: 1,
	}
	paused, err := runner.StartRun(t.Context(), request)
	if err != nil || paused.Run.Status != kernel.RunStatusPausedBudget ||
		paused.Run.ErrorCode != "agent.model_allowance_exhausted" || model.calls != 2 {
		t.Fatalf("unchanged work should pause: run=%#v calls=%d err=%v", paused.Run, model.calls, err)
	}
	view, err := agent.ViewState(paused)
	if err != nil || view.AutoAllowance.ModelRenewals != 1 || view.CallAllowance.LLMCalls != 2 {
		t.Fatalf("automatic renewal count: %#v err=%v", view, err)
	}
	// P1 owner-authorized manual grant still works; P2 cannot override a
	// human continuation decision or mutate the immutable hard ceiling.
	granted, err := runner.GrantCallAllowance(t.Context(), paused.Run.ID, paused.Run.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || granted.Run.Status != kernel.RunStatusRunning || granted.Run.ID != paused.Run.ID {
		t.Fatalf("manual fall-through grant: %#v err=%v", granted.Run, err)
	}
}

func TestAutoAllowanceCannotExceedFrozenHardLimit(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	var batches [][]tools.Call
	for i := 0; i < 6; i++ {
		batches = append(batches, []tools.Call{allowanceTool(fmt.Sprintf("unique-%d", i))})
	}
	model := &allowanceModel{batches: batches}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	request := startRequest("auto-hard", "auto-hard-request", "read beyond hard guard", manifestToolKey)
	request.CallAllowance = &agent.CallAllowance{LLMCalls: 2}
	request.AutoAllowance = &agent.AutoAllowancePolicy{
		ModelStep: 3, MaxModelRenewals: 4, MaxNoProgressWindow: 1,
	}
	failed, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, agent.ErrCallLimit) || failed.Run.Status != kernel.RunStatusFailed ||
		failed.Run.ErrorCode != "agent.llm_limit" || model.calls != 6 || len(executed) != 6 {
		t.Fatalf("hard ceiling unexpectedly bypassed: run=%#v calls=%d executed=%d err=%v",
			failed.Run, model.calls, len(executed), err)
	}
	view, err := agent.ViewState(failed)
	if err != nil || view.CallAllowance.LLMCalls != 6 || view.Budget.Limits.MaxLLMCalls != 6 ||
		view.Budget.Usage.LLMCalls != 6 || view.AutoAllowance.ModelRenewals != 2 {
		t.Fatalf("hard/segment ceilings diverged: %#v err=%v", view, err)
	}
}

func TestAutoAllowanceRejectsInvalidOptInWithoutDispatch(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	model := &allowanceModel{}
	var executed []string
	runner := allowanceRunner(t, runtime, approvals, model, &executed)
	invalid := []struct {
		allowance *agent.CallAllowance
		policy    agent.AutoAllowancePolicy
	}{
		{nil, agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1}},
		{&agent.CallAllowance{LLMCalls: 1}, agent.AutoAllowancePolicy{}},
		{&agent.CallAllowance{ToolCalls: 1}, agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 1, MaxNoProgressWindow: 1}},
		{&agent.CallAllowance{LLMCalls: 1}, agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 17, MaxNoProgressWindow: 1}},
		{&agent.CallAllowance{LLMCalls: 1}, agent.AutoAllowancePolicy{ModelStep: -1, MaxModelRenewals: 1, MaxNoProgressWindow: 1}},
		{&agent.CallAllowance{LLMCalls: 1}, agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 0, MaxNoProgressWindow: 1}},
		{&agent.CallAllowance{LLMCalls: 1}, agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 2, MaxNoProgressWindow: 0}},
		{&agent.CallAllowance{LLMCalls: 1}, agent.AutoAllowancePolicy{ModelStep: 7, MaxModelRenewals: 2, MaxNoProgressWindow: 1}},
	}
	for i, tc := range invalid {
		request := startRequest(fmt.Sprintf("invalid-auto-%d", i), fmt.Sprintf("invalid-auto-req-%d", i), "reject request")
		request.CallAllowance = tc.allowance
		request.AutoAllowance = &tc.policy
		_, err := runner.StartRun(t.Context(), request)
		if !errors.Is(err, agent.ErrInvalidCallAllowance) {
			t.Fatalf("invalid policy %d unexpectedly accepted: policy=%#v err=%v", i, tc.policy, err)
		}
	}
	if model.calls != 0 || len(executed) != 0 {
		t.Fatal("rejected automatic allowance must never dispatch external calls")
	}
}
