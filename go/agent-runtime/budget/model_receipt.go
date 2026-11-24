package budget

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const modelReceiptEvidenceVersion = "budget-model-receipt-opaque-v1"

var errModelResponseRejected = errors.New("completed model response was rejected")

type modelReceiptRejection struct {
	Code  string `json:"code,omitempty"`
	Quota bool   `json:"quota,omitempty"`
}

type modelReceiptPayload struct {
	model.Response
	Rejected *modelReceiptRejection `json:"rejected,omitempty"`
}

type persistedModelReceipt struct {
	modelReceiptPayload
	Evidence *jsoncontract.OpaqueEvidence `json:"_originalEvidence"`
}

// Bind the evidence seal to the exact reservation and accounted usage without
// storing a second copy of the request, usage or hosted receipt ledger.
type modelReceiptEvidence struct {
	ID, RunID, RequestHash, PhysicalRequestHash string
	Requested, Usage                            Usage
	Payload                                     modelReceiptPayload
}

func receiptEvidence(res Reservation, payload modelReceiptPayload) modelReceiptEvidence {
	return modelReceiptEvidence{ID: res.ID, RunID: res.RunID, RequestHash: res.RequestHash, PhysicalRequestHash: res.PhysicalRequestHash, Requested: res.Requested, Usage: res.Usage, Payload: payload}
}
func encodeModelReceipt(res Reservation, payload modelReceiptPayload) (json.RawMessage, error) {
	seal, err := jsoncontract.SealOpaque(modelReceiptEvidenceVersion, receiptEvidence(res, payload))
	if err != nil {
		return nil, err
	}
	return json.Marshal(persistedModelReceipt{modelReceiptPayload: payload, Evidence: &seal})
}

// DecodeModelReceipt reads one immutable settled model outcome. Missing or
// contradictory original evidence never becomes an accepted response.
func DecodeModelReceipt(res Reservation) (model.Response, error) {
	if res.Status != ReservationSettled || !res.Dispatched || res.PhysicalRequestHash == "" || len(res.Receipt) == 0 {
		return model.Response{}, ErrLedgerConflict
	}
	var saved persistedModelReceipt
	if err := json.Unmarshal(res.Receipt, &saved); err != nil {
		return model.Response{}, err
	}
	evidence := receiptEvidence(res, saved.modelReceiptPayload)
	if err := jsoncontract.RestoreOpaque(modelReceiptEvidenceVersion, &evidence, saved.Evidence); err != nil {
		return model.Response{}, errors.Join(ErrLedgerConflict, err)
	}
	payload := evidence.Payload
	if payload.Rejected != nil {
		cause := errModelResponseRejected
		if payload.Rejected.Quota {
			cause = errors.Join(cause, ErrInvalidUsage)
		}
		return model.BillingOnlyResponse(payload.Response), model.NewResponseRejectedError(model.NewClassifiedError(cause, payload.Rejected.Code))
	}
	return payload.Response, nil
}

// SettleModel commits actual usage, original response evidence, related hosted
// effects and execution qualification together on the same ledger CAS. Each
// CAS attempt computes transfers against the current authoritative snapshot;
// it cannot borrow a stale quota view or reclassify an already settled result.
// A rejected completed response is still charged. Unknown effects never settle.
func (coordinator Coordinator) SettleModel(ctx context.Context, scopeID string, request model.Request, response model.Response, callErr error) (Ledger, error) {
	hash, err := model.PhysicalRequestHash(request)
	if err != nil {
		return Ledger{}, err
	}
	if request.InvocationID == "" || request.RunID == "" || !model.ValidUsage(response.Usage) || (callErr != nil && (!model.IsResponseRejectedError(callErr) || response.Usage == nil)) {
		return Ledger{}, ErrInvalidUsage
	}
	response = model.CloneResponse(response)
	var tokens *TokenUsage
	if u := response.Usage; u != nil {
		tokens = &TokenUsage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens}
	}
	observed, err := ChargeModelCall(Usage{}, tokens)
	if err != nil {
		return Ledger{}, err
	}
	id := "model:" + request.InvocationID
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		current, ok := ledger.Reservations[id]
		if !ok || current.ID != id || current.RunID != request.RunID || current.PhysicalRequestHash != hash || !current.Dispatched || current.Status != ReservationHeld {
			return ErrLedgerConflict
		}
		if ledger.HasRunTokenLimits(current.RunID) && response.Usage == nil {
			return ErrInvalidUsage
		}
		hosted, related, err := hostedReceiptTransfers(*ledger, current.RunID, response.HostedToolCalls)
		if err != nil {
			return err
		}
		usage := observed
		usage.HostedToolCalls, usage.ToolCalls = hosted, hosted
		// Existing pending native effects are already reserved. Only new IDs
		// introduced by this physical response consume its new-execution quota.
		newHosted := 0
		for _, effect := range related {
			if _, exists := ledger.Reservations[effect.ID]; !exists {
				newHosted++
			}
		}
		_, hostedLimited := ledger.RemainingHostedCalls(current.RunID)
		quotaRejected := (ledger.HasRunTokenLimits(current.RunID) && (usage.InputTokens > current.Requested.InputTokens || usage.OutputTokens > current.Requested.OutputTokens)) ||
			(request.MaxOutputTokens > 0 && usage.OutputTokens > request.MaxOutputTokens) ||
			(hostedLimited && newHosted > current.Requested.HostedToolCalls) ||
			(request.HostedToolCallLimit != nil && newHosted > *request.HostedToolCallLimit)
		payload := modelReceiptPayload{Response: response}
		switch {
		case quotaRejected:
			payload.Rejected = &modelReceiptRejection{Code: "model.post_generation_failed", Quota: true}
		case model.IsResponseRejectedError(callErr):
			payload.Rejected = &modelReceiptRejection{Code: model.ClassifiedErrorCode(callErr)}
		case !model.ValidResponse(response):
			// A final usage receipt with an invalid result has no execution
			// qualification. Missing usage cannot make an invalid result known.
			if response.Usage == nil {
				return ErrInvalidUsage
			}
			payload.Rejected = &modelReceiptRejection{}
		}
		if payload.Rejected != nil {
			payload.Response = model.BillingOnlyResponse(response)
		}
		current.Usage = usage
		raw, err := encodeModelReceipt(current, payload)
		if err != nil {
			return err
		}
		return ledger.applyReceiptSettlement(id, usage, raw, related)
	})
}
