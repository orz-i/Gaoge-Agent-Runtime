package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ModelOutcomeVerifier is a construction-time trusted, read-only lookup of an
// exact previously dispatched physical SDK request's settled receipt. It must not reserve,
// generate, retry, project another effect, or infer completion from TTL.
type ModelOutcomeVerifier interface {
	VerifyModelOutcome(context.Context, model.Request) (model.Response, error)
}
type ModelOutcomeVerifierFunc func(context.Context, model.Request) (model.Response, error)

func (f ModelOutcomeVerifierFunc) VerifyModelOutcome(ctx context.Context, r model.Request) (model.Response, error) {
	return f(ctx, r)
}

func (runner *Runner) ReconcileModelOutcome(ctx context.Context, runID string, revision uint64) (kernel.Snapshot, error) {
	if runner == nil || ctx == nil || runID == "" || revision == 0 {
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
	// The verifier only sees the exact committed physical request. A missing
	// plan after dispatch is not evidence that logical and physical were equal.
	if inv.SamplingPlan == nil || !inv.SamplingPlan.valid(inv.Request) {
		return snapshot, ErrModelOutcomeUnknown
	}
	request := inv.SamplingPlan.request(inv.Request)
	response, receiptErr := runner.recoveryModelReceipt(ctx, inv, request, completion.HostCompleted)
	authorityState := state
	if inv.Status == ModelInvocationCompleted {
		authorityState.ModelInvocations = state.ModelInvocations[:len(state.ModelInvocations)-1]
	}
	authority := modelCompletionAuthority{named: inv.Provider != "" && inv.BindingFingerprint != "", request: model.CloneRequest(inv.Request), plan: inv.SamplingPlan, duplicate: func(id string) bool { return duplicatePhysicalProviderResponseID(authorityState, inv, id) }}
	response, receiptErr = authority.replay(request, response, receiptErr)
	response, receiptErr = authority.validate(response, receiptErr)
	rejected := model.IsResponseRejectedError(receiptErr) && response.Usage != nil && model.ValidUsage(response.Usage)
	if receiptErr != nil && !rejected {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, receiptErr)
	}
	if !rejected && !model.ValidResponse(response) {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, ErrInvalidModelResponse)
	}
	if rejected {
		response = model.BillingOnlyResponse(response)
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
		if !samePersistedModelResponse(originalInvocation.Response, inv.Response) || !reflect.DeepEqual(originalInvocation.Usage, inv.Usage) || originalInvocation.ProviderResponseID != inv.ProviderResponseID || originalInvocation.ErrorCode != inv.ErrorCode {
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

// A SharedBudget invocation has one qualification owner: its original
// reservation. An external receipt can prove Provider evidence while that
// reservation is still held, but cannot certify its settlement or override
// its committed rejection. Reads never acquire or alter budget rights.
func (runner *Runner) originalModelReservation(ctx context.Context, inv ModelInvocation) (budget.Reservation, error) {
	plan := inv.SamplingPlan
	if plan == nil || plan.LedgerID == "" || !plan.valid(inv.Request) || runner.samplingBudget == nil {
		return budget.Reservation{}, ErrInvalidRequest
	}
	expected, err := plan.reservation(inv.Request)
	if err != nil {
		return budget.Reservation{}, err
	}
	ledger, err := runner.samplingBudget.coordinator.Store.LoadBudget(ctx, plan.LedgerID)
	if err != nil {
		return budget.Reservation{}, err
	}
	actual, exists := ledger.Reservations[expected.ID]
	if ledger.ID != plan.LedgerID || !exists || actual.ID != expected.ID ||
		actual.RunID != expected.RunID || actual.RequestHash != expected.RequestHash ||
		actual.PhysicalRequestHash != expected.PhysicalRequestHash || actual.Requested != expected.Requested ||
		!actual.Dispatched || (actual.Status != budget.ReservationHeld && actual.Status != budget.ReservationSettled) {
		return budget.Reservation{}, budget.ErrLedgerConflict
	}
	if _, exists := ledger.Runs[expected.RunID]; !exists {
		return budget.Reservation{}, budget.ErrLedgerConflict
	}
	return actual, nil
}

func (runner *Runner) recoveryModelReceipt(ctx context.Context, inv ModelInvocation, request model.Request, hostCompleted bool) (model.Response, error) {
	if inv.SamplingPlan.LedgerID != "" {
		res, err := runner.originalModelReservation(ctx, inv)
		if err != nil {
			return model.Response{}, err
		}
		if res.Status == budget.ReservationSettled {
			return budget.DecodeModelReceipt(res)
		}
		if hostCompleted {
			return model.Response{}, budget.ErrWaiting
		}
	}
	if runner.modelOutcomeVerifier == nil {
		return model.Response{}, ErrModelOutcomeUnknown
	}
	return runner.modelOutcomeVerifier.VerifyModelOutcome(ctx, request)
}

// A recovered or previously committed receipt may only be consumed after
// its original budget qualifies it. A stricter local rejection (for example
// a post-settlement projection error) cannot promote the ledger's response.
func (runner *Runner) requireSettledModelQualification(ctx context.Context, inv ModelInvocation) error {
	if inv.Status != ModelInvocationCompleted || inv.SamplingPlan == nil || inv.SamplingPlan.LedgerID == "" {
		return nil
	}
	res, err := runner.originalModelReservation(ctx, inv)
	if err != nil {
		return errors.Join(ErrModelOutcomeUnknown, err)
	}
	if res.Status != budget.ReservationSettled {
		return errors.Join(ErrModelOutcomeUnknown, budget.ErrWaiting)
	}
	response, err := budget.DecodeModelReceipt(res)
	if err != nil && !model.IsResponseRejectedError(err) {
		return errors.Join(ErrModelOutcomeUnknown, err)
	}
	if model.IsResponseRejectedError(err) {
		code := model.ClassifiedErrorCode(err)
		if code == "" {
			code = rejectedModelResponseCode
		}
		if inv.ErrorCode != code {
			return errors.Join(ErrModelOutcomeUnknown, ErrInvalidModelResponse)
		}
	}
	if inv.ErrorCode != "" {
		response = model.BillingOnlyResponse(response)
	}
	if !samePersistedModelResponse(inv.Response, response) ||
		!reflect.DeepEqual(inv.Usage, response.Usage) || inv.ProviderResponseID != strings.TrimSpace(response.ResponseID) {
		return errors.Join(ErrModelOutcomeUnknown, ErrInvalidModelResponse)
	}
	return nil
}

// Compare the same lossless original response identity used by the receipt codec.
func samePersistedModelResponse(left, right model.Response) bool {
	a, ae := model.ResponseEvidenceHash(left)
	b, be := model.ResponseEvidenceHash(right)
	return ae == nil && be == nil && a == b
}
