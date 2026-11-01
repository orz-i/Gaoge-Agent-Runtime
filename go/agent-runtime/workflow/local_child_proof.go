package workflow

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// VerifyLocalChild proves only built-in subworkflows executed by this Kernel.
// Arbitrary EffectExecutor children have no inferred local authority, even if
// a local Run has identical metadata. The caller must prove the parent chain.
func (runner *Runner) VerifyLocalChild(runtime *kernel.Runtime, parent kernel.Snapshot, relation runrelation.Relation, child kernel.Snapshot, _ bool) bool {
	if runner == nil || runtime == nil || runner.runtime != runtime || runner.registry == nil ||
		parent.Run.Kind != RunKind || child.Run.Kind != RunKind ||
		relation.Kind != runrelation.KindWorkflowEffect || relation.ParentRunID != parent.Run.ID ||
		relation.ChildRunID != child.Run.ID || relation.OwnerNodeID == "" ||
		child.Run.Actor != parent.Run.Actor || child.Run.Thread != parent.Run.Thread {
		return false
	}
	state, err := decodeExecutionState(parent.State)
	if err != nil {
		return false
	}
	for _, effect := range state.Effects {
		if effect.Class != EffectClassSubworkflow || effect.ChildRunID != relation.ChildRunID ||
			effect.Definition == nil || !validDefinitionReference(*effect.Definition) ||
			effect.NestedDepth <= 0 {
			continue
		}
		ownerMatches := relation.OwnerNodeID == effectRelationOwnerID(effect)
		// ensureEffectRelation preserves historical node-owned fan-out relations.
		if (effect.OutputKey != "" || effect.Mapped) && relation.OwnerNodeID == effect.NodeID {
			ownerMatches = true
		}
		if !ownerMatches {
			continue
		}
		return verifiedSubworkflowChildOrigin(EffectRequest{
			EffectID: effect.ID, Actor: parent.Run.Actor, Thread: parent.Run.Thread,
			Definition: effect.Definition, NestedDepth: effect.NestedDepth, Input: effect.Input,
		}, child)
	}
	return false
}
