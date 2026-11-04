package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
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
	if contextWindowExcluded(ctx) {
		// Exclusion is sticky even if an internal caller subsequently rebinds
		// a process-local checkpoint. Never hand that state to the Agent.
		return withoutContextCheckpoint(ctx), nil
	}
	// Prove Context owner and Handoff isolation from one durable ancestry
	// scan. Re-reading a RunRelation after a prior NotFound could admit a
	// newly visible Specialist parent without rechecking its isolation.
	turn, access, isolated, err := resolveContextRunAuthority(ctx, middleware.store, middleware.relations, runID)
	if errors.Is(err, ErrNotFound) {
		// Independent/unresolved Runs have no proven Harness Context scope.
		// Do not leak a prior Run's checkpoint even if its binding is absent.
		return withoutContextCheckpoint(ctx), nil
	}
	if err != nil {
		return nil, err
	}
	if isolated {
		return withoutContextCheckpoint(ctx), nil
	}
	// A process Context may be reused for another Run. Its inherited Turn
	// and access must never supersede the durable authority above.
	if binding, ok := CurrentContextWindowBinding(ctx); ok && binding.TurnID == turn.ID {
		// A caller can deliberately narrow access, but an inherited owner
		// grant can never override a read-only child relation.
		if binding.Access == ContextWindowReadOnly {
			access = ContextWindowReadOnly
		}
		if binding.Access == access {
			return middleware.hydrateExistingBinding(ctx, binding)
		}
	}
	return middleware.hydrateTurn(withoutContextCheckpoint(ctx), turn.ID, access)
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
	if !validTurn(turn) || turn.ID != binding.TurnID || strings.TrimSpace(turn.ContextCheckpointID) == "" {
		return nil, ErrConflict
	}
	// An in-process Context may retain a matching checkpoint header after
	// its message bytes were changed. Verify the actual content hash before
	// reusing it; otherwise reload the durable Turn head instead of putting
	// unverified transcript content into a model-visible request.
	if validContextCheckpoint(checkpoint) && checkpoint.ScopeID == turn.SessionID &&
		checkpoint.ID == turn.ContextCheckpointID && sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return ctx, nil
	}
	return middleware.hydrateTurn(ctx, binding.TurnID, binding.Access)
}

// resolveContextRunAuthority proves an execution-scoped Context owner or
// Handoff exclusion in one traversal of immutable Run ancestry. Every
// ChildRunID is looked up at most once, including when a relation appears
// concurrently with the request. The nearest durable Harness Invocation
// defines the Turn; any Handoff ancestor excludes Supervisor Context for
// the entire subtree, regardless of caller-held process Context.
//
// Both Context rehydration and the read-only Artifact Tool must enforce
// this same proof. Other feature consumers may still use their separate
// durable Turn resolver for non-Context authorization.
func resolveContextRunAuthority(
	ctx context.Context, store Store, relations ContextRunRelationSource, runID string,
) (Turn, ContextWindowAccess, bool, error) {
	if store == nil || strings.TrimSpace(runID) == "" {
		return Turn{}, "", false, ErrNotFound
	}
	runID = strings.TrimSpace(runID)
	current := runID
	seen := make(map[string]struct{})
	turnID := ""
	access := ContextWindowReadOnly
	isolated := false
	for current != "" {
		if _, duplicate := seen[current]; duplicate {
			return Turn{}, "", false, ErrConflict
		}
		seen[current] = struct{}{}
		invocation, err := verifiedRunInvocation(ctx, store, current)
		if err == nil {
			if invocation.ParentItemID != "" && isHandoffDelegationInvocation(invocation) {
				isolated = true
			}
			if turnID == "" {
				turnID = invocation.TurnID
				if current == runID && invocation.ParentItemID == "" && invocation.ExecutionClass == ExecutionAgent {
					access = ContextWindowOwner
				}
			}
		} else if !errors.Is(err, ErrNotFound) {
			return Turn{}, "", false, err
		}
		parent, found, err := contextParentRunID(ctx, relations, current)
		if err != nil {
			return Turn{}, "", false, err
		}
		if !found {
			break
		}
		current = parent
	}
	if turnID == "" {
		return Turn{}, "", false, ErrNotFound
	}
	turn, err := store.GetTurn(ctx, turnID)
	if err != nil {
		return Turn{}, "", false, err
	}
	if !validTurn(turn) || turn.ID != turnID {
		return Turn{}, "", false, ErrConflict
	}
	return turn, access, isolated, nil
}

// An execution index is a locator, not a second independent source of
// ownership. Cross-check its immutable payload against the canonical
// Invocation read before any consumer uses the Turn or execution class.
// Mutable lifecycle fields may advance between reads without revoking
// immutable identity; an index/payload disagreement always fails closed.
func verifiedRunInvocation(ctx context.Context, store Store, executionRefID string) (Invocation, error) {
	indexed, err := store.GetInvocationByExecutionRefID(ctx, executionRefID)
	if err != nil {
		return Invocation{}, err
	}
	if !validInvocation(indexed) || indexed.ExecutionRefID != executionRefID {
		return Invocation{}, ErrConflict
	}
	canonical, err := store.GetInvocation(ctx, indexed.ID)
	if errors.Is(err, ErrNotFound) {
		return Invocation{}, ErrConflict
	}
	if err != nil {
		return Invocation{}, err
	}
	if !validInvocation(canonical) || canonical.ExecutionRefID != executionRefID ||
		!sameInvocationIdentity(indexed, canonical) {
		return Invocation{}, ErrConflict
	}
	return indexed, nil
}

// Context, Artifact and shared-budget consumers resolve ownership from the
// same verified Run ancestry. Handoff isolation restricts Context/Artifact,
// but descendants still participate in Supervisor shared budgeting.
func resolveContextTurnForRun(
	ctx context.Context,
	store Store,
	relations ContextRunRelationSource,
	runID string,
) (Turn, ContextWindowAccess, error) {
	turn, access, _, err := resolveContextRunAuthority(ctx, store, relations, runID)
	return turn, access, err
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
	// A relation read is an ownership authority input. Enforce the same
	// immutable-record contract as the writer, including Kind, OwnerNodeID
	// and CreatedAt, and reject non-canonical identity metadata. An invalid
	// index result cannot authorize a foreign Turn or Supervisor Context.
	prepared, prepareErr := runrelation.Prepare(relation)
	if prepareErr != nil || relation.ChildRunID != childRunID ||
		prepared.ParentRunID != relation.ParentRunID || prepared.ChildRunID != relation.ChildRunID ||
		prepared.Kind != relation.Kind || prepared.OwnerNodeID != relation.OwnerNodeID {
		return "", false, ErrConflict
	}
	return relation.ParentRunID, true, nil
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
	if !validTurn(turn) || turn.ID != strings.TrimSpace(turnID) {
		return nil, ErrConflict
	}
	checkpointID := strings.TrimSpace(turn.ContextCheckpointID)
	if checkpointID == "" {
		return withoutContextCheckpoint(ctx), nil
	}
	checkpoint, err := middleware.store.GetContextCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	// Store adapters must return a complete valid Checkpoint, not only a
	// header matching the durable Turn. Recompute the content hash at this
	// final model-visible boundary even when the adapter also validates it.
	if !validContextCheckpoint(checkpoint) || checkpoint.ScopeID != turn.SessionID ||
		checkpoint.ID != checkpointID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return nil, ErrConflict
	}
	ctx = withContextCheckpoint(ctx, checkpoint)
	return withContextWindowBinding(ctx, turn.ID, access), nil
}
