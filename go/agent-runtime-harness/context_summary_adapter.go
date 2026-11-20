package harness

import (
	"context"
	"reflect"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// NewContextSummaryAdapter connects the native summary lifecycle to the existing
// Harness ownership, immutable Context storage and checkpoint CAS. It grants no
// Provider or execution authority to a Host callback. The same bounded adapter
// must also own Run hydration in the Agent composition.
func NewContextSummaryAdapter(store Store, runtime *kernel.Runtime, bounded *agent.BoundedContextAdapter, resolve ModelWindowResolver) (*agent.ContextSummaryAdapter, error) {
	if store == nil || runtime == nil || bounded == nil || resolve == nil {
		return nil, ErrInvalidRequest
	}
	return agent.NewContextSummaryAdapter(bounded, contextSummaryPorts{store: store, runtime: runtime, resolve: resolve})
}

type contextSummaryPorts struct {
	store   Store
	runtime *kernel.Runtime
	resolve ModelWindowResolver
}

func (p contextSummaryPorts) ContextSummaryRecoveryHost() kernel.ManagedRecoveryHostStore {
	host, _ := p.store.(kernel.ManagedRecoveryHostStore)
	return host
}

func summarySamplingTurn(turn Turn) agent.SamplingTurn {
	value := agent.SamplingTurn{
		ID: turn.ID, SessionID: turn.SessionID, ConfigSnapshotID: turn.ConfigSnapshotID,
		ContextCheckpointID: turn.ContextCheckpointID,
		ContextRef: agent.SamplingCheckpointRef{
			ID: turn.ContextRef.ID, Generation: turn.ContextRef.Generation, Revision: turn.ContextRef.Revision,
			LineageHash: turn.ContextRef.LineageHash, CoveredThroughSourceID: turn.ContextRef.CoveredThroughSourceID, ContentHash: turn.ContextRef.ContentHash,
		},
		Status: string(turn.Status), Revision: turn.Revision, ErrorCode: turn.ErrorCode, ErrorDetail: turn.ErrorDetail,
		// Host storage may return an equivalent local offset. The summary wire
		// projection has one canonical time representation at every read/receipt.
		CreatedAt: turn.CreatedAt.UTC(), UpdatedAt: turn.UpdatedAt.UTC(),
	}
	value.HostTurn.Kind, value.HostTurn.ID = turn.HostTurn.Kind, turn.HostTurn.ID
	return value
}

func summaryRoot(invocation Invocation) agent.ContextSummaryRoot {
	return agent.ContextSummaryRoot{InvocationID: invocation.ID, InputHash: invocation.InputHash, Attempt: invocation.Attempt}
}

func summaryCursorWriteTime(turn Turn, observed time.Time) time.Time {
	writeTime := observed.UTC().Truncate(time.Microsecond)
	if writeTime.Before(turn.UpdatedAt) {
		// Harness.Clock may be ahead of wall time, and a wall clock can move
		// backwards. Freeze the same lower bound that native commit uses.
		// Keep the already-stored precision (including Memory nanoseconds).
		return turn.UpdatedAt.UTC()
	}
	return writeTime
}

func (p contextSummaryPorts) indexedRoot(ctx context.Context, runID string) (Turn, Invocation, Session, error) {
	invocation, err := p.store.GetInvocationByExecutionRefID(ctx, runID)
	if err != nil {
		return Turn{}, Invocation{}, Session{}, err
	}
	if invocation.ExecutionRefID != runID || invocation.ParentItemID != "" || invocation.ExecutionClass != ExecutionAgent || !validInvocationExecutionAuthority(invocation) {
		return Turn{}, Invocation{}, Session{}, ErrConflict
	}
	turn, err := loadVerifiedTurn(ctx, p.store, invocation.TurnID)
	if err != nil {
		return Turn{}, Invocation{}, Session{}, err
	}
	session, err := loadVerifiedSession(ctx, p.store, turn.SessionID)
	if err != nil {
		return Turn{}, Invocation{}, Session{}, err
	}
	return turn, invocation, session, nil
}

func (p contextSummaryPorts) loadRoot(ctx context.Context, runID string, turn Turn, invocation Invocation, session Session) (kernel.Snapshot, error) {
	snapshot, err := p.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.ParentRunID != "" || snapshot.Run.Kind != agent.RunKind || snapshot.Run.Actor != session.Actor ||
		validateRootRuntimeOwnership(invocation, snapshot) != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	config, err := loadSealedConfig(ctx, p.store, turn.ConfigSnapshotID)
	if err != nil || checkRootModelPolicy(invocation, snapshot, config) != nil {
		return kernel.Snapshot{}, ErrConflict
	}
	return snapshot, nil
}

func (p contextSummaryPorts) ownedRoot(ctx context.Context, runID string) (Turn, Invocation, kernel.Snapshot, error) {
	turn, invocation, session, err := p.indexedRoot(ctx, runID)
	if err != nil {
		return Turn{}, Invocation{}, kernel.Snapshot{}, err
	}
	snapshot, err := p.loadRoot(ctx, runID, turn, invocation, session)
	if err != nil {
		return Turn{}, Invocation{}, kernel.Snapshot{}, err
	}
	return turn, invocation, snapshot, nil
}

func (p contextSummaryPorts) PlanSummary(ctx context.Context, incoming model.Request, source kernel.Snapshot) (agent.ContextSummaryPlan, error) {
	plan, err := (boundedContextPorts{store: p.store, resolve: p.resolve}).Plan(ctx, incoming)
	if err != nil {
		return agent.ContextSummaryPlan{}, err
	}
	result := agent.ContextSummaryPlan{Request: plan.Request, ModelWindow: plan.ModelWindow, Policy: plan.Policy, Owner: plan.Owner, ArtifactReader: plan.ArtifactReader}
	if plan.Checkpoint == nil || !plan.Owner {
		return result, nil
	}
	turn, invocation, current, err := p.ownedRoot(ctx, incoming.RunID)
	if err != nil {
		return agent.ContextSummaryPlan{}, err
	}
	if source.Run.ID != incoming.RunID || current.Run.ID != source.Run.ID ||
		validateRootRuntimeOwnership(invocation, source) != nil || current.Run.Status != kernel.RunStatusRunning ||
		turn.ID != plan.TurnID || turn.Revision != plan.TurnRevision || turn.ContextCheckpointID != plan.Checkpoint.ID ||
		!sameContextCheckpointRef(*plan.Checkpoint, turn.ContextRef) {
		return agent.ContextSummaryPlan{}, ErrConflict
	}
	// RetryInvocation rotates the Run, not the Turn. It cannot buy another
	// summary by starting with an empty native invocation list.
	if invocation.Attempt != 1 {
		result.Owner = false
		return result, nil
	}
	config, err := loadSealedConfig(ctx, p.store, turn.ConfigSnapshotID)
	if err != nil {
		return agent.ContextSummaryPlan{}, err
	}
	if config.WorkingStateSummary == nil || config.SharedBudget != nil {
		return agent.ContextSummaryPlan{}, ErrInvalidRequest
	}
	result.Root = summaryRoot(invocation)
	result.Cursor = agent.SamplingCursor{
		Turn: summarySamplingTurn(turn), Checkpoint: runtimecontext.CloneCheckpoint(*plan.Checkpoint),
		// PostgreSQL persists Turn timestamps at microsecond precision. Freeze
		// those exact representable bytes before hashing the summary recipe;
		// an ACK-loss replay must not widen equality to accept a changed time.
		ExpectedTurnCheckpointID: plan.Checkpoint.ID, ExpectedHeadCheckpointID: plan.Checkpoint.ID, UpdatedAt: summaryCursorWriteTime(turn, time.Now()),
	}
	state := plan.Checkpoint.Window.WorkingState
	if state != nil {
		result.ExplicitWorkingState = state.Derivation == nil
		if state.Derivation != nil {
			result.PriorSummaryRun = p.priorSummaryRun(ctx, turn, source, state)
		}
	}
	return result, nil
}

func (p contextSummaryPorts) priorSummaryRun(ctx context.Context, current Turn, owner kernel.Snapshot, state *runtimecontext.WorkingState) *kernel.Snapshot {
	if state.Derivation == nil {
		return nil
	}
	turn, root, session, err := p.indexedRoot(ctx, state.Derivation.RunID)
	if err != nil || turn.SessionID != current.SessionID || session.Actor != owner.Run.Actor {
		return nil
	}
	actor, thread, err := frozenTopLevelInvocationIdentity(root)
	if err != nil || actor != owner.Run.Actor || thread != owner.Run.Thread {
		return nil
	}
	// Authorize the original indexed root in this Session before reading any
	// native feature state named by an inert derivation marker.
	snapshot, err := p.loadRoot(ctx, state.Derivation.RunID, turn, root, session)
	if err != nil {
		return nil
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		return nil
	}
	matched := false
	for _, invocation := range view.ModelInvocations {
		if invocation.ID == state.Derivation.InvocationID && invocation.ContextSummary != nil &&
			invocation.ContextSummary.Root == summaryRoot(root) && invocation.ContextSummary.Cursor.Turn.ID == turn.ID &&
			invocation.ContextSummary.Cursor.Turn.ConfigSnapshotID == turn.ConfigSnapshotID {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	if !agent.VerifyWorkingStateSummaryOrigin(snapshot, state, current.SessionID, owner.Run.Actor, owner.Run.Thread) {
		return nil
	}
	return &snapshot
}

func (p contextSummaryPorts) ReadSummary(ctx context.Context, binding agent.ContextSummaryBinding) (agent.ContextSummaryRead, error) {
	turn, invocation, snapshot, err := p.ownedRoot(ctx, binding.RunID)
	if err != nil {
		return agent.ContextSummaryRead{}, err
	}
	if turn.ID != binding.Cursor.Turn.ID || turn.SessionID != binding.Cursor.Turn.SessionID ||
		turn.ConfigSnapshotID != binding.Cursor.Turn.ConfigSnapshotID || summaryRoot(invocation) != binding.Root ||
		snapshot.Run.Status != kernel.RunStatusRunning || (turn.Status != TurnAccepted && turn.Status != TurnRunning) {
		return agent.ContextSummaryRead{}, ErrConflict
	}
	source, err := p.store.GetContextCheckpoint(ctx, binding.Source.CheckpointID)
	if err != nil {
		return agent.ContextSummaryRead{}, err
	}
	if !validContextCheckpoint(source) || source.ID != binding.Source.CheckpointID || source.ScopeID != turn.SessionID ||
		source.ContentHash != binding.Source.ContentHash || source.CoveredThroughSourceID != binding.Source.CoveredThroughSourceID || source.CoveredPathHash != binding.Source.CoveredPathHash {
		return agent.ContextSummaryRead{}, ErrConflict
	}
	checkpoint, err := p.store.GetContextCheckpoint(ctx, turn.ContextCheckpointID)
	if err != nil {
		return agent.ContextSummaryRead{}, err
	}
	if !validContextCheckpoint(checkpoint) || checkpoint.ID != turn.ContextCheckpointID || checkpoint.ScopeID != turn.SessionID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return agent.ContextSummaryRead{}, ErrConflict
	}
	return agent.ContextSummaryRead{
		SourceCheckpoint: source, Root: summaryRoot(invocation),
		Cursor: agent.SamplingCursor{Turn: summarySamplingTurn(turn), Checkpoint: checkpoint,
			ExpectedTurnCheckpointID: checkpoint.ID, ExpectedHeadCheckpointID: binding.Cursor.ExpectedHeadCheckpointID, UpdatedAt: binding.Cursor.UpdatedAt},
	}, nil
}

func (p contextSummaryPorts) CommitSummary(ctx context.Context, cursor agent.SamplingCursor, next runtimecontext.Checkpoint) (agent.SamplingCommitReceipt, error) {
	turn, err := p.store.CommitContextCheckpoint(ctx, ContextCheckpointCommit{
		TurnID: cursor.Turn.ID, ExpectedTurnRevision: cursor.Turn.Revision,
		ExpectedTurnCheckpointID: cursor.ExpectedTurnCheckpointID, ExpectedHeadCheckpointID: cursor.ExpectedHeadCheckpointID,
		Checkpoint: next, UpdatedAt: cursor.UpdatedAt,
	})
	if err != nil {
		return agent.SamplingCommitReceipt{}, err
	}
	checkpoint, err := p.store.GetContextCheckpoint(ctx, next.ID)
	if err != nil {
		return agent.SamplingCommitReceipt{}, err
	}
	return agent.SamplingCommitReceipt{Turn: summarySamplingTurn(turn), Checkpoint: checkpoint,
		ExpectedTurnCheckpointID: cursor.ExpectedTurnCheckpointID, ExpectedHeadCheckpointID: cursor.ExpectedHeadCheckpointID}, nil
}

func (p contextSummaryPorts) BindSummary(ctx context.Context, cursor agent.SamplingCursor, next runtimecontext.Checkpoint) (agent.SamplingBindingReceipt, error) {
	previous, found := CurrentContextCheckpoint(ctx)
	binding, bound := CurrentContextWindowBinding(ctx)
	if !found || !bound || binding.TurnID != cursor.Turn.ID || binding.Access != ContextWindowOwner || !reflect.DeepEqual(previous, cursor.Checkpoint) {
		return agent.SamplingBindingReceipt{}, ErrConflict
	}
	if previous.ID != next.ID {
		if err := ReplaceContextCheckpoint(ctx, previous.ID, next); err != nil {
			return agent.SamplingBindingReceipt{}, err
		}
	}
	current, found := CurrentContextCheckpoint(ctx)
	if !found {
		return agent.SamplingBindingReceipt{}, ErrConflict
	}
	return agent.SamplingBindingReceipt{TurnID: binding.TurnID, Access: string(binding.Access), Previous: previous, Current: current}, nil
}
