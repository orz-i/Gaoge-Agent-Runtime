package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func newApprovalRecoveryRun(t *testing.T) (*agent.Runner, *kernel.Runtime, *interaction.Approvals, kernel.Snapshot, *int) {
	t.Helper()
	runtime, approvals := newTestRuntimeAndApprovals(t)
	executions := new(int)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: publishToolKey, Name: publishToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			*executions++
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: committedDisposition},
			}, nil
		}),
	}})
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: &approvalModel{}, Catalog: registry, Executor: registry,
		Approvals:        interactionadapter.New(approvals),
		ApprovalPolicies: []plugin.ApprovalPolicy{requiredApprovalPolicy{name: "required"}},
		DeferResumption:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := runner.StartRun(t.Context(), startRequest("approval_recovery", "request_approval_recovery",
		"approve tool", publishToolKey))
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("start: %+v, %v", waiting.Run, err)
	}
	return runner, runtime, approvals, waiting, executions
}

// A pre-upgrade checkpoint without a persisted Tool approval binding must
// NEVER authorize a Tool even if its resolved payload matches the pending call.
func TestDeferredApprovalMissingBindingCannotRecoverGrantedTool(t *testing.T) {
	runner, runtime, approvals, waiting, executions := newApprovalRecoveryRun(t)
	legacyState := approvalStateEdit(t, waiting.State, func(state map[string]json.RawMessage) {
		delete(state, "pendingApproval")
	})
	checkpoint, err := approvals.Resolve(waiting.Checkpoint, interaction.ApprovalResponse{Decision: interaction.DecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	running, err := runtime.Apply(t.Context(), waiting.Run.ID, waiting.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: legacyState, Checkpoint: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := runner.Resume(t.Context(), running.Run.ID, running.Run.Revision)
	if err != nil || done.Run.Status != kernel.RunStatusWaitingInput || *executions != 0 ||
		done.Checkpoint == nil || done.Checkpoint.Status != kernel.CheckpointPending {
		t.Fatalf("missing binding authorized Tool instead of failing closed: %+v, checkpoint=%+v executions=%d err=%v",
			done.Run, done.Checkpoint, *executions, err)
	}
}

func TestDeferredApprovalRejectsMismatchedOrRejectedGrant(t *testing.T) {
	for _, testcase := range []string{"call-changed", "payload-changed", "decision-rejected"} {
		t.Run(testcase, func(t *testing.T) {
			runner, runtime, approvals, waiting, executions := newApprovalRecoveryRun(t)
			decision := interaction.DecisionApprove
			if testcase == "decision-rejected" {
				decision = interaction.DecisionReject
			}
			checkpoint, err := approvals.Resolve(waiting.Checkpoint, interaction.ApprovalResponse{Decision: decision})
			if err != nil {
				t.Fatal(err)
			}
			state := waiting.State
			if testcase == "call-changed" {
				state = approvalStateEdit(t, waiting.State, func(fields map[string]json.RawMessage) {
					var pending []tools.Call
					if err := json.Unmarshal(fields["pendingCalls"], &pending); err != nil {
						t.Fatal(err)
					}
					pending[0].ID = "forged-second-call"
					fields["pendingCalls"], err = json.Marshal(pending)
					if err != nil {
						t.Fatal(err)
					}
				})
			}
			if testcase == "payload-changed" {
				checkpoint.Payload = json.RawMessage(`{"toolCallID":"different","toolKey":"story.publish_change_set","arguments":{}}`)
			}
			running, err := runtime.Apply(t.Context(), waiting.Run.ID, waiting.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusRunning, State: state, Checkpoint: checkpoint,
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Resume(t.Context(), running.Run.ID, running.Run.Revision)
			if !errors.Is(err, agent.ErrApprovalRequired) || result.Run.Status != kernel.RunStatusFailed || *executions != 0 {
				t.Fatalf("%s unexpectedly authorized tool: run=%+v executions=%d err=%v",
					testcase, result.Run, *executions, err)
			}
		})
	}
}

func approvalStateEdit(t *testing.T, encoded json.RawMessage, edit func(map[string]json.RawMessage)) json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	edit(fields)
	updated, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// A resolved checkpoint without its durable binding cannot authorize another
// Tool, even when call ID and arguments are reused.
func TestDeferredApprovalMissingBindingCannotBeReused(t *testing.T) {
	runner, runtime, approvals, waiting, executions := newApprovalRecoveryRun(t)
	legacyState := approvalStateEdit(t, waiting.State, func(fields map[string]json.RawMessage) {
		delete(fields, "pendingApproval")
		var messages []runtimemodel.Message
		if err := json.Unmarshal(fields["messages"], &messages); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, runtimemodel.Message{
			Role: runtimemodel.RoleTool, ToolCallID: "approval_call", Content: `{"previous":true}`,
		})
		var err error
		fields["messages"], err = json.Marshal(messages)
		if err != nil {
			t.Fatal(err)
		}
	})
	checkpoint, err := approvals.Resolve(waiting.Checkpoint, interaction.ApprovalResponse{Decision: interaction.DecisionApprove})
	if err != nil {
		t.Fatal(err)
	}
	running, err := runtime.Apply(t.Context(), waiting.Run.ID, waiting.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: legacyState, Checkpoint: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := runner.Resume(t.Context(), running.Run.ID, running.Run.Revision)
	if err != nil || next.Run.Status != kernel.RunStatusWaitingInput || *executions != 0 {
		t.Fatalf("stale legacy grant reused: run=%+v executions=%d err=%v", next.Run, *executions, err)
	}
}
