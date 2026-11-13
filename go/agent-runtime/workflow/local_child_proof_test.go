package workflow

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestVerifyLocalChildOnlyBuiltInSubworkflow(t *testing.T) {
	runtime := &kernel.Runtime{}
	runner := &Runner{runtime: runtime, registry: &DefinitionRegistry{}}
	effect := Effect{ID: "effect", NodeID: "node", Class: EffectClassSubworkflow, Definition: &DefinitionReference{ID: "nested", Revision: 1, Hash: "hash"}, NestedDepth: 1, Attempt: 1, ChildRunID: "effect-child", Input: json.RawMessage(`{"x":1}`)}
	encode := func(value any) json.RawMessage {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	parent := kernel.Snapshot{Run: kernel.Run{ID: "parent", Kind: RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}}, State: encode(executionState{Effects: []Effect{effect}})}
	child := kernel.Snapshot{Run: kernel.Run{ID: "effect-child", Kind: RunKind, ParentRunID: parent.Run.ID, Actor: parent.Run.Actor, Thread: parent.Run.Thread, RequestID: "effect", Goal: "subworkflow:nested", Status: kernel.RunStatusRunning}, State: encode(executionState{Definition: Definition{ID: "nested", Revision: 1, Hash: "hash"}, NestedDepth: 1, Input: effect.Input})}
	relation := runrelation.Relation{ParentRunID: "parent", ChildRunID: "effect-child", Kind: runrelation.KindWorkflowEffect, OwnerNodeID: "node"}
	for _, control := range []bool{false, true} {
		if !runner.VerifyLocalChild(runtime, parent, relation, child, control) {
			t.Fatal("built-in local child rejected")
		}
		foreign := child
		foreign.Run.Actor.TenantID = "foreign"
		if runner.VerifyLocalChild(runtime, parent, relation, foreign, control) {
			t.Fatal("foreign task accepted")
		}
		foreign = child
		foreign.Run.ParentRunID = "foreign-workflow"
		if runner.VerifyLocalChild(runtime, parent, relation, foreign, control) {
			t.Fatal("nested Workflow reused a foreign immutable Kernel parent")
		}
		foreign = child
		foreign.State = encode(executionState{Definition: Definition{ID: "nested", Revision: 2, Hash: "hash"}, NestedDepth: 1, Input: effect.Input})
		if runner.VerifyLocalChild(runtime, parent, relation, foreign, control) {
			t.Fatal("different definition revision accepted")
		}
		external := effect
		external.Class = EffectClass("external")
		externalParent := parent
		externalParent.State = encode(executionState{Effects: []Effect{external}})
		if runner.VerifyLocalChild(runtime, externalParent, relation, child, control) {
			t.Fatal("arbitrary external effect accepted")
		}
	}
	runner.registry = nil
	if runner.VerifyLocalChild(runtime, parent, relation, child, false) {
		t.Fatal("custom subworkflow executor accepted local twin")
	}
}
