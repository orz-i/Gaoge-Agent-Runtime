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

type minimalAgentModel struct{ calls int }

func (m *minimalAgentModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	m.calls++
	return model.Response{Content: "done"}, nil
}

// A single Agent can be composed without Team, Workflow, Interactions,
// Relations, Scheduler, TurnFeed or a caller-provided Harness clock.
// None of these optional ports are silently installed as execution owners.
func TestMinimalAgentHarnessConstructionAndStableTurnAdmission(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	client := &minimalAgentModel{}
	loop, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: client})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: rt, Agent: loop, Store: harness.NewMemoryStore(),
		// Clock uses the system clock; no Context is enabled implicitly.
	})
	if err != nil {
		t.Fatal(err)
	}
	request := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "tenant/one"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "one"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "tenant/one"},
		Goal:       "One Agent task", Config: harness.ConfigSnapshot{Model: "fixture"},
	}
	first, err := host.StartAgentTurn(t.Context(), request)
	if err != nil || first.Turn.Status != harness.TurnCompleted || first.Turn.CreatedAt.IsZero() || client.calls != 1 {
		t.Fatalf("first StartAgentTurn: turn=%+v calls=%d err=%v", first.Turn, client.calls, err)
	}
	replayed, err := host.StartAgentTurn(t.Context(), request)
	if err != nil || replayed.Turn.ID != first.Turn.ID || client.calls != 1 {
		t.Fatalf("idempotent replay dispatched model: turn=%+v calls=%d err=%v", replayed.Turn, client.calls, err)
	}
	request.Goal = "Different goal for same HostTurn"
	if _, err := host.StartAgentTurn(t.Context(), request); !errors.Is(err, harness.ErrConflict) || client.calls != 1 {
		t.Fatalf("changed admission was not rejected: calls=%d err=%v", client.calls, err)
	}
	// Session Context remains explicit; omitting it must not silently create
	// a second transcript authority or a partial multi-turn model request.
	if _, err := host.OpenSession(harness.SessionOptions{
		HostThread: request.HostThread, Actor: request.Actor, Thread: request.Thread,
	}); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("missing Context Manager silently enabled Session: %v", err)
	}
}
