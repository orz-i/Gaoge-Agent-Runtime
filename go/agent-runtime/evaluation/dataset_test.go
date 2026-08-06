package evaluation_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/evaluation"
)

const (
	dimensionAccuracy    = "accuracy"
	dimensionHelpfulness = "helpfulness"
	targetCandidate      = "candidate"
)

func TestCompileDatasetProducesStableHash(t *testing.T) {
	t.Parallel()
	firstDraft := evaluation.DatasetDraft{
		ID: "support", Revision: 1, Name: "Support",
		Dimensions: []evaluation.Dimension{{Name: dimensionHelpfulness, Weight: 2}, {Name: dimensionAccuracy, Weight: 1}},
		Cases: []evaluation.Case{
			{ID: "b", Name: "Second", Input: json.RawMessage(`{"question":"b"}`), Expected: json.RawMessage(`{"answer":2}`)},
			{ID: "a", Name: "First", Input: json.RawMessage(`{ "question" : "a" }`), Expected: json.RawMessage(`{"answer":1}`)},
		},
	}
	secondDraft := firstDraft
	secondDraft.Dimensions = []evaluation.Dimension{{Name: dimensionAccuracy, Weight: 1}, {Name: dimensionHelpfulness, Weight: 2}}
	secondDraft.Cases = []evaluation.Case{
		{ID: "a", Name: "First", Input: json.RawMessage(`{"question":"a"}`), Expected: json.RawMessage(`{ "answer": 1 }`)},
		{ID: "b", Name: "Second", Input: json.RawMessage(`{"question":"b"}`), Expected: json.RawMessage(`{"answer":2}`)},
	}
	first, err := evaluation.CompileDataset(firstDraft)
	if err != nil {
		t.Fatalf("compile first dataset: %v", err)
	}
	second, err := evaluation.CompileDataset(secondDraft)
	if err != nil {
		t.Fatalf("compile second dataset: %v", err)
	}
	if first.Hash == "" || first.Hash != second.Hash {
		t.Fatalf("dataset hash is unstable: %q %q", first.Hash, second.Hash)
	}
	if first.Cases[0].ID != "a" || first.Dimensions[0].Name != dimensionAccuracy {
		t.Fatalf("dataset order was not normalized: %#v", first)
	}
	if err = evaluation.ValidateDataset(first); err != nil {
		t.Fatalf("validate dataset: %v", err)
	}
	first.Cases[0].Name = "Mutated"
	if !errors.Is(evaluation.ValidateDataset(first), evaluation.ErrDatasetHash) {
		t.Fatal("mutated dataset must fail hash validation")
	}
}

func TestCompileDatasetRejectsInvalidContracts(t *testing.T) {
	t.Parallel()
	tests := []evaluation.DatasetDraft{
		{ID: "empty", Revision: 1, Name: "Empty"},
		{
			ID: "duplicate-case", Revision: 1, Name: "Duplicate",
			Dimensions: []evaluation.Dimension{{Name: "quality", Weight: 1}},
			Cases: []evaluation.Case{
				{ID: "same", Name: "A", Input: json.RawMessage(`null`), Expected: json.RawMessage(`null`)},
				{ID: "same", Name: "B", Input: json.RawMessage(`null`), Expected: json.RawMessage(`null`)},
			},
		},
		{
			ID: "bad-score", Revision: 1, Name: "Bad Score", PassThreshold: 1.5,
			Dimensions: []evaluation.Dimension{{Name: "quality", Weight: 1}},
			Cases:      []evaluation.Case{{ID: "a", Name: "A", Input: json.RawMessage(`null`), Expected: json.RawMessage(`null`)}},
		},
	}
	for _, draft := range tests {
		if _, err := evaluation.CompileDataset(draft); !errors.Is(err, evaluation.ErrInvalidDataset) {
			t.Fatalf("expected invalid dataset for %s, got %v", draft.ID, err)
		}
	}
}
