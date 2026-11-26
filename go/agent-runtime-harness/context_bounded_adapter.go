package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// NewBoundedContextAdapter opts into Runtime-owned lifecycle orchestration.
// Host authorization, ancestry, archive storage and checkpoint CAS remain the
// same ports as the ordinary constructors, which remain ordinary middleware.
func NewBoundedContextAdapter(store Store, manager *runtimecontext.Manager, resolve ModelWindowResolver, relations ContextRunRelationSource) (*agent.BoundedContextAdapter, error) {
	if store == nil || manager == nil || resolve == nil {
		return nil, ErrInvalidRequest
	}
	return agent.NewBoundedContextAdapter(manager, boundedContextPorts{store: store, resolve: resolve, relations: relations})
}

type boundedContextPorts struct {
	store     Store
	resolve   ModelWindowResolver
	relations ContextRunRelationSource
}

// The native selector checks this private bridge's exact Go type before
// reading its actual Store. A public marker alone grants no recovery proof.
func (p boundedContextPorts) ContextSummaryRecoveryHost() kernel.ManagedRecoveryHostStore {
	host, _ := p.store.(kernel.ManagedRecoveryHostStore)
	return host
}

func (p boundedContextPorts) Hydrate(ctx context.Context, invocation plugin.RunInvocation) (context.Context, agent.BoundedContextHydration, error) {
	hydrated, err := (contextRunMiddleware{store: p.store, relations: p.relations}).hydrate(ctx, invocation.RunID)
	if err != nil {
		return ctx, agent.BoundedContextHydration{}, err
	}
	binding, bound := CurrentContextWindowBinding(hydrated)
	checkpoint, found := CurrentContextCheckpoint(hydrated)
	source, err := json.Marshal(struct {
		Binding         ContextWindowBinding
		Bound, Excluded bool
	}{binding, bound, contextWindowExcluded(hydrated)})
	if err != nil {
		return ctx, agent.BoundedContextHydration{}, err
	}
	facts := agent.BoundedContextHydration{RunID: invocation.RunID, SourceBinding: source}
	if found {
		facts.Checkpoint = &checkpoint
	}
	return hydrated, facts, nil
}
func (p boundedContextPorts) ResolveWindow(ctx context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
	return p.resolve(ctx, model.CloneRequest(request))
}
func (p boundedContextPorts) Plan(ctx context.Context, incoming model.Request) (agent.BoundedContextPlan, error) {
	request, err := MaterializeContextWindowRequest(ctx, incoming)
	if err != nil {
		return agent.BoundedContextPlan{}, err
	}
	plan := agent.BoundedContextPlan{Request: request}
	binding, bound := CurrentContextWindowBinding(ctx)
	checkpoint, found := CurrentContextCheckpoint(ctx)
	if !found {
		return plan, nil
	}
	if !bound {
		return plan, ErrInvalidRequest
	}
	turn, err := loadVerifiedTurn(ctx, p.store, binding.TurnID)
	if err != nil {
		return plan, err
	}
	if turn.ContextCheckpointID != checkpoint.ID || !sameContextCheckpointRef(checkpoint, turn.ContextRef) {
		return plan, ErrConflict
	}
	config, err := loadSealedConfig(ctx, p.store, turn.ConfigSnapshotID)
	if err != nil {
		return plan, err
	}
	window, err := p.ResolveWindow(ctx, request)
	if err != nil {
		return plan, err
	}
	source, err := json.Marshal(struct {
		Binding ContextWindowBinding
		Ref     ContextCheckpointRef
	}{binding, turn.ContextRef})
	if err != nil {
		return plan, err
	}
	plan.Checkpoint = &checkpoint
	plan.ModelWindow = window
	plan.Policy = runtimecontext.NormalizePolicy(config.ContextPolicy)
	plan.Owner = binding.Access == ContextWindowOwner
	plan.ArtifactReader = hasContextArtifactReader(request)
	plan.TurnID = turn.ID
	plan.TurnRevision = turn.Revision
	plan.SourceBinding = source
	return plan, nil
}
func (p boundedContextPorts) PutArtifact(ctx context.Context, a runtimecontext.Artifact) (runtimecontext.Artifact, error) {
	saved, _, err := p.store.PutContextArtifact(ctx, a)
	return saved, err
}
func (p boundedContextPorts) CommitCheckpoint(ctx context.Context, plan agent.BoundedContextPlan, next runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error) {
	if plan.Checkpoint == nil {
		return runtimecontext.Checkpoint{}, ErrInvalidRequest
	}
	saved, err := p.store.CommitContextCheckpoint(ctx, ContextCheckpointCommit{TurnID: plan.TurnID, ExpectedTurnRevision: plan.TurnRevision, ExpectedTurnCheckpointID: plan.Checkpoint.ID, ExpectedHeadCheckpointID: plan.Checkpoint.ID, Checkpoint: next, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return runtimecontext.Checkpoint{}, err
	}
	if saved.ID != plan.TurnID || saved.ContextCheckpointID != next.ID || !sameContextCheckpointRef(next, saved.ContextRef) {
		return runtimecontext.Checkpoint{}, ErrConflict
	}
	return p.store.GetContextCheckpoint(ctx, next.ID)
}
func (p boundedContextPorts) BindCheckpoint(ctx context.Context, previous string, next runtimecontext.Checkpoint) error {
	return ReplaceContextCheckpoint(ctx, previous, next)
}

// ReadManagedPreparationContext is the concrete first-party storage read for
// the pre-Model frontier. It never calls a window resolver, model, tokenizer,
// transcript source, or tool. A previously committed model-pressure rollover
// is authoritative only on the exact original Context lineage.
func (p boundedContextPorts) ReadManagedPreparationContext(ctx context.Context, b kernel.ManagedHostBinding, expected *kernel.ManagedPreparationContext, messages []model.Message) (kernel.ManagedPreparationContext, error) {
	host, ok := p.store.(kernel.ManagedRecoveryHostStore)
	if !ok {
		return kernel.ManagedPreparationContext{}, ErrConflict
	}
	if original, err := host.ReadManagedOriginalDispatch(ctx, b); err != nil || !reflect.DeepEqual(original, b) {
		return kernel.ManagedPreparationContext{}, errors.Join(ErrConflict, err)
	}
	turn, err := loadVerifiedTurn(ctx, p.store, b.TurnID)
	if err != nil {
		return kernel.ManagedPreparationContext{}, err
	}
	if terminalTurnStatus(turn.Status) || turn.ContextCheckpointID == "" {
		return kernel.ManagedPreparationContext{}, ErrConflict
	}
	current, err := p.store.GetContextCheckpoint(ctx, turn.ContextCheckpointID)
	if err != nil {
		return kernel.ManagedPreparationContext{}, err
	}
	if !validContextCheckpoint(current) || current.ScopeID != turn.SessionID || !sameContextCheckpointRef(current, turn.ContextRef) {
		return kernel.ManagedPreparationContext{}, ErrConflict
	}
	actual := kernel.ManagedPreparationContext{CheckpointID: current.ID, CheckpointDigest: kernel.ManagedDigest(current)}
	if expected == nil {
		// When sealing, this must also be the actual hydrated frame.
		bound, ok := CurrentContextWindowBinding(ctx)
		hydrated, found := CurrentContextCheckpoint(ctx)
		if !ok || bound.TurnID != turn.ID || bound.Access != ContextWindowOwner || !found || kernel.ManagedDigest(hydrated) != actual.CheckpointDigest {
			return kernel.ManagedPreparationContext{}, ErrConflict
		}
		return actual, nil
	}
	if actual == *expected {
		return actual, nil
	}
	previous, err := p.store.GetContextCheckpoint(ctx, expected.CheckpointID)
	if err != nil {
		return kernel.ManagedPreparationContext{}, err
	}
	if !validContextCheckpoint(previous) || kernel.ManagedDigest(previous) != expected.CheckpointDigest ||
		current.ParentCheckpointID != previous.ID || current.ScopeID != previous.ScopeID {
		return kernel.ManagedPreparationContext{}, ErrConflict
	}
	config, err := loadSealedConfig(ctx, p.store, turn.ConfigSnapshotID)
	if err != nil {
		return kernel.ManagedPreparationContext{}, err
	}
	original, err := mergeContextRuntimeMessagesForCheckpoint(previous, runtimecontext.Materialize(previous.Window), messages)
	if err != nil {
		return kernel.ManagedPreparationContext{}, err
	}
	// Do not trust reason/lineage metadata as proof of candidate contents.
	// Rebuild the finite native compaction candidates without a meter or
	// Provider. The current immutable checkpoint must equal one exact
	// candidate, including all retained and newly archived evidence.
	if err = p.verifyManagedPreparationRollover(ctx, b.RunID, previous, current, original, config.ContextPolicy); err != nil {
		return kernel.ManagedPreparationContext{}, err
	}
	return actual, nil
}

func (p boundedContextPorts) verifyManagedPreparationRollover(ctx context.Context, runID string, previous, current runtimecontext.Checkpoint, messages []model.Message, policy runtimecontext.Policy) error {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	match := func(w runtimecontext.Window, artifacts []runtimecontext.Artifact) (bool, error) {
		target, err := manager.Rollover(ctx, runtimecontext.RolloverRequest{Previous: previous, Window: w, Artifacts: artifacts, Reason: "model_window_pressure",
			ModelWindowFingerprint: current.ModelWindowFingerprint, Assessment: current.Trace.LastAssessment})
		if err != nil {
			return false, err
		}
		if kernel.ManagedDigest(target) != kernel.ManagedDigest(current) {
			return false, nil
		}
		for _, a := range artifacts {
			saved, err := p.store.GetContextArtifact(ctx, a.ID)
			if err != nil {
				return false, err
			}
			if !runtimecontext.ValidArtifact(saved) || kernel.ManagedDigest(saved) != kernel.ManagedDigest(a) {
				return false, ErrConflict
			}
		}
		return true, nil
	}
	policy = runtimecontext.NormalizePolicy(policy)
	tails := []int{policy.PreserveRecentTurns}
	if policy.MinimumRecentTurns < policy.PreserveRecentTurns {
		tails = append(tails, policy.MinimumRecentTurns)
	}
	for _, recent := range tails {
		limited := policy
		limited.PreserveRecentTurns = recent
		for _, omit := range []bool{false, true} {
			candidate, err := manager.CompactPortable(runtimecontext.PortableCompactionRequest{Previous: previous, RunID: runID, Messages: messages, Policy: limited, OmitExtract: omit})
			if errors.Is(err, runtimecontext.ErrBudgetExceeded) {
				continue
			}
			if err != nil {
				return err
			}
			// Same archive safety rule as native portable compaction.
			eligible := true
			for _, a := range candidate.Artifacts {
				var contents []model.Message
				if json.Unmarshal(a.ContentJSON, &contents) != nil || len(contents) == 0 {
					eligible = false
					break
				}
				for _, m := range contents {
					if m.ProviderReplay != nil {
						eligible = false
						break
					}
				}
			}
			if !eligible {
				continue
			}
			ok, err := match(candidate.Window, candidate.Artifacts)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
	candidate, err := manager.CompactToolPressure(previous, runID, messages)
	if err != nil && !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		return err
	}
	if err == nil {
		ok, err := match(candidate.Window, candidate.Artifacts)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return ErrConflict
}
