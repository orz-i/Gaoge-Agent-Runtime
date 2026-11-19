package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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
		_, state, changed, err := runner.admitRunInputs(t.Context(), kernel.Snapshot{}, original, nil)
		if !errors.Is(err, ErrInvalidRequest) || changed || state.InputCursor != 0 || len(state.Messages) != 0 {
			t.Fatalf("case %d changed state for invalid input: %v changed=%t", index, err, changed)
		}
	}
}
