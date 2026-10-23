package harness_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestStartRejectsAnotherRootRequestID(t *testing.T) {
	for _, continuing := range []bool{true, false} {
		t.Run(fmt.Sprintf("continuing=%v", continuing), func(t *testing.T) {
			client := &rootInvocationModel{continuing: continuing}
			runner := newHarnessRunnerWithModel(t, client)
			request := testStartRequest()
			request.RequestID = "original-root"
			first, err := runner.Start(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			request.RequestID = "another-root"
			if _, err = runner.Start(t.Context(), request); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("different root request error=%v", err)
			}
			request.RequestID = "original-root"
			replayed, err := runner.Start(t.Context(), request)
			if err != nil || len(replayed.Invocations) != 1 || replayed.Invocations[0].ID != first.Invocations[0].ID ||
				replayed.Turn.Status != first.Turn.Status || client.calls.Load() != 1 {
				t.Fatalf("root changed or redispatched: snapshot=%#v calls=%d err=%v", replayed, client.calls.Load(), err)
			}
		})
	}
}

func TestConcurrentStartDispatchesOneRoot(t *testing.T) {
	client := &rootInvocationModel{continuing: true}
	runner := newHarnessRunnerWithModel(t, client)
	const writers = 8
	start := make(chan struct{})
	results := make(chan error, writers)
	for index := range writers {
		go func() {
			<-start
			request := testStartRequest()
			request.RequestID = fmt.Sprintf("competing-root-%d", index)
			_, err := runner.Start(t.Context(), request)
			results <- err
		}()
	}
	close(start)
	winners := 0
	for range writers {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("unexpected concurrent start error: %v", err)
		}
	}
	request := testStartRequest()
	sessionID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := harness.TurnID(sessionID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.Refresh(t.Context(), turnID)
	if err != nil || winners != 1 || client.calls.Load() != 1 || len(snapshot.Invocations) != 1 {
		t.Fatalf("competing roots: winners=%d calls=%d invocations=%#v err=%v", winners, client.calls.Load(), snapshot.Invocations, err)
	}
}

func TestCommandTurnRejectsAnotherRootCapability(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: unusedFeatureAgent{}, Store: harness.NewMemoryStore(), Clock: fixedClock{},
		Workflows: completedWorkflowFeature{runtime: runtime}, Teams: completedTeamFeature{runtime: runtime},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	request.RequestID = "workflow-root"
	first, err := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{StartRequest: request})
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "team-root"
	if _, err = runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{StartRequest: request}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("different root capability error=%v", err)
	}
	invocationID, err := harness.InvocationID(first.Turn.ID, "", harness.CapabilityTeam, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Load(t.Context(), harness.CapabilityExecutionRefID(invocationID)); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("conflicting command dispatched a Run: %v", err)
	}
	request.RequestID = "workflow-root"
	replayed, err := runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{StartRequest: request})
	if err != nil || len(replayed.Invocations) != 1 || replayed.Invocations[0].ID != first.Invocations[0].ID {
		t.Fatalf("original command replay: snapshot=%#v err=%v", replayed, err)
	}
}

type rootInvocationModel struct {
	calls      atomic.Int64
	continuing bool
}

func (client *rootInvocationModel) Generate(context.Context, model.Request) (model.Response, error) {
	client.calls.Add(1)
	return model.Response{Content: "root response", Continue: client.continuing}, nil
}
