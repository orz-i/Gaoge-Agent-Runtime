package workflow_test

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestCompileDefinitionProposalReportsStructuralAndPolicyImpact(t *testing.T) {
	t.Parallel()
	base, err := workflow.CompileDefinition(workflow.DefinitionDraft{
		ID: "story.flow", Revision: 1, Name: "Story flow",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"storyID":{"type":"string"}}}`),
		Nodes: []workflow.Node{
			{ID: "draft", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
				Kind: "agent.run", Input: json.RawMessage(`{"mode":"draft"}`),
			}},
			{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{FromNode: "draft"}},
		},
		Policy: workflow.DefinitionPolicy{
			RequiredPermissions: []string{"story.read"}, CostClass: workflow.CostLow,
			MaxCostUnits: 10, SideEffectClass: workflow.SideEffectRead,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := workflow.CompileDefinitionProposal(&base, workflow.DefinitionDraft{
		Name:        "Story flow v2",
		InputSchema: json.RawMessage(`{"properties":{"storyID":{"type":"string"}},"type":"object"}`),
		Nodes: []workflow.Node{
			{ID: "draft", Type: workflow.NodeEffect, Effect: &workflow.EffectNode{
				Kind: "agent.run", Input: json.RawMessage(`{"mode":"rewrite"}`),
			}},
			{ID: "review", Type: workflow.NodeWait, Wait: &workflow.WaitNode{
				Kind: "author.review", Payload: json.RawMessage(`{"required":true}`),
			}},
			{ID: "done", Type: workflow.NodeReturn, Return: &workflow.ReturnNode{FromNode: "review"}},
		},
		Policy: workflow.DefinitionPolicy{
			RequiredPermissions: []string{"media.generate", "story.read"}, CostClass: workflow.CostHigh,
			MaxCostUnits: 40, SideEffectClass: workflow.SideEffectWrite,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.BaseRevision != 1 || report.Candidate.Revision != 2 || report.Diff.InputSchemaChanged ||
		len(report.Diff.AddedNodeIDs) != 1 || report.Diff.AddedNodeIDs[0] != "review" ||
		len(report.Diff.ChangedNodeIDs) != 2 || report.Impact.MaxCostUnitsDelta != 30 ||
		len(report.Impact.PermissionsAdded) != 1 || report.Impact.PermissionsAdded[0] != "media.generate" {
		t.Fatalf("proposal report = %#v", report)
	}
}

func TestCompileDefinitionCanonicalizesNestedJSONForStableHash(t *testing.T) {
	t.Parallel()
	first := registryDraft("story.flow", "Story flow")
	first.Revision = 1
	first.InputSchema = json.RawMessage(`{"type":"object","properties":{"b":{"type":"number"},"a":{"type":"string"}}}`)
	first.Nodes[0].Effect.Input = json.RawMessage(`{"b":2,"a":1}`)
	second := registryDraft("story.flow", "Story flow")
	second.Revision = 1
	second.InputSchema = json.RawMessage(`{"properties":{"a":{"type":"string"},"b":{"type":"number"}},"type":"object"}`)
	second.Nodes[0].Effect.Input = json.RawMessage(`{"a":1,"b":2}`)
	left, err := workflow.CompileDefinition(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := workflow.CompileDefinition(second)
	if err != nil {
		t.Fatal(err)
	}
	if left.Hash != right.Hash {
		t.Fatalf("canonical hashes differ: %s != %s", left.Hash, right.Hash)
	}
}
