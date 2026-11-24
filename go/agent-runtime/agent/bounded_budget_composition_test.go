package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type boundBudgetCompositionModel struct{}

func (boundBudgetCompositionModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "never called"}, nil
}

func TestBoundedBudgetCompositionHasOneOriginalAdapterAcrossAllCallbackLayers(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	source := budget.NewMemoryLedgerStore()
	original, err := NewBoundedBudgetAdapter(source, &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewBoundedBudgetAdapter(source, &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		run   []plugin.RunMiddleware
		model []plugin.ModelMiddleware
		tool  []plugin.ToolMiddleware
		valid bool
	}{
		{"same adapter in all layers", []plugin.RunMiddleware{original}, []plugin.ModelMiddleware{original}, []plugin.ToolMiddleware{original}, true},
		{"model-only explicit native adapter", nil, []plugin.ModelMiddleware{original}, nil, true},
		{"unmetered stand-alone Agent", nil, nil, nil, true},
		{"foreign model adapter", []plugin.RunMiddleware{original}, []plugin.ModelMiddleware{other}, nil, false},
		{"foreign Tool adapter", []plugin.RunMiddleware{original}, nil, []plugin.ToolMiddleware{other}, false},
		{"duplicate Run adapter", []plugin.RunMiddleware{original, original}, nil, nil, false},
		{"duplicate Model adapter", nil, []plugin.ModelMiddleware{original, original}, nil, false},
		{"duplicate Tool adapter", nil, nil, []plugin.ToolMiddleware{original, original}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, e := NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{},
				RunMiddleware: tc.run, ModelMiddleware: tc.model, ToolMiddleware: tc.tool})
			if tc.valid && (e != nil || runner == nil) {
				t.Fatalf("valid single budget rejected: %v", e)
			}
			if !tc.valid && !errors.Is(e, ErrInvalidRequest) {
				t.Fatalf("foreign/duplicate budget admitted: %v", e)
			}
		})
	}
}
