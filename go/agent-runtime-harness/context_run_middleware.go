package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

const contextRunMiddlewareName = "harness.context_run"

// ContextRunRelationSource resolves durable parent ownership for Runtime child runs.
// It is intentionally narrower than runrelation.Registry so the middleware can be
// tested and reused without owning relation persistence.
type ContextRunRelationSource interface {
	GetByChild(context.Context, string) (runrelation.Relation, error)
}

// NewContextRunMiddleware rebuilds the execution-scoped Context Window from
// durable Harness facts whenever an Agent Run is started or resumed without the
// original process-local context.Context. Exact top-level Agent invocations own
// the Turn Context head; nested Runtime runs inherit it read-only.
func NewContextRunMiddleware(store Store, relations ContextRunRelationSource) (plugin.RunMiddleware, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return contextRunMiddleware{store: store, relations: relations}, nil
}

type contextRunMiddleware struct {
	store     Store
	relations ContextRunRelationSource
}

func (contextRunMiddleware) Name() string { return contextRunMiddlewareName }

func (middleware contextRunMiddleware) Run(
	ctx context.Context,
	invocation plugin.RunInvocation,
	next plugin.RunNext,
) (kernel.Snapshot, error) {
	hydrated, err := middleware.hydrate(ctx, strings.TrimSpace(invocation.RunID))
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return next(hydrated)
}

func (middleware contextRunMiddleware) hydrate(ctx context.Context, runID string) (context.Context, error) {
	if binding, ok := CurrentContextWindowBinding(ctx); ok {
		return middleware.hydrateExistingBinding(ctx, binding)
	}
	return middleware.hydrateRun(ctx, runID)
}

func (middleware contextRunMiddleware) hydrateExistingBinding(
	ctx context.Context,
	binding ContextWindowBinding,
) (context.Context, error) {
	checkpoint, ok := CurrentContextCheckpoint(ctx)
	if !ok {
		return middleware.hydrateTurn(ctx, binding.TurnID, binding.Access)
	}
	turn, err := middleware.store.GetTurn(ctx, binding.TurnID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(turn.ContextCheckpointID) == "" {
		return nil, ErrConflict
	}
	if checkpoint.ID == turn.ContextCheckpointID && sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return ctx, nil
	}
	return middleware.hydrateTurn(ctx, binding.TurnID, binding.Access)
}

func (middleware contextRunMiddleware) hydrateRun(ctx context.Context, runID string) (context.Context, error) {
	if runID == "" {
		return ctx, nil
	}
	turn, access, err := resolveContextTurnForRun(ctx, middleware.store, middleware.relations, runID)
	if errors.Is(err, ErrNotFound) {
		return ctx, nil
	}
	if err != nil {
		return nil, err
	}
	return middleware.hydrateTurn(ctx, turn.ID, access)
}

func resolveContextTurnForRun(
	ctx context.Context,
	store Store,
	relations ContextRunRelationSource,
	runID string,
) (Turn, ContextWindowAccess, error) {
	if store == nil || strings.TrimSpace(runID) == "" {
		return Turn{}, "", ErrNotFound
	}
	runID = strings.TrimSpace(runID)

	current := runID
	seen := make(map[string]struct{})
	for current != "" {
		if _, duplicate := seen[current]; duplicate {
			return Turn{}, "", ErrConflict
		}
		seen[current] = struct{}{}

		turn, access, found, err := contextTurnForExecutionRef(ctx, store, current, runID)
		if err != nil || found {
			return turn, access, err
		}
		parentRunID, found, err := contextParentRunID(ctx, relations, current)
		if err != nil {
			return Turn{}, "", err
		}
		if !found {
			return Turn{}, "", ErrNotFound
		}
		current = parentRunID
	}
	return Turn{}, "", ErrNotFound
}

func contextTurnForExecutionRef(
	ctx context.Context,
	store Store,
	executionRefID string,
	requestedRunID string,
) (Turn, ContextWindowAccess, bool, error) {
	invocation, err := store.GetInvocationByExecutionRefID(ctx, executionRefID)
	if errors.Is(err, ErrNotFound) {
		return Turn{}, "", false, nil
	}
	if err != nil {
		return Turn{}, "", false, err
	}
	access := ContextWindowReadOnly
	if executionRefID == requestedRunID && strings.TrimSpace(invocation.ParentItemID) == "" && invocation.ExecutionClass == ExecutionAgent {
		access = ContextWindowOwner
	}
	turn, err := store.GetTurn(ctx, invocation.TurnID)
	return turn, access, true, err
}

func contextParentRunID(
	ctx context.Context,
	relations ContextRunRelationSource,
	childRunID string,
) (string, bool, error) {
	if relations == nil {
		return "", false, nil
	}
	relation, err := relations.GetByChild(ctx, childRunID)
	if errors.Is(err, runrelation.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(relation.ParentRunID), true, nil
}

func (middleware contextRunMiddleware) hydrateTurn(
	ctx context.Context,
	turnID string,
	access ContextWindowAccess,
) (context.Context, error) {
	turn, err := middleware.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return nil, err
	}
	checkpointID := strings.TrimSpace(turn.ContextCheckpointID)
	if checkpointID == "" {
		return ctx, nil
	}
	checkpoint, err := middleware.store.GetContextCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint.ScopeID != turn.SessionID || checkpoint.ID != checkpointID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return nil, ErrConflict
	}
	ctx = withContextCheckpoint(ctx, checkpoint)
	return withContextWindowBinding(ctx, turn.ID, access), nil
}
