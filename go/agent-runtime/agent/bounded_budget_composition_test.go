package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
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
		{"retired triple registration", []plugin.RunMiddleware{original}, []plugin.ModelMiddleware{original}, []plugin.ToolMiddleware{original}, false},
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

func TestFirstPartyOwnerSelectionUsesExplicitStages(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), &managedScopePorts{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBoundedBudgetAdapter(budget.NewMemoryLedgerStore(), &receiptBudgetPorts{scope: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{}, BoundedContext: c, BoundedBudget: b})
	if err != nil || r.boundedContext != c || r.nativeBudget != b || r.samplingBudget != b || r.toolBudget != b ||
		r.prepareModelRequest != nil || !r.receiptOnlyRunChain || r.kernelOwnedStartCancellation || len(r.boundedCallbacks) != 2 {
		t.Fatalf("fixed stages did not select one original owner: %v", err)
	}
	for _, d := range []Dependencies{
		{BoundedContext: c, BoundedBudget: b, RunMiddleware: []plugin.RunMiddleware{b}},
		{BoundedContext: c, BoundedBudget: b, ModelMiddleware: []plugin.ModelMiddleware{b}},
		{BoundedContext: c, BoundedBudget: b, ToolMiddleware: []plugin.ToolMiddleware{b}},
		{BoundedContext: c, BoundedBudget: b, ManagedRecovery: true},
	} {
		d.Runtime, d.Model = rt, boundBudgetCompositionModel{}
		if _, err := NewRunner(d); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("duplicate or unproved topology accepted: %v", err)
		}
	}
	// The old mixed/partial extension remains in its actual chain. It cannot
	// acquire native budget run custody merely by selecting a Model adapter.
	r, err = NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{}, BoundedContext: c, ModelMiddleware: []plugin.ModelMiddleware{b}})
	if err != nil || r.nativeBudget != nil || r.samplingBudget != b || r.toolBudget != nil || r.receiptOnlyRunChain {
		t.Fatalf("partial chain reclassified as complete: %v", err)
	}
	if _, err := NewRunner(Dependencies{Runtime: rt, Model: boundBudgetCompositionModel{}, BoundedContext: c, ManagedRecovery: true}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unregistered custom Context certified managed recovery: %v", err)
	}
}
