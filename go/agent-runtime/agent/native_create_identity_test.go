package agent_test

import (
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// An indeterminate CREATE with an SDK-minted Run ID cannot be safely retried
// when the caller never received that ID. Native durability therefore
// requires a host-provided stable Run ID before any model/tool dispatch.
func TestNativeStartRequiresCallerStableRunID(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: modelClient,
		RunExecution: &agent.RunExecution{OwnerID: "stable-create", LeaseDuration: 3 * time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "stable create",
	}
	if _, err = runner.StartRun(t.Context(), request); !errors.Is(err, agent.ErrInvalidRequest) ||
		modelClient.calls != 0 {
		t.Fatalf("native start minted an unobservable ID: err=%v model=%d", err, modelClient.calls)
	}
	request.ID = "stable-create-id"
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || modelClient.calls != 1 {
		t.Fatalf("stable native start failed: run=%+v err=%v model=%d", completed.Run, err, modelClient.calls)
	}
	if _, err = runner.StartRun(t.Context(), request); !errors.Is(err, kernel.ErrAlreadyExists) ||
		modelClient.calls != 1 {
		t.Fatalf("duplicate stable ID dispatched a second logical Run: err=%v model=%d", err, modelClient.calls)
	}
}
