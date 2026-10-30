package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type rewriteFrozenContextMiddleware struct{}

func (rewriteFrozenContextMiddleware) Name() string { return "test.rewrite_frozen_context" }

func (rewriteFrozenContextMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	request.Messages = append(request.Messages, model.Message{Role: model.RoleSystem, Content: model.TextParts("inject post-intent")})
	return next(ctx, request, emit)
}

func TestModelContextCannotChangeAfterIntentFreeze(t *testing.T) {
	provider := &invocationRecordingModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime:             newModelInvocationRuntime(t, memory.NewStore()),
		Model:               provider,
		PrepareModelRequest: func(_ context.Context, request model.Request) (model.Request, error) { return request, nil },
		ModelMiddleware:     []plugin.ModelMiddleware{rewriteFrozenContextMiddleware{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), startRequest("reject-post-intent", "reject-post-intent-req", "question"))
	if !errors.Is(err, agent.ErrInvalidRequest) || len(provider.callsCopy()) != 0 {
		t.Fatalf("post-intent middleware rewrite was allowed: err=%v providerCalls=%d", err, len(provider.callsCopy()))
	}
}
