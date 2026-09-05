package planexecute_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
)

type waitingPlanner struct {
	recordingPlanner
	waiting bool
}
type plannerWaitError struct{}

func (plannerWaitError) Error() string   { return "shared budget temporarily reserved" }
func (plannerWaitError) Retryable() bool { return true }
func (planner *waitingPlanner) GeneratePlan(ctx context.Context, request planexecute.PlannerRequest) (planexecute.PlannerResponse, error) {
	if !planner.waiting {
		planner.waiting = true
		return planexecute.PlannerResponse{}, plannerWaitError{}
	}
	return planner.recordingPlanner.GeneratePlan(ctx, request)
}

func TestPlannerBudgetWaitKeepsDurableInvocationForContinuation(t *testing.T) {
	store := memory.NewStore()
	runtime := newPlannerFaultRuntime(t, store, planFaultClock{})
	planner := &waitingPlanner{}
	runner := newPlannerFaultRunner(t, runtime, planner, newFakeAgentRunner(childComplete), nil)
	snapshot, err := runner.StartRun(t.Context(), baseStartRequest(planexecute.ApprovalAuto))
	if !errors.Is(err, planexecute.ErrPlannerPending) || snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("waiting became terminal: %+v %v", snapshot.Run, err)
	}
	restarted := newPlannerFaultRunner(t, runtime, planner, newFakeAgentRunner(childComplete), nil)
	completed, err := restarted.Resume(t.Context(), snapshot.Run.ID, snapshot.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertPlannerConsumed(t, completed, &planner.recordingPlanner, 1)
}
