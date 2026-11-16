package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func delegationDepthExceeded() error {
	return tools.NewRecoverableCallError(
		"delegation.depth_exceeded",
		"Delegation depth limit reached for this task.",
		ErrInvalidRequest,
	)
}

func (runner *Runner) enforceDelegationDepth(
	ctx context.Context,
	config ConfigSnapshot,
	invocation Invocation,
) error {
	allowed, err := delegationAllowed(ctx, runner.store, config.DelegationPolicy, invocation)
	if err != nil {
		return err
	}
	if !allowed {
		return delegationDepthExceeded()
	}
	return nil
}

func delegationAllowed(
	ctx context.Context,
	store Store,
	policy DelegationPolicySnapshot,
	invocation Invocation,
) (bool, error) {
	if policy.MaxDepth <= 0 {
		return true, nil
	}
	depth, err := invocationDelegationDepth(ctx, store, invocation)
	if err != nil {
		return false, err
	}
	return depth < policy.MaxDepth, nil
}

func invocationDelegationDepth(ctx context.Context, store Store, invocation Invocation) (int, error) {
	if store == nil || strings.TrimSpace(invocation.ID) == "" || strings.TrimSpace(invocation.TurnID) == "" {
		return 0, ErrInvalidRequest
	}
	// Only the original ancestor chain determines delegation depth. A full
	// Turn Timeline scan scales with unrelated Items and allocates an index
	// that already exists in both Memory and PostgreSQL Stores.
	// GetItem reads the globally unique immutable ID directly; recheck Turn
	// and parent identity because a global hit is not permission to traverse
	// an Item from a different Host Turn.
	depth := 0
	current := invocation
	visited := map[string]struct{}{}
	for strings.TrimSpace(current.ParentItemID) != "" {
		if current.TurnID != invocation.TurnID {
			return 0, ErrConflict
		}
		if _, exists := visited[current.ID]; exists {
			return 0, ErrConflict
		}
		visited[current.ID] = struct{}{}
		item, err := store.GetItem(ctx, current.ParentItemID)
		if errors.Is(err, ErrNotFound) {
			return 0, ErrConflict
		}
		if err != nil {
			return 0, err
		}
		if item.ID != current.ParentItemID || item.TurnID != invocation.TurnID {
			return 0, ErrConflict
		}
		if item.Kind == ItemDelegation {
			depth++
		}
		if strings.TrimSpace(item.InvocationID) == "" {
			break
		}
		parent, loadErr := store.GetInvocation(ctx, item.InvocationID)
		if errors.Is(loadErr, ErrNotFound) {
			return 0, ErrConflict
		}
		if loadErr != nil {
			return 0, loadErr
		}
		if parent.ID != item.InvocationID || parent.TurnID != invocation.TurnID {
			return 0, ErrConflict
		}
		current = parent
	}
	return depth, nil
}
