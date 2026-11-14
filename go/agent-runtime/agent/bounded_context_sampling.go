package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// samplingDisposition is private and can only be produced by the concrete
// algorithm below, never by matching a port error's type or message.
type samplingDisposition uint8

const (
	samplingUnknown samplingDisposition = iota
	samplingPrepared
	samplingHardRejected
)

func (a *BoundedContextAdapter) prepareSampling(ctx context.Context, incoming model.Request, source kernel.Snapshot) (model.Request, samplingDisposition, error) {
	plan, err := a.sampling.PlanSampling(ctx, model.CloneRequest(incoming), cloneSamplingSnapshot(source))
	if err != nil {
		return model.Request{}, samplingUnknown, err
	}
	plan = cloneSamplingPlan(plan)
	if !sameBoundedValue(plan.Source, source) || plan.Request.RunID != incoming.RunID {
		return model.Request{}, samplingUnknown, ErrInvalidRequest
	}
	if !a.recordSamplingOperation(ctx, "plan", plan) {
		return model.Request{}, samplingUnknown, ErrInvalidRequest
	}
	plan.Policy = runtimecontext.NormalizePolicy(plan.Policy)
	if plan.Checkpoint != nil {
		if !runtimecontext.ValidCheckpoint(*plan.Checkpoint) || !sameBoundedValue(*plan.Checkpoint, plan.Cursor.Checkpoint) {
			return model.Request{}, samplingUnknown, ErrInvalidRequest
		}
		if plan.Owner && (plan.Cursor.Turn.ID == "" || plan.Cursor.Turn.Revision == 0 || plan.Cursor.UpdatedAt.IsZero() ||
			plan.Cursor.ExpectedTurnCheckpointID != plan.Checkpoint.ID || plan.Cursor.Turn.ContextCheckpointID != plan.Checkpoint.ID ||
			!sameBoundedValue(plan.Cursor.Turn.ContextRef, samplingRef(*plan.Checkpoint))) {
			return model.Request{}, samplingUnknown, ErrInvalidRequest
		}
	}
	if plan.Owner && plan.Checkpoint == nil {
		return model.Request{}, samplingUnknown, ErrInvalidRequest
	}
	if plan.Owner {
		next, e := a.manager.BindModelWindow(ctx, *plan.Checkpoint, plan.WindowFingerprint)
		if e != nil {
			return model.Request{}, samplingUnknown, e
		}
		if e = a.commitSampling(ctx, &plan, next); e != nil {
			return model.Request{}, samplingUnknown, e
		}
	}
	request := model.CloneRequest(plan.Request)
	var artifacts []runtimecontext.Artifact
	var window runtimecontext.Window
	if plan.Checkpoint != nil {
		window = plan.Checkpoint.Window
		prefix := len(runtimecontext.Materialize(window))
		if prefix > len(request.Messages) {
			return model.Request{}, samplingUnknown, runtimecontext.ErrLineageConflict
		}
		compact, e := a.manager.CompactToolResults(plan.Checkpoint.ScopeID, plan.Checkpoint.Generation, request.Messages[prefix:], plan.Policy.MaxToolResultBytes)
		if e != nil {
			return model.Request{}, samplingUnknown, e
		}
		request.Messages = append(model.CloneMessages(request.Messages[:prefix]), compact.Messages...)
		artifacts = runtimecontext.CloneArtifacts(compact.Artifacts)
	} else if plan.DetachedScope != "" {
		compact, e := a.manager.CompactToolResults(plan.DetachedScope, 1, request.Messages, plan.Policy.MaxToolResultBytes)
		if e != nil {
			return model.Request{}, samplingUnknown, e
		}
		request.Messages = compact.Messages
		artifacts = runtimecontext.CloneArtifacts(compact.Artifacts)
	}
	initial, err := a.evaluateSampling(ctx, plan, request, window, false)
	if err != nil {
		return model.Request{}, samplingUnknown, err
	}
	if !samplingSoft(initial.Assessment, plan.Policy) || !runtimecontext.WithinHardBudget(initial.Assessment, plan.Policy) {
		initial, err = a.evaluateSampling(ctx, plan, request, window, true)
		if err != nil {
			return model.Request{}, samplingUnknown, err
		}
		if err = a.putSamplingArtifacts(ctx, initial.Artifacts); err != nil {
			return model.Request{}, samplingUnknown, err
		}
	}
	within := runtimecontext.WithinHardBudget(initial.Assessment, plan.Policy)
	if within && samplingSoft(initial.Assessment, plan.Policy) {
		err = a.captureSampling(ctx, &plan, request, artifacts, initial.Assessment)
		if err != nil {
			return model.Request{}, samplingUnknown, err
		}
		return initial.Request, samplingPrepared, nil
	}
	if plan.Checkpoint == nil || (!plan.Owner && !plan.ArtifactReader) {
		if within {
			return initial.Request, samplingPrepared, nil
		}
		return samplingRejected(initial.Assessment)
	}
	// Try candidates in the original sequence; only pure Manager budget
	// rejection is a candidate miss. A port error never selects another candidate.
	try := func(w runtimecontext.Window, extra []runtimecontext.Artifact, reason string) (model.Request, bool, error) {
		all := append(runtimecontext.CloneArtifacts(artifacts), extra...)
		target := *plan.Checkpoint
		var e error
		if plan.Owner {
			target, e = a.manager.Rollover(ctx, runtimecontext.RolloverRequest{Previous: *plan.Checkpoint, Window: w, Artifacts: all, Reason: reason, ModelWindowFingerprint: plan.WindowFingerprint, Assessment: &initial.Assessment})
			if e != nil {
				return model.Request{}, false, e
			}
		} else {
			target.Window = w
		}
		trial := model.CloneRequest(request)
		trial.Messages = append(runtimecontext.Materialize(target.Window), model.CloneMessages(plan.Guidance)...)
		evaluated, e := a.evaluateSampling(ctx, plan, trial, target.Window, true)
		if e != nil {
			return model.Request{}, false, e
		}
		// Original native evaluation persists its exact archival facts even if a
		// candidate still exceeds budget. Persist before trying the next candidate.
		if e = a.putSamplingArtifacts(ctx, evaluated.Artifacts); e != nil {
			return model.Request{}, false, e
		}
		if !runtimecontext.WithinHardBudget(evaluated.Assessment, plan.Policy) {
			return model.Request{}, false, nil
		}
		if e = a.putSamplingArtifacts(ctx, all); e != nil {
			return model.Request{}, false, e
		}
		if plan.Owner {
			if e = a.commitSampling(ctx, &plan, target); e != nil {
				return model.Request{}, false, e
			}
		}
		return evaluated.Request, true, nil
	}
	portable := func(policy runtimecontext.Policy, reason string) (model.Request, bool, error) {
		for _, omit := range []bool{false, true} {
			candidate, e := a.manager.CompactPortable(runtimecontext.PortableCompactionRequest{Previous: *plan.Checkpoint, RunID: request.RunID, Messages: samplingDurable(request.Messages, plan), Policy: policy, OmitExtract: omit})
			if errors.Is(e, runtimecontext.ErrBudgetExceeded) {
				return model.Request{}, false, nil
			}
			if e != nil {
				return model.Request{}, false, e
			}
			result, ok, e := try(candidate.Window, candidate.Artifacts, reason)
			if e != nil || ok {
				return result, ok, e
			}
		}
		return model.Request{}, false, nil
	}
	if result, ok, e := portable(plan.Policy, "soft_limit"); e != nil {
		return model.Request{}, samplingUnknown, e
	} else if ok {
		return result, samplingPrepared, nil
	}
	pressure, e := a.manager.CompactToolPressure(*plan.Checkpoint, request.RunID, samplingDurable(request.Messages, plan))
	if e != nil && !errors.Is(e, runtimecontext.ErrBudgetExceeded) {
		return model.Request{}, samplingUnknown, e
	}
	if e == nil {
		if result, ok, e := try(pressure.Window, pressure.Artifacts, "tool_pressure"); e != nil {
			return model.Request{}, samplingUnknown, e
		} else if ok {
			return result, samplingPrepared, nil
		}
	}
	if within {
		if e = a.captureSampling(ctx, &plan, request, artifacts, initial.Assessment); e != nil {
			return model.Request{}, samplingUnknown, e
		}
		return initial.Request, samplingPrepared, nil
	}
	if plan.Policy.MinimumRecentTurns < plan.Policy.PreserveRecentTurns {
		minimum := plan.Policy
		minimum.PreserveRecentTurns = minimum.MinimumRecentTurns
		if result, ok, e := portable(minimum, "hard_limit"); e != nil {
			return model.Request{}, samplingUnknown, e
		} else if ok {
			return result, samplingPrepared, nil
		}
	}
	return samplingRejected(initial.Assessment)
}
func samplingRejected(assessment runtimecontext.Assessment) (model.Request, samplingDisposition, error) {
	return model.Request{}, samplingHardRejected, model.NewClassifiedError(runtimecontext.ErrBudgetExceeded, "context.input_budget_exceeded")
}
func samplingSoft(a runtimecontext.Assessment, p runtimecontext.Policy) bool {
	return a.SoftInputTokens > 0 && a.AdjustedTokenEstimate <= a.SoftInputTokens && a.SerializedBytes <= p.MaxSerializedBytes
}
func samplingDurable(messages []model.Message, p SamplingPlan) []model.Message {
	out := model.CloneMessages(messages)
	n := len(p.Guidance)
	if n > 0 && p.GuidanceStart >= 0 && p.GuidanceStart+n <= len(out) {
		out = append(out[:p.GuidanceStart], out[p.GuidanceStart+n:]...)
	}
	return out
}
func (a *BoundedContextAdapter) evaluateSampling(ctx context.Context, p SamplingPlan, r model.Request, w runtimecontext.Window, native bool) (SamplingEvaluation, error) {
	result, err := a.sampling.EvaluateSampling(ctx, cloneSamplingPlan(p), model.CloneRequest(r), runtimecontext.CloneWindow(w), native)
	if err != nil {
		return SamplingEvaluation{}, err
	}
	if !a.recordSamplingOperation(ctx, "evaluate", struct {
		Plan    SamplingPlan
		Request model.Request
		Window  runtimecontext.Window
		Native  bool
		Result  SamplingEvaluation
	}{p, r, w, native, result}) {
		return SamplingEvaluation{}, ErrInvalidRequest
	}
	return cloneSamplingEvaluation(result), nil
}
func (a *BoundedContextAdapter) putSamplingArtifacts(ctx context.Context, artifacts []runtimecontext.Artifact) error {
	for _, artifact := range artifacts {
		expected := runtimecontext.CloneArtifacts([]runtimecontext.Artifact{artifact})[0]
		got, err := a.sampling.PutArtifact(ctx, runtimecontext.CloneArtifacts([]runtimecontext.Artifact{artifact})[0])
		if err != nil {
			return err
		}
		if !sameBoundedValue(got, expected) {
			return ErrInvalidRequest
		}
		if !a.recordSamplingOperation(ctx, "artifact", expected) {
			return ErrInvalidRequest
		}
	}
	return nil
}
func (a *BoundedContextAdapter) captureSampling(ctx context.Context, p *SamplingPlan, r model.Request, artifacts []runtimecontext.Artifact, assessment runtimecontext.Assessment) error {
	if !p.Owner {
		return a.putSamplingArtifacts(ctx, artifacts)
	}
	next, err := a.manager.Capture(ctx, runtimecontext.CaptureRequest{Previous: *p.Checkpoint, StaticFingerprint: p.Checkpoint.StaticFingerprint, RunID: r.RunID, Messages: samplingDurable(r.Messages, *p), Artifacts: artifacts, Assessment: &assessment})
	if err != nil || next.ID == p.Checkpoint.ID {
		return err
	}
	if err = a.putSamplingArtifacts(ctx, artifacts); err != nil {
		return err
	}
	return a.commitSampling(ctx, p, next)
}
func (a *BoundedContextAdapter) commitSampling(ctx context.Context, p *SamplingPlan, next runtimecontext.Checkpoint) error {
	if next.ID == p.Checkpoint.ID {
		return nil
	}
	cursor := cloneSamplingCursor(p.Cursor)
	expected := cursor.Turn
	expected.ContextCheckpointID = next.ID
	expected.ContextRef = samplingRef(next)
	expected.Revision++
	expected.UpdatedAt = cursor.UpdatedAt
	receipt, err := a.sampling.CommitSampling(ctx, cloneSamplingCursor(cursor), runtimecontext.CloneCheckpoint(next))
	if err != nil {
		return err
	}
	if !sameBoundedValue(receipt.Turn, expected) || !sameBoundedValue(receipt.Checkpoint, next) || receipt.ExpectedTurnCheckpointID != cursor.ExpectedTurnCheckpointID || receipt.ExpectedHeadCheckpointID != cursor.ExpectedHeadCheckpointID {
		return ErrInvalidRequest
	}
	bound, err := a.sampling.BindSampling(ctx, cloneSamplingCursor(cursor), runtimecontext.CloneCheckpoint(next))
	if err != nil {
		return err
	}
	if bound.TurnID != cursor.Turn.ID || bound.Access != "owner" || !sameBoundedValue(bound.Previous, cursor.Checkpoint) || !sameBoundedValue(bound.Current, next) {
		return ErrInvalidRequest
	}
	if !a.recordSamplingOperation(ctx, "checkpoint", struct {
		Cursor  SamplingCursor
		Commit  SamplingCommitReceipt
		Binding SamplingBindingReceipt
	}{cursor, receipt, bound}) {
		return ErrInvalidRequest
	}
	p.Cursor.Turn = expected
	p.Cursor.Checkpoint = runtimecontext.CloneCheckpoint(next)
	p.Cursor.ExpectedTurnCheckpointID = next.ID
	p.Cursor.ExpectedHeadCheckpointID = next.ID
	copy := runtimecontext.CloneCheckpoint(next)
	p.Checkpoint = &copy
	return nil
}

func (a *BoundedContextAdapter) recordSamplingOperation(ctx context.Context, stage string, source any) bool {
	raw, err := json.Marshal(source)
	if err != nil {
		return false
	}
	hash := sha256.Sum256(raw)
	return callbackscope.Operation(ctx, a, "sampling."+stage+":"+hex.EncodeToString(hash[:]), source)
}
