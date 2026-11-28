package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type summaryRejectedInputs struct{ bindingCalls, reads int }

func (inputs *summaryRejectedInputs) BindingID() string {
	inputs.bindingCalls++
	return "summary-inputs"
}

func (inputs *summaryRejectedInputs) ReadRunInputs(context.Context, kernel.Run, uint64, int) ([]agent.RunInput, error) {
	inputs.reads++
	return nil, nil
}

func TestContextSummaryNativeBindsHostInputsWithRealBridges(t *testing.T) {
	store := harness.NewMemoryStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), ManagedRecoveryHost: store})
	if err != nil {
		t.Fatal(err)
	}
	resolved := 0
	resolve := func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
		resolved++
		return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 128}, nil
	}
	bounded, err := harness.NewBoundedContextAdapter(store, runtimecontext.NewManager(runtimecontext.Dependencies{}), resolve, nil)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := harness.NewContextSummaryAdapter(store, runtime, bounded, resolve)
	if err != nil {
		t.Fatal(err)
	}
	client := &summarySessionModel{}
	dependencies := agent.Dependencies{Runtime: runtime, Model: client,
		ContextSummary: summary}
	if _, err = agent.NewRunner(dependencies); err != nil {
		t.Fatalf("the same native composition without Host inputs must be valid: %v", err)
	}
	legacy := dependencies
	legacy.PrepareModelRequest = harness.MaterializeContextWindowRequest
	if _, err = agent.NewRunner(legacy); !errors.Is(err, agent.ErrInvalidRequest) {
		t.Fatalf("summary accepted competing Host preparation: %v", err)
	}
	inputs := &summaryRejectedInputs{}
	dependencies.Inputs = inputs
	if _, err = agent.NewRunner(dependencies); err != nil {
		t.Fatalf("exact managed Host input composition rejected: %v", err)
	}
	if inputs.bindingCalls != 1 || inputs.reads != 0 || resolved != 0 || len(client.requests) != 0 {
		t.Fatal("construction read a queue or called Provider")
	}
}
