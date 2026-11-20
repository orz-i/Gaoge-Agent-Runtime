package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestToolAgentSessionOptionalOutputCeilingRequiresWindowReservation(t *testing.T) {
	for _, test := range []struct {
		name        string
		ceiling     int64
		reservation int64
		reject      bool
	}{
		{name: "legacy unspecified", ceiling: 0, reservation: 0},
		{name: "positive frozen ceiling", ceiling: 128, reservation: 128},
		{name: "insufficient reservation", ceiling: 128, reservation: 127, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			modelCalls, toolCalls, resolves := 0, 0, 0
			client := toolSessionModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
				modelCalls++
				if request.MaxOutputTokens != test.ceiling {
					t.Fatalf("lost original output ceiling: got=%d want=%d", request.MaxOutputTokens, test.ceiling)
				}
				return model.Response{Content: "done"}, nil
			})
			options := toolSessionOptions(t, client)
			options.Session.Config.MaxModelOutputTokens = test.ceiling
			options.ResolveModelWindow = func(_ context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
				resolves++
				if request.MaxOutputTokens != test.ceiling {
					t.Fatal("resolver did not receive the frozen request")
				}
				return runtimecontext.ModelWindow{ContextTokens: 32768, ReservedOutputTokens: test.reservation}, nil
			}
			options.Tools[0].Registration.Handler = tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
				toolCalls++
				return tools.ExecutionResult{}, nil
			})
			session, err := harness.OpenToolAgentSession(options)
			if err != nil || resolves != 0 || modelCalls != 0 || toolCalls != 0 {
				t.Fatalf("constructor performed effects or resolved model capacity: err=%v resolves=%d models=%d tools=%d", err, resolves, modelCalls, toolCalls)
			}
			result, err := session.Prompt(t.Context(), "capacity", "Finish the requested task")
			if test.reject {
				if !errors.Is(err, harness.ErrInvalidRequest) || resolves != 1 || modelCalls != 0 || toolCalls != 0 {
					t.Fatalf("insufficient headroom reached effects: err=%v resolves=%d models=%d tools=%d", err, resolves, modelCalls, toolCalls)
				}
				return
			}
			if err != nil || result.Turn.Status != harness.TurnCompleted || modelCalls != 1 || toolCalls != 0 {
				t.Fatalf("valid reservation changed facade behavior: err=%v status=%s models=%d tools=%d", err, result.Turn.Status, modelCalls, toolCalls)
			}
		})
	}
}
