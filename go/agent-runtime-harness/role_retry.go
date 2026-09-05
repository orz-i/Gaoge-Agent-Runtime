package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (runner *Runner) startRetriedRole(ctx context.Context, invocation Invocation, child childInvocationContext, requestID string, frozen handoff.Delegation) (kernel.Snapshot, error) {
	config, err := runner.store.GetConfigSnapshot(ctx, child.turn.ConfigSnapshotID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if config.SharedBudget != nil {
		role, exists := findRole(config.Roles, frozen.RoleID)
		if !exists || runner.budgets == nil {
			return kernel.Snapshot{}, ErrConflict
		}
		if _, err = runner.budgets.coordinator().RegisterRun(ctx, child.turn.ID, invocation.ExecutionRefID,
			budget.RunBudget{ParentRunID: child.parentExecutionRefID, Limits: role.Limits}); err != nil {
			return kernel.Snapshot{}, err
		}
	}
	return runner.agent.StartRun(ctx, agent.StartRequest{ID: invocation.ExecutionRefID, Actor: child.actor, Thread: child.thread,
		RequestID: requestID, Goal: frozen.Goal, Instructions: frozen.Instructions, Model: frozen.Model,
		ModelOptions: frozen.ModelOptions, ToolKeys: frozen.ToolKeys, Limits: frozen.Limits})
}
