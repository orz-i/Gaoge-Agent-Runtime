package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A model-correctable error is not by itself proof of a no-effect outcome:
// an external Tool may commit its side effect then fail the response.
func TestPostdispatchRecoverableToolErrorDoesNotCertifyNoEffect(t *testing.T) {
	backing := memory.NewStore()
	runtime := newModelInvocationRuntime(t, backing)
	physical := 0
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: "external.mutate", Name: "mutate_external_record",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			physical++
			return tools.ExecutionResult{}, tools.NewRecoverableCallError(
				"business.arguments_invalid_after_write", "retry with corrected arguments",
				errors.New("remote write already committed before validation response"),
			)
		}),
	}})
	client := &postdispatchRecoverableModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest(
		"tool-post-dispatch-error-run", "tool-post-dispatch-error-request",
		"write external record", "external.mutate",
	)
	_, startErr := runner.StartRun(t.Context(), request)
	if !errors.Is(startErr, agent.ErrToolOutcomeUnknown) || physical != 1 || client.calls != 1 {
		t.Fatalf("post-dispatch recoverable response falsely certifies no-effect: err=%v physical=%d model=%d",
			startErr, physical, client.calls)
	}
	pending, err := runtime.Load(t.Context(), request.ID)
	if err != nil || pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unverified external write lost durable Running state: state=%+v err=%v", pending.Run, err)
	}
	restarted, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, resumeErr := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if !errors.Is(resumeErr, agent.ErrToolOutcomeUnknown) || physical != 1 || client.calls != 1 {
		t.Fatalf("unverified write was physically replayed after restart: err=%v physical=%d model=%d",
			resumeErr, physical, client.calls)
	}
}

type postdispatchRecoverableModel struct{ calls int }

func (client *postdispatchRecoverableModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	client.calls++
	if client.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{
			ID: "remote-effect", ToolKey: "external.mutate", Arguments: json.RawMessage(`{}`),
		}}}, nil
	}
	return model.Response{Content: "unsafe correction reached model"}, nil
}
