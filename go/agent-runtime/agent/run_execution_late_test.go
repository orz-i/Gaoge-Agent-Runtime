package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type lateModel struct {
	calls      int
	delayed    bool
	invokeTool bool
}

func (m *lateModel) Generate(_ context.Context, req model.Request) (model.Response, error) {
	m.calls++
	if m.delayed {
		time.Sleep(40 * time.Millisecond)
	}
	for _, msg := range req.Messages {
		if msg.Role == model.RoleTool {
			return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
		}
	}
	if m.invokeTool {
		return model.Response{ToolCalls: []tools.Call{{ID: "late-call", ToolKey: "slow", Arguments: json.RawMessage(`{}`)}},
			Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
	}
	return model.Response{Content: "late but known", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

func TestNativeLateKnownModelResponseIsReceiptedNotRepeated(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	m := &lateModel{delayed: true}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m,
		RunExecution: &agent.RunExecution{OwnerID: "late-model", LeaseDuration: 2 * time.Minute},
		Execution: agent.ExecutionPolicy{
			Model: agent.CallPolicy{Timeout: 10 * time.Millisecond, MaxAttempts: 1},
			Tool:  agent.CallPolicy{Timeout: 10 * time.Millisecond, MaxAttempts: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, e := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "late-known-model", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "deadline", Model: "fixture",
	})
	if !errors.Is(e, agent.ErrModelLateResult) || first.Run.Status != kernel.RunStatusRunning || m.calls != 1 {
		t.Fatalf("late success misreported: run=%+v calls=%d err=%v", first.Run, m.calls, e)
	}
	done, e := runner.Resume(t.Context(), first.Run.ID, first.Run.Revision)
	if e != nil || done.Run.Status != kernel.RunStatusCompleted || m.calls != 1 {
		t.Fatalf("known late Model response was invoked again: %+v calls=%d err=%v", done.Run, m.calls, e)
	}
}

func TestNativeLateKnownToolReceiptIsPersistedNotRepeated(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	m := &lateModel{invokeTool: true}
	toolsCalled := 0
	catalog, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "slow", Name: "Slow", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			toolsCalled++
			time.Sleep(40 * time.Millisecond) // ignores Context; effect already happened
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`),
				Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: m, Catalog: catalog, Executor: catalog,
		RunExecution: &agent.RunExecution{OwnerID: "late-tool", LeaseDuration: 2 * time.Minute},
		Execution: agent.ExecutionPolicy{
			Model: agent.CallPolicy{Timeout: 10 * time.Millisecond, MaxAttempts: 1},
			Tool:  agent.CallPolicy{Timeout: 10 * time.Millisecond, MaxAttempts: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, e := runner.StartRun(t.Context(), agent.StartRequest{
		ID: "late-known-tool", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "timeout", ToolKeys: []string{"slow"},
	})
	if !errors.Is(e, agent.ErrToolLateResult) || first.Run.Status != kernel.RunStatusRunning || toolsCalled != 1 {
		t.Fatalf("late Tool receipt not marked as warning: %+v calls=%d err=%v", first.Run, toolsCalled, e)
	}
	done, e := runner.Resume(t.Context(), first.Run.ID, first.Run.Revision)
	if e != nil || done.Run.Status != kernel.RunStatusCompleted || toolsCalled != 1 || m.calls != 2 {
		t.Fatalf("late Tool result replayed: %+v tools=%d models=%d err=%v", done.Run, toolsCalled, m.calls, e)
	}
}
