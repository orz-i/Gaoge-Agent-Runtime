package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// agentCallAllowanceGranter is the narrow opt-in capability needed for an
// owner-authorized, explicit continuation grant. It does not make every Agent
// implementation or application Feature a renewable task.
type agentCallAllowanceGranter interface {
	GrantCallAllowance(context.Context, string, uint64, agent.CallAllowance) (kernel.Snapshot, error)
}

// GrantCallAllowance renews a paused direct top-level Agent within a frozen
// Harness Turn. It uses the same Run ID and existing Turn budget ledger.
// External callers MUST authorize Turn ownership (the HTTP adapter does so
// using the authenticated Session Actor); this SDK method accepts no user ID.
func (runner *Runner) GrantCallAllowance(
	ctx context.Context, turnID string, expectedTurnRevision uint64, increment agent.CallAllowance,
) (Snapshot, error) {
	if runner == nil || runner.runtime == nil || runner.store == nil ||
		strings.TrimSpace(turnID) == "" || expectedTurnRevision == 0 {
		return Snapshot{}, ErrInvalidRequest
	}
	grant, ok := runner.agent.(agentCallAllowanceGranter)
	if !ok {
		return Snapshot{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	if turn.Status != TurnPausedBudget || turn.Revision != expectedTurnRevision {
		return Snapshot{}, ErrConflict
	}
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass != ExecutionAgent || invocation.ParentItemID != "" ||
		invocation.Status != InvocationPausedBudget || invocation.ExecutionRefID == "" {
		return Snapshot{}, ErrConflict
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return Snapshot{}, err
	}
	current, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil {
		return Snapshot{}, err
	}
	if current.Run.Actor != session.Actor || current.Run.Kind != agent.RunKind ||
		current.Run.Status != kernel.RunStatusPausedBudget {
		return Snapshot{}, ErrConflict
	}
	resumed, err := grant.GrantCallAllowance(ctx, current.Run.ID, current.Run.Revision, increment)
	if err != nil {
		switch {
		case errors.Is(err, kernel.ErrConflict), errors.Is(err, agent.ErrRunNotBudgetPaused):
			return Snapshot{}, errors.Join(ErrConflict, err)
		case errors.Is(err, agent.ErrInvalidCallAllowance), errors.Is(err, kernel.ErrDeadline):
			return Snapshot{}, errors.Join(ErrInvalidRequest, err)
		default:
			return Snapshot{}, err
		}
	}
	return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, resumed)
}
