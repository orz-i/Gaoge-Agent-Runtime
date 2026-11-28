package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type summaryInputsModel struct {
	*summaryCoreModel
	before func(context.Context, model.Request)
}

func (provider summaryInputsModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	provider.before(ctx, request)
	return provider.summaryCoreModel.Generate(ctx, request)
}

func TestContextSummaryHostInputsApplyOnlyBeforeNextBusinessIntent(t *testing.T) {
	_, runtime, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
	inputs, err := NewMemoryRunInputs("summary-inputs")
	if err != nil {
		t.Fatal(err)
	}
	summaries, business := 0, 0
	client := summaryInputsModel{summaryCoreModel: provider}
	client.before = func(ctx context.Context, incoming model.Request) {
		isSummary := incoming.OutputContract != nil && incoming.OutputContract.Name == "working_state_summary"
		if isSummary {
			summaries++
		} else {
			business++
			want := "first queued direction"
			if business == 2 {
				want = "second queued direction"
			}
			last := incoming.Messages[len(incoming.Messages)-1]
			if last.Role != model.RoleUser || model.TextValue(last.Content) != want {
				t.Fatalf("queued input was not admitted before the next business intent: got=%+v want=%q", last, want)
			}
		}
		if !isSummary && business != 1 {
			return
		}
		snapshot, err := runtime.Load(ctx, incoming.RunID)
		if err != nil {
			t.Fatal(err)
		}
		original := append(json.RawMessage(nil), snapshot.State...)
		id, text := "input-1", "first queued direction"
		if !isSummary {
			id, text = "input-2", "second queued direction"
		}
		if _, err = inputs.Submit(ctx, snapshot.Run, id, text); err != nil {
			t.Fatal(err)
		}
		after, err := runtime.Load(ctx, incoming.RunID)
		if err != nil || after.Run.Revision != snapshot.Run.Revision || !reflect.DeepEqual(after.State, original) {
			t.Fatal("queuing input changed the dispatched frozen intent")
		}
		view, err := ViewState(after)
		if err != nil || len(view.ModelInvocations) == 0 || !reflect.DeepEqual(view.ModelInvocations[len(view.ModelInvocations)-1].Request, incoming) {
			t.Fatal("queue submission rewrote the request already seen by Generate")
		}
	}
	bounded, err := NewBoundedContextAdapter(ports.manager, ports)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := NewContextSummaryAdapter(bounded, ports)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: runtime, Model: client, Clock: clock, Inputs: inputs,
		RunMiddleware: []plugin.RunMiddleware{bounded}, ContextSummary: summary})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ViewState(result)
	if err != nil || result.Run.Status != kernel.RunStatusCompleted || summaries != 1 || business != 2 ||
		view.InputBinding != "summary-inputs" || view.InputCursor != 2 || view.Budget.Usage.LLMCalls != 3 ||
		view.Budget.Usage.TotalTokens != 30 || len(view.Messages) != 5 || len(view.ModelInvocations) != 3 {
		t.Fatalf("input/summary boundary changed completion or accounting: err=%v calls=%d/%d view=%+v", err, summaries, business, view)
	}
	for index, invocation := range view.ModelInvocations {
		if invocation.Status != ModelInvocationConsumed || (index == 0) != (invocation.Purpose == ModelInvocationContextSummary) {
			t.Fatal("summary receipt entered the business lifecycle or was consumed twice")
		}
	}
	for _, message := range view.Messages {
		if model.TextValue(message.Content) == summaryCoreDocument {
			t.Fatal("summary became an assistant answer while applying Host input")
		}
	}
}

func TestContextSummaryNativeInputStateRejectedBeforePark(t *testing.T) {
	for _, mode := range []string{"binding", "binding and cursor", "cursor"} {
		t.Run(mode, func(t *testing.T) {
			runner, trap := summaryNativePointFixture(t, true)
			state, err := decodeState(trap.current.State)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "cursor" {
				state.InputBinding = "summary-inputs"
			}
			if mode != "binding" {
				state.InputCursor = 1
			}
			// A new profile can freeze a legitimate input binding; the old
			// summary proof cannot be reinterpreted as that profile even
			// when the altered state has its own valid aggregate seal.
			raw, err := encodeState(state)
			if err != nil {
				t.Fatal(err)
			}
			trap.current.State, trap.transfer.SafePoint.Source.State = raw, append(json.RawMessage(nil), raw...)
			_, err = decodeState(raw)
			if mode == "cursor" && !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("cursor without binding was decoded: %v", err)
			}
			_, _, err = runner.RecoverManagedContinuation(t.Context(), trap.transfer)
			if !errors.Is(err, ErrInvalidRequest) || trap.admissions != 0 || !bytes.Equal(trap.current.State, raw) {
				t.Fatalf("invalid mixed frame reached PARK: err=%v admissions=%d", err, trap.admissions)
			}
		})
	}
}
