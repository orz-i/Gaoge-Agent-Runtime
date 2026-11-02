package harness

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// validateChildRuntimeOwnership proves that a locally executed child belongs
// to the original frozen root/ancestor Attempts and to its own immutable
// physical request. It can read a terminal child/root for reconciliation;
// liveness and physical dispatch grants remain separate responsibilities.
//
// Handoff children use their fixed ChildRunner and delegation receipts, not
// this native Feature validator. No new Store or execution authority is added.
func (runner *Runner) validateChildRuntimeOwnership(
	ctx context.Context, child Invocation, physical kernel.Snapshot,
) error {
	if runner == nil || runner.store == nil || runner.runtime == nil ||
		strings.TrimSpace(child.ParentItemID) == "" || isHandoffDelegationInvocation(child) {
		return ErrConflict
	}
	var expectedKind kernel.RunKind
	switch child.ExecutionClass {
	case ExecutionAgent:
		expectedKind = agent.RunKind
	case ExecutionTeam:
		expectedKind = team.RunKind
	case ExecutionGroupChat:
		expectedKind = groupchat.RunKind
	case ExecutionWorkflow:
		expectedKind = workflow.RunKind
	case ExecutionApplication:
		return ErrConflict
	default:
		return ErrConflict
	}
	binding, err := frozenChildRoot(child)
	if err != nil {
		return err
	}
	root, err := runner.store.GetInvocation(ctx, binding.InvocationID)
	if err != nil || root.ID == child.ID || root.ID != binding.InvocationID ||
		root.TurnID != child.TurnID || root.ParentItemID != "" ||
		invocationAttempt(root) != binding.attempt() || root.ExecutionClass == ExecutionApplication {
		return ErrConflict
	}
	actor, thread, err := frozenTopLevelInvocationIdentity(root)
	if err != nil {
		return err
	}
	// A root Retry rotates the original physical authority. Never accept an
	// old child Attempt as a result of a new root or inherited parent Attempt.
	for _, parent := range binding.Parents {
		owner, loadErr := runner.store.GetInvocation(ctx, parent.InvocationID)
		if loadErr != nil || owner.TurnID != child.TurnID ||
			owner.ParentItemID == "" || invocationAttempt(owner) != parent {
			return ErrConflict
		}
	}
	// The actual root Run must agree with its immutable Harness owner, not
	// merely share its ID with a foreign physical Run. This guards cases
	// where an old child is independently observed after root progression.
	rootRun, err := runner.runtime.Load(ctx, root.ExecutionRefID)
	if err != nil {
		return err
	}
	if err := validateRootRuntimeOwnership(root, rootRun); err != nil {
		return err
	}
	var frozen struct {
		Goal string `json:"goal"`
	}
	if err := json.Unmarshal(child.Input, &frozen); err != nil || strings.TrimSpace(frozen.Goal) == "" {
		return ErrConflict
	}
	if physical.Run.ID != child.ExecutionRefID || physical.Run.Kind != expectedKind ||
		physical.Run.Actor != actor || physical.Run.Thread != thread ||
		physical.Run.Goal != frozen.Goal {
		return ErrConflict
	}
	if child.Attempt == 1 {
		id, idErr := InvocationID(child.TurnID, child.ParentItemID, child.CapabilityKey, physical.Run.RequestID)
		if idErr != nil || id != child.ID {
			return ErrConflict
		}
	} else if physical.Run.RequestID != stableID("hretry", child.ID, attemptString(child.Attempt)) {
		return ErrConflict
	}
	return nil
}
