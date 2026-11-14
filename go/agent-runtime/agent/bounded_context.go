package agent

import (
	"context"
	"encoding/json"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type BoundedContextHydration struct {
	RunID         string
	Checkpoint    *runtimecontext.Checkpoint
	SourceBinding json.RawMessage
}
type BoundedContextPlan struct {
	Request               model.Request
	Checkpoint            *runtimecontext.Checkpoint
	ModelWindow           runtimecontext.ModelWindow
	Policy                runtimecontext.Policy
	Owner, ArtifactReader bool
	TurnID                string
	TurnRevision          uint64
	SourceBinding         json.RawMessage
}

// Ports retain host authorization and storage. They expose specific operations,
// never a Run/Next callback or a caller assertion of whole-layer completion.
type BoundedContextPorts interface {
	Hydrate(context.Context, plugin.RunInvocation) (context.Context, BoundedContextHydration, error)
	Plan(context.Context, model.Request) (BoundedContextPlan, error)
	PutArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, error)
	CommitCheckpoint(context.Context, BoundedContextPlan, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error)
	BindCheckpoint(context.Context, string, runtimecontext.Checkpoint) error
}
type BoundedContextAdapter struct {
	manager  *runtimecontext.Manager
	ports    BoundedContextPorts
	sampling SamplingContextPorts
}

func NewBoundedContextAdapter(manager *runtimecontext.Manager, ports BoundedContextPorts) (*BoundedContextAdapter, error) {
	if manager == nil || ports == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedContextAdapter{manager: manager, ports: ports}, nil
}
func (*BoundedContextAdapter) Name() string { return "runtime.bounded_context.v1" }
func (a *BoundedContextAdapter) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	var hydrated context.Context
	var source BoundedContextHydration
	var err error
	if a.sampling != nil {
		hydrated, source, err = a.sampling.Hydrate(ctx, inv)
	} else {
		hydrated, source, err = a.ports.Hydrate(ctx, inv)
	}
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if hydrated == nil || source.RunID != inv.RunID || len(source.SourceBinding) == 0 || !json.Valid(source.SourceBinding) {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	callbackscope.Operation(ctx, a, "managed.hydrated", source)
	snapshot, nextErr := next(hydrated)
	// Runtime owns this entire sequence: one successful read/binding operation,
	// all Next work returned, and no post-Next host operation can be hidden here.
	callbackscope.Record(ctx, a, "context.hydrate.returned", source)
	return snapshot, nextErr
}
func (a *BoundedContextAdapter) Prepare(ctx context.Context, incoming model.Request) (prepared model.Request, prepareErr error) {
	if a.sampling != nil {
		return model.Request{}, ErrInvalidRequest
	}
	callbackscope.ManagedPrepareEntered(ctx, a)
	defer func() { callbackscope.ManagedPrepareReturned(ctx, a, prepared, prepareErr) }()
	plan, err := a.ports.Plan(ctx, model.CloneRequest(incoming))
	if err != nil {
		return model.Request{}, err
	}
	request := plan.Request
	if request.RunID != incoming.RunID {
		return model.Request{}, ErrInvalidRequest
	}
	if plan.Checkpoint == nil {
		return request, nil
	}
	if plan.TurnID == "" || plan.TurnRevision == 0 || len(plan.SourceBinding) == 0 || !json.Valid(plan.SourceBinding) {
		return model.Request{}, ErrInvalidRequest
	}
	policy := runtimecontext.NormalizePolicy(plan.Policy)
	assessment, err := a.manager.AssessModelRequest(ctx, request, plan.ModelWindow, policy)
	if err != nil {
		return model.Request{}, err
	}
	withinHard := runtimecontext.WithinHardBudget(assessment, policy)
	if withinHard && assessment.HardTokenEstimate <= assessment.SoftInputTokens {
		return request, nil
	}
	if !plan.ArtifactReader || !plan.Owner {
		if withinHard {
			return request, nil
		}
		return model.Request{}, runtimecontext.ErrBudgetExceeded
	}
	choice, ok, err := a.selectCompaction(ctx, *plan.Checkpoint, request, plan.ModelWindow, policy, assessment, withinHard)
	if err != nil {
		return model.Request{}, err
	}
	if !ok {
		if withinHard {
			return request, nil
		}
		return model.Request{}, runtimecontext.ErrBudgetExceeded
	}
	next, err := a.manager.Rollover(ctx, runtimecontext.RolloverRequest{Previous: *plan.Checkpoint, Window: choice.window, Artifacts: choice.artifacts, Reason: "model_window_pressure", ModelWindowFingerprint: runtimecontext.ModelWindowFingerprint(plan.ModelWindow), Assessment: &choice.assessment})
	if err != nil {
		return model.Request{}, err
	}
	for _, artifact := range choice.artifacts {
		saved, saveErr := a.ports.PutArtifact(ctx, artifact)
		if saveErr != nil {
			return model.Request{}, saveErr
		}
		if !sameBoundedValue(saved, artifact) {
			return model.Request{}, ErrInvalidRequest
		}
	}
	saved, err := a.ports.CommitCheckpoint(ctx, plan, next)
	if err != nil {
		return model.Request{}, err
	}
	if !sameBoundedValue(saved, next) {
		return model.Request{}, ErrInvalidRequest
	}
	if err = a.ports.BindCheckpoint(ctx, plan.Checkpoint.ID, saved); err != nil {
		return model.Request{}, err
	}
	callbackscope.Operation(ctx, a, "context.checkpoint:"+saved.ID, struct {
		RunID, TurnID       string
		TurnRevision        uint64
		Source              json.RawMessage
		Previous, Committed runtimecontext.Checkpoint
		Artifacts           []runtimecontext.Artifact
	}{incoming.RunID, plan.TurnID, plan.TurnRevision, plan.SourceBinding, *plan.Checkpoint, saved, choice.artifacts})
	request.Messages = runtimecontext.Materialize(saved.Window)
	return request, nil
}
func sameBoundedValue(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}
