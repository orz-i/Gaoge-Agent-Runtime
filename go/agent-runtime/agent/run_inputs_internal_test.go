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
		runner := Runner{inputs: invalidRunInputSource{records}, inputBinding: "inputs-v1"}
		original := runState{InputBinding: "inputs-v1"}
		ctx := context.WithValue(t.Context(), runInputScopeKey{}, "run")
		_, state, changed, err := runner.admitRunInputs(ctx, kernel.Snapshot{Run: kernel.Run{ID: "run"}}, original, nil)
		if !errors.Is(err, ErrInvalidRequest) || changed || state.InputCursor != 0 || len(state.Messages) != 0 {
			t.Fatalf("case %d changed state for invalid input: %v changed=%t", index, err, changed)
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
	if runner.kernelOwnedStartCancellation || !runner.runMiddlewareConfigured {
		t.Fatal("Host input source was classified as native callback-free execution")
	}
	if _, err = NewRunner(Dependencies{Runtime: runtime, Model: inputBoundaryModel{}, Inputs: inputs, ManagedContextRecovery: &BoundedContextAdapter{}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Host input bypassed the managed-transfer boundary: %v", err)
	}
}
