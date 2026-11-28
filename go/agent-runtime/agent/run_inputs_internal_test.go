package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type invalidRunInputSource struct{ records []RunInput }

func (invalidRunInputSource) BindingID() string { return "inputs-v1" }
func (source invalidRunInputSource) ReadRunInputs(context.Context, kernel.Run, uint64, int) ([]RunInput, error) {
	return source.records, nil
}

func TestRunInputAdmissionRejectsMalformedBatchBeforeStateMutation(t *testing.T) {
	cases := [][]RunInput{
		{{Sequence: 2, ID: "gap", Text: "text"}},
		{{Sequence: 1, ID: "empty", Text: " "}},
		{{Sequence: 1, ID: "bad-\xff", Text: "text"}},
		{{Sequence: 1, ID: "large", Text: strings.Repeat("x", runInputTextLimit+1)}},
		{{Sequence: 1, ID: "same", Text: "first"}, {Sequence: 2, ID: "same", Text: "second"}},
		make([]RunInput, runInputBatchLimit+1),
	}
	for index, records := range cases {
		rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := NewRunner(Dependencies{Runtime: rt, Model: inputBoundaryModel{}, Inputs: invalidRunInputSource{records}})
		if err != nil {
			t.Fatal(err)
		}
		request := StartRequest{ID: "run", RequestID: "request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "thread"}, Goal: "start", Model: "test"}
		result, err := runner.StartRun(t.Context(), request)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("case %d: %v", index, err)
		}
		snapshot, err := rt.Load(t.Context(), result.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		state, err := ViewState(snapshot)
		if err != nil || state.InputCursor != 0 || len(state.ModelInvocations) != 0 {
			t.Fatalf("case %d changed state or dispatched Model: %v", index, err)
		}
	}
}

type inputBoundaryModel struct{}

func (inputBoundaryModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done"}, nil
}

func TestRunInputSourceIsClassifiedAsHostCallbackBoundary(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	inputs, _ := NewMemoryRunInputs("inputs-v1")
	runner, err := NewRunner(Dependencies{Runtime: runtime, Model: inputBoundaryModel{}, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if runner.kernelOwnedStartCancellation || !runner.hasRunStages {
		t.Fatal("Host input source was classified as native callback-free execution")
	}
	if _, err = NewRunner(Dependencies{Runtime: runtime, Model: inputBoundaryModel{}, Inputs: inputs, BoundedContext: &BoundedContextAdapter{}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Host input bypassed the managed-transfer boundary: %v", err)
	}
}

func TestRunInputControlCustodyDependsOnActualRead(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := NewMemoryRunInputs("inputs-v1")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: rt, Model: inputBoundaryModel{}, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"read", "disabled"} {
		disabled := id == "disabled"
		req := StartRequest{ID: id, RequestID: id, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "thread"}, Goal: "start", Model: "test", DisableInputs: disabled}
		final, err := runner.StartRun(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if got := runner.VerifiedDeferredRootControl(t.Context(), rt, final); got != disabled {
			t.Fatalf("%s: control custody = %t, want %t", id, got, disabled)
		}
	}
}
