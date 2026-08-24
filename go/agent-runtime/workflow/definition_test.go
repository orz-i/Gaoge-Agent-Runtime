package workflow_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

const workflowReturnNodeID = "done"

func TestCompileDefinitionProducesStableHash(t *testing.T) {
	t.Parallel()
	draft := workflow.DefinitionDraft{
		ID: "publish", Revision: 1, Name: "Publish",
		Nodes: []workflow.Node{
			{ID: "prepare", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
				Kind: "document.prepare", Input: json.RawMessage(`{"draft":true}`),
			}},
			{ID: "review", Type: workflow.NodeWait, Wait: &workflow.WaitNode{
				Kind: "editor.review", Payload: json.RawMessage(`{"required":true}`),
			}},
			{ID: workflowReturnNodeID, Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`{"status":"published"}`)}},
		},
	}
	first, err := workflow.CompileDefinition(draft)
	if err != nil {
		t.Fatalf("compile definition: %v", err)
	}
	second, err := workflow.CompileDefinition(draft)
	if err != nil {
		t.Fatalf("compile definition again: %v", err)
	}
	if first.Hash == "" || first.Hash != second.Hash {
		t.Fatalf("unstable definition hash: %q %q", first.Hash, second.Hash)
	}
	if err = workflow.ValidateDefinition(first); err != nil {
		t.Fatalf("validate definition: %v", err)
	}
	first.Nodes[0].Effect.Kind = "document.delete"
	if !errors.Is(workflow.ValidateDefinition(first), workflow.ErrDefinitionHash) {
		t.Fatal("mutated definition must fail hash validation")
	}
}

func TestCompileDefinitionRejectsInvalidSequence(t *testing.T) {
	t.Parallel()
	tests := []workflow.DefinitionDraft{
		{
			ID: "duplicate", Revision: 1, Name: "Duplicate",
			Nodes: []workflow.Node{
				{ID: "same", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "input", Payload: json.RawMessage(`{}`)}},
				{ID: "same", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)}},
			},
		},
		{
			ID: "missing-return", Revision: 1, Name: "Missing Return",
			Nodes: []workflow.Node{{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "input", Payload: json.RawMessage(`{}`)}}},
		},
		{
			ID: "bad-union", Revision: 1, Name: "Bad Union",
			Nodes: []workflow.Node{{
				ID: "done", Type: workflow.NodeReturn,
				Effect: &workflow.EffectNode{Kind: "bad", Input: json.RawMessage(`{}`)},
				Return: &workflow.ReturnNode{Value: json.RawMessage(`null`)},
			}},
		},
	}
	for _, draft := range tests {
		if _, err := workflow.CompileDefinition(draft); !errors.Is(err, workflow.ErrInvalidDefinition) {
			t.Fatalf("expected invalid definition for %s, got %v", draft.ID, err)
		}
	}
}
