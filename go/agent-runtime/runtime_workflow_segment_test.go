package agentruntime

import (
	"strings"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestWorkflowSegmentYieldsAtNodeActivationLimit(t *testing.T) {
	now := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	root := model.WorkflowNode{
		ID: workflowRootScope, Type: model.WorkflowNodeSequence,
		Children: []model.WorkflowNode{
			{ID: "first", Type: model.WorkflowNodeSet, Assignments: map[string]model.WorkflowExpr{"first": workflowTestLiteral(true)}},
			{ID: "second", Type: model.WorkflowNodeSet, Assignments: map[string]model.WorkflowExpr{"second": workflowTestLiteral(true)}},
			{ID: "return", Type: model.WorkflowNodeReturn, Value: workflowExprPointer(workflowTestLiteral("done"))},
		},
	}
	runner := workflowCompensationTestRunner(root)
	runner.run.StartedAt = now
	runner.now = now
	runner.service.clock = &mutableRuntimeClock{now: now}
	runner.service.cfg = StaticConfigProvider(Config{Workflow: WorkflowConfig{
		MaxSegmentNodeActivations: 2,
		MaxSegmentDurationMS:      10_000,
		MaxSegmentEffects:         1,
		MaxSegmentTransitionBytes: 1 << 20,
	}})
	runner.segment = workflowSegmentState{}

	if err := runner.advanceRoot(); err != nil {
		t.Fatal(err)
	}
	if runner.segment.yieldReason != "node_activation_limit" {
		t.Fatalf("yield reason = %q", runner.segment.yieldReason)
	}
	if _, exists := runner.state.Activations["root/second"]; exists {
		t.Fatal("second node activated after the segment activation limit")
	}
	if runner.terminalOutcome != "" || runner.state.Returned {
		t.Fatal("yielded segment must not complete the workflow")
	}
}

func TestWorkflowSegmentYieldsAtWallClockAndTransitionSize(t *testing.T) {
	now := time.Date(2026, time.July, 28, 11, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		configure  func(*workflowRunner, *mutableRuntimeClock)
		wantReason string
	}{
		{
			name: "wall clock",
			configure: func(runner *workflowRunner, clock *mutableRuntimeClock) {
				runner.segment.policy.maxDuration = 50 * time.Millisecond
				clock.now = now.Add(51 * time.Millisecond)
			},
			wantReason: "wall_clock_limit",
		},
		{
			name: "transition bytes",
			configure: func(runner *workflowRunner, _ *mutableRuntimeClock) {
				runner.segment.policy.maxTransitionBytes = 128
				runner.state.Presentation = strings.Repeat("x", 1024)
			},
			wantReason: "transition_bytes_limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &mutableRuntimeClock{now: now}
			runner := workflowCompensationTestRunner(model.WorkflowNode{ID: "root", Type: model.WorkflowNodeSequence})
			runner.service.clock = clock
			runner.progress = true
			runner.segment = workflowSegmentState{
				startedAt: now,
				policy: workflowSegmentPolicy{
					maxNodeActivations: 100,
					maxDuration:        time.Hour,
					maxEffects:         1,
					maxTransitionBytes: 1 << 20,
				},
			}
			test.configure(&runner, clock)
			if !runner.shouldYieldWorkflowSegment() || runner.segment.yieldReason != test.wantReason {
				t.Fatalf("yield=%v reason=%q", runner.segment.yieldReason != "", runner.segment.yieldReason)
			}
		})
	}
}

func TestWorkflowDeadlinePreventsSuccessfulCompletion(t *testing.T) {
	startedAt := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	root := model.WorkflowNode{
		ID: "return", Type: model.WorkflowNodeReturn,
		Value: workflowExprPointer(workflowTestLiteral("too late")),
	}
	runner := workflowCompensationTestRunner(root)
	runner.run.StartedAt = startedAt
	runner.budget.Limits.MaxDurationSeconds = 60
	runner.service.clock = &mutableRuntimeClock{now: startedAt.Add(60 * time.Second)}
	runner.now = startedAt
	runner.segment = workflowSegmentState{}

	if err := runner.advanceRoot(); err != nil {
		t.Fatal(err)
	}
	if runner.terminalOutcome != model.TerminalFailed || runner.terminalCode != workflowFailureDurationExceeded {
		t.Fatalf("terminal outcome=%q code=%q", runner.terminalOutcome, runner.terminalCode)
	}
	if runner.result != nil || runner.state.Returned {
		t.Fatal("workflow completed after its deadline")
	}
}
