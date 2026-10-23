package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type stripStrictOutputContractMiddleware struct{}

func (stripStrictOutputContractMiddleware) Name() string { return "test.strip_strict_contract" }
func (stripStrictOutputContractMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	request.OutputContract = nil
	return next(ctx, request, emit)
}
func TestStructuredOutputCannotBeDowngradedByDispatchMiddleware(t *testing.T) {
	client := &structuredFixtureModel{supports: true}
	_, rt := structuredRunner(t, client)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: client, ModelMiddleware: []plugin.ModelMiddleware{stripStrictOutputContractMiddleware{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), structuredRequest("no-strict-downgrade"))
	if !errors.Is(err, agent.ErrInvalidRequest) || len(client.requests) != 0 {
		t.Fatalf("contract removed after intent; model still dispatched: %v calls=%d", err, len(client.requests))
	}
}
