package harness

import (
	"context"
	"encoding/json"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
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
	window, err := p.resolve(ctx, model.CloneRequest(request))
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
