package harness_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type rootEnvelopeCountedModel struct{ calls atomic.Int32 }

func (m *rootEnvelopeCountedModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls.Add(1)
	return model.Response{Content: "done"}, nil
}

// Agent and composite top-level starts share one sealed Turn admission.
// A changed Feature cannot reinterpret an already admitted Agent Turn, and
// exact Agent replay must not invoke a second physical model request.
func TestHardcutTopLevelEnvelopeCannotSwitchAgentToTeam(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	client := &rootEnvelopeCountedModel{}
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loop, Teams: completedTeamFeature{runtime},
		Store: harness.NewMemoryStore(), Clock: fixedClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := testStartRequest()
	first, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil || first.Turn.Status != harness.TurnCompleted || client.calls.Load() != 1 {
		t.Fatalf("start Agent: status=%s err=%v calls=%d", first.Turn.Status, err, client.calls.Load())
	}
	_, err = runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: request,
		Mode:         team.ExecutionSequential,
		Members:      []team.Member{{ID: "writer", Goal: "other execution"}},
	})
	if !errors.Is(err, harness.ErrConflict) || client.calls.Load() != 1 {
		t.Fatalf("same Turn switched Feature: err=%v calls=%d", err, client.calls.Load())
	}
	replayed, err := runner.StartAgentTurn(t.Context(), request)
	if err != nil || replayed.Turn.ID != first.Turn.ID ||
		replayed.Turn.Status != harness.TurnCompleted || client.calls.Load() != 1 {
		t.Fatalf("exact replay: status=%s err=%v calls=%d", replayed.Turn.Status, err, client.calls.Load())
	}
}
