package harness_test

import (
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Even a failed Workflow Kernel Run does not prove that every independently
// routed Workflow Effect Run has reached an authoritative terminal outcome.
func TestPR82EffectfulFailedWorkflowRootCannotRetryWithoutDrainProof(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	feature := &retryTopLevelWorkflowFeature{runtime: runtime, failAfterCreate: true}
	store := harness.NewMemoryStore()
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: store, Clock: featureInvocationClock{}, Workflows: feature,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, startErr := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-workflow-no-drain-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-workflow-no-drain-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-workflow-no-drain-thread"},
			RequestID:  "effectful-workflow", Goal: "workflow can have effects",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Input: json.RawMessage(`{"goal":"effectful workflow"}`),
	})
	if !errors.Is(startErr, errRetryTopLevelWorkflow) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("fixture failed after actual Run creation: %+v %v", failed.Turn, startErr)
	}
	root, ok := harness.TopLevelInvocation(failed)
	if !ok {
		t.Fatal("root Invocation missing")
	}
	oldRun, err := runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || oldRun.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("missing actual physical Workflow Run: %+v %v", oldRun.Run, err)
	}
	if _, err = runner.RetryInvocation(t.Context(), root.ID); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("effectful Workflow was replayed without descendant-drain proof: %v", err)
	}
	after, err := store.GetInvocation(t.Context(), root.ID)
	if err != nil || after.Attempt != 1 || after.ExecutionRefID != root.ExecutionRefID || feature.starts != 1 {
		t.Fatalf("unsafe Workflow Retry mutated physical attempt: %+v starts=%d err=%v", after, feature.starts, err)
	}
}

// A completed child Team can still own live physical members. Its terminal
// Harness Invocation must not be made retryable by rewriting the same Attempt
// as Failed, even when its parent Turn remains active.
func TestPR82TerminalTeamChildCannotBeRewrittenIntoRetryableFailure(t *testing.T) {
	runner, turnID, parentItemID, _, _, store, _ := newFeatureInvocationHarness(t)
	started, err := runner.StartTeamInvocation(t.Context(), turnID, harness.TeamInvocationRequest{
		ParentItemID: parentItemID, RequestID: "pr82-terminal-team-child", Goal: "complete team",
		Mode: team.ExecutionSequential, Members: []team.Member{{ID: "member", Goal: "do work"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child := teamChildInvocation(t, started)
	if child.Status != harness.InvocationCompleted {
		t.Fatalf("expected terminal Harness child: %+v", child)
	}
	failed := child
	failed.Status = harness.InvocationFailed
	failed.UpdatedAt = featureInvocationClock{}.Now()
	if _, err = store.UpdateInvocation(t.Context(), failed, child.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("terminal child was rewritten into retryable Failed: %v", err)
	}
	if _, err = runner.RetryInvocation(t.Context(), child.ID); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("completed Team child obtained new Attempt: %v", err)
	}
	persisted, err := store.GetInvocation(t.Context(), child.ID)
	if err != nil || persisted.Status != harness.InvocationCompleted || persisted.Revision != child.Revision ||
		persisted.ExecutionRefID != child.ExecutionRefID || persisted.Attempt != child.Attempt {
		t.Fatalf("completed child lost its original Attempt: before=%+v after=%+v err=%v", child, persisted, err)
	}
}
