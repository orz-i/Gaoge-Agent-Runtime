package agent

import (
	"context"
	"errors"
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
	if !ok || active.DispatchAttempt == 0 {
		return snapshot, ErrModelOutcomeUnknown
	}
	inv := *active
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, err)
	}
	completion, err := runner.runtime.LookupPhysicalCallbackReturn(ctx, grant)
	if err != nil {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, ErrRootOutcomeUnknown, err)
	}
	if completion.NotEntered() {
		return snapshot, ErrModelOutcomeUnknown
	}
	if !matchesFrozenProviderBinding(runner.model, inv) {
		return snapshot, ErrProviderBindingMismatch
	}
	// The verifier only sees the exact committed physical request. A missing
	// plan after dispatch is not evidence that logical and physical were equal.
	if inv.SamplingPlan == nil || !inv.SamplingPlan.valid(inv.Request) || inv.SamplingPlan.LedgerID != snapshot.Run.BudgetScopeID {
		return snapshot, ErrModelOutcomeUnknown
	}
	request := inv.SamplingPlan.request(inv.Request)
	response, receiptErr := runner.recoveryModelReceipt(ctx, inv, request, completion.HostCompleted)
	originalInvocation := inv
	var ledgerMutation *budget.LedgerMutation
	if a := runner.modelBudget(inv.SamplingPlan); a != nil && completion.HostCompleted && inv.Status == ModelInvocationPending {
		res, loadErr := runner.originalModelReservation(ctx, inv)
		if loadErr != nil {
			return snapshot, errors.Join(ErrModelOutcomeUnknown, loadErr)
		}
		if res.Status == budget.ReservationHeld {
			// Validate the actual original verifier receipt before computing
			// accounting (including hosted effects and quota rejection).
			if _, err = runner.qualifiedRecoveredModelReceipt(state, inv, response, receiptErr, true); err != nil {
				return snapshot, err
			}
			ledgerMutation, response, receiptErr, err = a.planModelReceipt(ctx, inv, response, receiptErr)
			if err != nil {
				return snapshot, errors.Join(ErrModelOutcomeUnknown, err)
			}
		}
	}
	inv, err = runner.qualifiedRecoveredModelReceipt(state, inv, response, receiptErr, completion.HostCompleted)
	if err != nil {
		return snapshot, err
	}
	if originalInvocation.Status == ModelInvocationCompleted {
		// Immutable original evidence: neither another receipt nor another
		// verifier response may overwrite it, even under the same call ID.
		if !samePersistedModelResponse(originalInvocation.Response, inv.Response) || originalInvocation.ProviderResponseID != inv.ProviderResponseID || originalInvocation.ErrorCode != inv.ErrorCode {
			return snapshot, ErrInvalidModelResponse
		}
		if !completion.HostCompleted {
			return snapshot, ErrRootOutcomeUnknown
		}
		if err = runner.ReconcilePhysicalExecution(ctx, runID); err != nil {
			return snapshot, err
		}
		return snapshot, nil
	}
	if !replaceLastModelInvocation(&state, inv) {
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
	repaired, err := runner.commitReturnedState(ctx, snapshot, grant, kernel.Mutation{Ledger: ledgerMutation, Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint, Events: []kernel.EventDraft{{Type: "agent.model_invocation.completed", Message: inv.ID, Wakeup: true}}})
	if err != nil {
		return snapshot, errors.Join(ErrModelOutcomeUnknown, err)
	}
	if err = runner.ReconcilePhysicalExecution(ctx, runID); err != nil {
		return repaired, err
	}
	return repaired, nil
}

// Both an ordinary returned callback and a qualified managed successor
// use the same exact receipt identity and rejection reducer. The caller
// still owns its separate Store mutation/retirement authority.
func (runner *Runner) qualifiedRecoveredModelReceipt(state runState, inv ModelInvocation, response model.Response, receiptErr error, clearDiagnostic bool) (ModelInvocation, error) {
	if inv.SamplingPlan == nil || !inv.SamplingPlan.valid(inv.Request) {
		return ModelInvocation{}, ErrModelOutcomeUnknown
	}
	authorityState := state
	if inv.Status == ModelInvocationCompleted {
		authorityState.ModelInvocations = state.ModelInvocations[:len(state.ModelInvocations)-1]
	}
	authority := modelCompletionAuthority{named: inv.Provider != "" && inv.BindingFingerprint != "", request: model.CloneRequest(inv.Request), plan: inv.SamplingPlan, duplicate: func(id string) bool { return duplicatePhysicalProviderResponseID(authorityState, inv, id) }}
	response, receiptErr = authority.replay(inv.SamplingPlan.request(inv.Request), response, receiptErr)
	response, receiptErr = authority.validate(response, receiptErr)
	rejected := model.IsResponseRejectedError(receiptErr) && response.Usage != nil && model.ValidUsage(response.Usage)
	if receiptErr != nil && !rejected {
		return ModelInvocation{}, errors.Join(ErrModelOutcomeUnknown, receiptErr)
	}
	if !rejected && !model.ValidResponse(response) {
		return ModelInvocation{}, errors.Join(ErrModelOutcomeUnknown, ErrInvalidModelResponse)
	}
	inv = completeModelInvocation(inv, response, receiptErr, rejected, runner.clock.Now().UTC())
	if clearDiagnostic {
		inv.OutcomeErrorCode, inv.OutcomeErrorDetail = "", ""
	}
	if !validModelInvocationReceipt(inv) {
		return ModelInvocation{}, ErrInvalidModelResponse
	}
	return inv, nil
}

// A SharedBudget invocation has one qualification owner: its original
// reservation. An external receipt can prove Provider evidence while that
// reservation is still held, but cannot certify its settlement or override
// its committed rejection. Reads never acquire or alter budget rights.
func (runner *Runner) originalModelReservation(ctx context.Context, inv ModelInvocation) (budget.Reservation, error) {
	plan := inv.SamplingPlan
	if plan == nil || plan.LedgerID == "" || !plan.valid(inv.Request) || runner.boundedBudget == nil {
		return budget.Reservation{}, ErrInvalidRequest
	}
	expected, err := plan.reservation(inv.Request)
	if err != nil {
		return budget.Reservation{}, err
	}
	ledger, err := runner.boundedBudget.bindReceiptScope(ctx, inv.RunID, plan.LedgerID)
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
	return matchesSettledModelReceipt(inv, res)
}

// Match the exact original receipt for both ordinary consumption and managed
// qualification. A classified rejection is settled evidence, not a retry.
func matchesSettledModelReceipt(inv ModelInvocation, res budget.Reservation) error {
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
		inv.ProviderResponseID != strings.TrimSpace(response.ResponseID) {
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
