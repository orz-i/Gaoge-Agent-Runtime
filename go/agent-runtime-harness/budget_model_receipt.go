package harness

import (
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var errBudgetResponseRejected = errors.New("completed model response was rejected")

// Embedding keeps existing successful Response receipts wire-compatible. Only a
// bounded failure code is persisted; provider error text is not an accounting fact.
type budgetModelReceipt struct {
	model.Response
	Rejected *budgetRejectedResponse `json:"rejected,omitempty"`
}

type budgetRejectedResponse struct {
	Code string `json:"code,omitempty"`
}

// Once the authoritative ledger receipt is committed, projection/validation
// errors must preserve its final usage and cannot authorize another generation.
func settledModelResult(response model.Response, callErr, localErr error) (model.Response, error) {
	if localErr != nil {
		callErr = model.NewClassifiedError(errors.Join(callErr, localErr), "model.post_generation_failed")
		if response.Usage != nil {
			callErr = model.NewResponseRejectedError(callErr)
		}
	}
	return response, callErr
}

func encodeBudgetModelReceipt(response model.Response, callErr error) ([]byte, error) {
	receipt := budgetModelReceipt{Response: response}
	if callErr != nil {
		receipt.Rejected = &budgetRejectedResponse{Code: model.ClassifiedErrorCode(callErr)}
	}
	return json.Marshal(receipt)
}

func decodeBudgetModelReceipt(raw json.RawMessage) (model.Response, error) {
	var receipt budgetModelReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return model.Response{}, err
	}
	if receipt.Rejected != nil {
		return receipt.Response, model.NewResponseRejectedError(model.NewClassifiedError(errBudgetResponseRejected, receipt.Rejected.Code))
	}
	return receipt.Response, nil
}
