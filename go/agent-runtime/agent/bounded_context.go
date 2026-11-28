package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
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
	ResolveWindow(context.Context, model.Request) (runtimecontext.ModelWindow, error)
	PutArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, error)
	CommitCheckpoint(context.Context, BoundedContextPlan, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error)
	BindCheckpoint(context.Context, string, runtimecontext.Checkpoint) error
}

// BoundedContextBudgetError exposes only numeric admission facts for a
// rejected model request. The original request, Provider replay, Tool payload,
// Context artifact and model identity never enter this error.
// It wraps ErrBudgetExceeded so existing execution/failure paths are unchanged.
type BoundedContextBudgetError struct {
	Assessment         runtimecontext.Assessment
	MaxSerializedBytes int64
	Reason             string // no_artifact_reader, not_owner, or no_safe_compaction
}

func (*BoundedContextBudgetError) Error() string { return runtimecontext.ErrBudgetExceeded.Error() }
func (*BoundedContextBudgetError) Unwrap() error { return runtimecontext.ErrBudgetExceeded }

func rejectedContextBudget(a runtimecontext.Assessment, policy runtimecontext.Policy, reason string) error {
	return &BoundedContextBudgetError{Assessment: a, MaxSerializedBytes: policy.MaxSerializedBytes, Reason: reason}
}

// candidateRejection is a Runtime-owned no-dispatch decision. It is set
// only after joint candidate assessment/search, never by a Host port error.
// The numeric facts also bind the original owned preparation receipt.
type candidateRejection struct {
	Code       string
	Reason     string
	Assessment runtimecontext.Assessment
	Quota      candidateQuota
	cause      error
}

func (c modelCandidate) reject(policy runtimecontext.Policy, reason string, rejected *candidateRejection) error {
	decision := candidateRejection{Code: "context.input_budget_exceeded", Reason: reason, Assessment: c.assessment,
		Quota: c.quota, cause: rejectedContextBudget(c.assessment, policy, reason)}
	if errors.Is(c.quota.err, budget.ErrExhausted) {
		decision.Code, decision.cause = sharedBudgetErrorCode(c.quota.err), c.quota.err
	}
	if rejected != nil {
		*rejected = decision
	}
	return decision.cause
}

type BoundedContextAdapter struct {
	manager *runtimecontext.Manager
	ports   BoundedContextPorts
}

func NewBoundedContextAdapter(manager *runtimecontext.Manager, ports BoundedContextPorts) (*BoundedContextAdapter, error) {
	if manager == nil || ports == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedContextAdapter{manager: manager, ports: ports}, nil
}
func (a *BoundedContextAdapter) hydrate(ctx context.Context, inv plugin.RunInvocation) (context.Context, BoundedContextHydration, error) {
	hydrated, source, err := a.ports.Hydrate(ctx, inv)
	if err != nil {
		return ctx, BoundedContextHydration{}, err
	}
	if hydrated == nil || source.RunID != inv.RunID || len(source.SourceBinding) == 0 || !json.Valid(source.SourceBinding) {
		return ctx, BoundedContextHydration{}, ErrInvalidRequest
	}
	callbackscope.Operation(ctx, a, "managed.hydrated", source)
	return hydrated, source, nil
}

func (a *BoundedContextAdapter) finishHydration(ctx context.Context, source BoundedContextHydration) {
	// Hydration has returned and all downstream work has actually exited.
	// There is no hidden post-Next Host callback here.
	callbackscope.Record(ctx, a, "context.hydrate.returned", source)
}

func (a *BoundedContextAdapter) prepareManaged(ctx context.Context, incoming model.Request) (prepared model.Request, prepareErr error) {
	callbackscope.ManagedPrepareEntered(ctx, a)
	defer func() { callbackscope.ManagedPrepareReturned(ctx, a, prepared, prepareErr) }()
	return a.prepare(ctx, incoming, nil)
}

// prepare owns the only Context preparation algorithm. Only a rejection
// computed by this concrete adapter can set rejected, never an error supplied
// by a Host port. Managed transfer and ordinary owned execution share it.
func (a *BoundedContextAdapter) prepare(ctx context.Context, incoming model.Request, rejected *candidateRejection) (model.Request, error) {
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
	frame, err := a.manager.FreezeRequestAccounting(ctx, request, plan.ModelWindow)
	if err != nil {
		return model.Request{}, err
	}
	original, err := assessModelCandidate(ctx, a, request, plan.ModelWindow, policy, frame)
	if err != nil {
		return model.Request{}, err
	}
	if original.disposition == candidateExhausted {
		return model.Request{}, original.reject(policy, "shared_budget_exhausted", rejected)
	}
	if original.soft() {
		return original.selectForIntent(ctx)
	}
	if !plan.ArtifactReader || original.omitLocalTools || !plan.Owner {
		if original.selectable() {
			return original.selectForIntent(ctx)
		}
		reason := "no_artifact_reader"
		if plan.ArtifactReader && !original.omitLocalTools {
			reason = "not_owner"
		}
		return model.Request{}, original.reject(policy, reason, rejected)
	}
	choice, ok, err := a.selectCompaction(ctx, *plan.Checkpoint, request, plan.ModelWindow, policy, frame, original)
	if err != nil {
		return model.Request{}, err
	}
	if !ok {
		if original.selectable() {
			return original.selectForIntent(ctx)
		}
		return model.Request{}, original.reject(policy, "no_safe_compaction", rejected)
	}
	next, err := a.manager.Rollover(ctx, runtimecontext.RolloverRequest{Previous: *plan.Checkpoint, Window: choice.window, Artifacts: choice.artifacts, Reason: "model_window_pressure", ModelWindowFingerprint: runtimecontext.ModelWindowFingerprint(choice.modelWindow), Assessment: &choice.assessment})
	if err != nil {
		return model.Request{}, err
	}
	if !sameFrozenModelMessages(choice.request.Messages, model.CloneMessages(runtimecontext.Materialize(next.Window))) {
		return model.Request{}, ErrInvalidRequest
	}
	request, err = choice.selectForIntent(ctx)
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
	return request, nil
}
func sameBoundedValue(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && string(x) == string(y)
}
