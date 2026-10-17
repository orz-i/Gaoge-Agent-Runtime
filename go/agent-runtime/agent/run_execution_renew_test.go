package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type advancementClock struct {
	mu      sync.Mutex
	instant time.Time
}

func (c *advancementClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.instant
}
func (c *advancementClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.instant = c.instant.Add(duration)
}

type progressingModel struct {
	clock *advancementClock
	calls int
}

func (m *progressingModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	m.calls++
	m.clock.Advance(75 * time.Second)
	if m.calls <= 2 {
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("tool-%d", m.calls),
			ToolKey: "increment", Arguments: json.RawMessage(`{}`)}},
			Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func TestNativeRunExecutionRenewsOneSegmentAcrossManySteps(t *testing.T) {
	clock := &advancementClock{instant: time.Now().UTC().Truncate(time.Millisecond)}
	store := memory.NewStoreWithExecutionClock(clock)
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	m := &progressingModel{clock: clock}
	toolsCalled := 0
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "increment", Name: "Increment", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolsCalled++
			clock.Advance(30 * time.Second)
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	originalClock := clock.Now()
	config := &agent.RunExecution{OwnerID: "one-worker", LeaseDuration: 2 * time.Minute}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m, Catalog: registry, Executor: registry,
		RunExecution: config, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "many-steps", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "multiple models and tools",
		ToolKeys: []string{"increment"},
	})
	if err != nil || final.Run.Status != kernel.RunStatusCompleted ||
		m.calls != 3 || toolsCalled != 2 {
		t.Fatalf("native segment not renewed: status=%s models=%d tools=%d err=%v", final.Run.Status, m.calls, toolsCalled, err)
	}
	elapsed := clock.Now().Sub(originalClock)
	if elapsed <= config.LeaseDuration*2 {
		t.Fatalf("synthetic Run was not long enough to test renewal: %s", elapsed)
	}
	if err := rt.RequireExecution(t.Context(), final.Run.ID); !errors.Is(err, kernel.ErrTerminal) {
		t.Fatalf("Run not completed after long renewed segment: %v", err)
	}
}
