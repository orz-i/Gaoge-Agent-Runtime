package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ModelOutcomeVerifier is a construction-time trusted, read-only lookup of an
// exact previously dispatched request's settled receipt. It must not reserve,
// generate, retry, project another effect, or infer completion from TTL.
type ModelOutcomeVerifier interface {
	VerifyModelOutcome(context.Context, model.Request) (model.Response, error)
}
type ModelOutcomeVerifierFunc func(context.Context, model.Request) (model.Response, error)

func (f ModelOutcomeVerifierFunc) VerifyModelOutcome(ctx context.Context, r model.Request) (model.Response, error) {
	return f(ctx, r)
}

func (runner *Runner) ReconcileModelOutcome(ctx context.Context, runID string, revision uint64) (kernel.Snapshot, error) {
	if runner == nil || ctx == nil || runID == "" || revision == 0 || runner.modelOutcomeVerifier == nil {
		return kernel.Snapshot{}, ErrModelOutcomeUnknown
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Run.Revision != revision {
		return snapshot, kernel.ErrConflict
	}
	if snapshot.Run.ParentRunID != "" || snapshot.Run.Status != kernel.RunStatusRunning {
		return snapshot, ErrModelOutcomeUnknown
	}
	active, ok := activeModelInvocation(state)
	if !ok || (active.Status != ModelInvocationPending && active.Status != ModelInvocationCompleted) || active.DispatchAttempt == 0 {
		return snapshot, ErrModelOutcomeUnknown
	}
	inv := *active
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, err)
	}
	completion, err := runner.runtime.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if err != nil {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, ErrRootOutcomeUnknown, err)
	}
	if !matchesFrozenProviderBinding(runner.model, inv) {
		return snapshot, ErrProviderBindingMismatch
	}
	request := model.CloneRequest(inv.Request)
	response, receiptErr := runner.modelOutcomeVerifier.VerifyModelOutcome(ctx, request)
	authorityState := state
	if inv.Status == ModelInvocationCompleted {
		authorityState.ModelInvocations = state.ModelInvocations[:len(state.ModelInvocations)-1]
	}
	authority := modelCompletionAuthority{named: inv.Provider != "" && inv.BindingFingerprint != "", request: model.CloneRequest(inv.Request), duplicate: func(id string) bool { return duplicatePhysicalProviderResponseID(authorityState, inv, id) }}
	response, receiptErr = authority.replay(request, response, receiptErr)
	response, receiptErr = authority.validate(response, receiptErr)
	rejected := model.IsResponseRejectedError(receiptErr) && response.Usage != nil && validModelUsage(response.Usage)
	if receiptErr != nil && !rejected {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, receiptErr)
	}
	if !rejected && !validModelResponse(response) {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, ErrInvalidModelResponse)
	}
	if rejected {
		response = model.Response{ResponseID: response.ResponseID, Usage: model.CloneUsage(response.Usage)}
	}
	originalInvocation := inv
	inv.Status = ModelInvocationCompleted
	inv.ProviderResponseID = strings.TrimSpace(response.ResponseID)
	inv.Response = model.CloneResponse(response)
	inv.Usage = model.CloneUsage(response.Usage)
	inv.ExecutionLeaseUntil = nil
	inv.RetryAfter = nil
	if completion.HostCompleted {
		inv.OutcomeErrorCode = ""
		inv.OutcomeErrorDetail = ""
	}
	completedAt := runner.clock.Now().UTC()
	if inv.CompletedAt == nil {
		inv.CompletedAt = &completedAt
	}
	if rejected {
		inv.ErrorCode = model.ClassifiedErrorCode(receiptErr)
		if inv.ErrorCode == "" {
			inv.ErrorCode = rejectedModelResponseCode
		}
	}
	if originalInvocation.Status == ModelInvocationCompleted {
		// Immutable original evidence: neither another receipt nor another
		// verifier response may overwrite it, even under the same call ID.
		if !reflect.DeepEqual(originalInvocation.Response, inv.Response) || !reflect.DeepEqual(originalInvocation.Usage, inv.Usage) || originalInvocation.ProviderResponseID != inv.ProviderResponseID || originalInvocation.ErrorCode != inv.ErrorCode {
			return snapshot, ErrInvalidModelResponse
		}
		if !completion.HostCompleted {
			return snapshot, ErrRootOutcomeUnknown
		}
		if err = runner.ReconcileRootPhysicalExecution(ctx, runID); err != nil {
			return snapshot, err
		}
		return snapshot, nil
	}
	if !validModelInvocationReceipt(inv) || !replaceLastModelInvocation(&state, inv) {
		return snapshot, ErrInvalidModelResponse
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	if !completion.HostCompleted {
		source := featurecontrol.Seal(runID, revision, "agent_owned_model_receipt_evidence_source", grant.ID, snapshot.State, snapshot.Checkpoint)
		target := featurecontrol.Seal(runID, revision, "agent_owned_model_receipt_evidence_target", grant.ID, encoded, snapshot.Checkpoint)
		evidence, evidenceErr := runner.runtime.ApplyOwnedModelReceiptEvidence(ctx, grant, revision, kernel.Mutation{Status: snapshot.Run.Status, State: encoded, Checkpoint: snapshot.Checkpoint, ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail}, source, target)
		if evidenceErr != nil {
			return snapshot, errors.Join(ErrRootOutcomeUnknown, evidenceErr)
		}
		// Full response evidence is retained, not consumed. No budget usage,
		// transcript, Tool authorization, wakeup or Host settlement is minted.
		return evidence, ErrRootOutcomeUnknown
	}
	source := featurecontrol.Seal(runID, revision, "agent_owned_receipt_recovery_source", grant.ID, snapshot.State, snapshot.Checkpoint)
	target := featurecontrol.Seal(runID, revision, "agent_owned_receipt_recovery_target", grant.ID, encoded, snapshot.Checkpoint)
	repaired, err := runner.runtime.ApplyOwnedPhysicalRecovery(ctx, grant, revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, Events: []kernel.EventDraft{{Type: "agent.model_invocation.completed", Message: inv.ID, Wakeup: true}}}, source, target)
	if err != nil {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, err)
	}
	if err = runner.ReconcileRootPhysicalExecution(ctx, runID); err != nil {
		return repaired, err
	}
	return repaired, nil
}
