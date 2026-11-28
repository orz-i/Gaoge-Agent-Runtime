package agent

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// toolOutcomeReceipt is the same immutable terminal fact in the Budget ledger
// and the Agent's recovered outcome slot. No-effect rejection is not success.
type toolOutcomeReceipt struct {
	Result          tools.ExecutionResult `json:"result"`
	ErrorCode       string                `json:"errorCode,omitempty"`
	ErrorMessage    string                `json:"errorMessage,omitempty"`
	BlockedTools    []string              `json:"blockedTools,omitempty"`
	AllowCompletion bool                  `json:"allowCompletion,omitempty"`
}

func newToolOutcomeReceipt(result tools.ExecutionResult, err error) toolOutcomeReceipt {
	code, message, _ := tools.RecoverableCallErrorInfo(err)
	return toolOutcomeReceipt{Result: tools.CloneExecutionResult(result),
		ErrorCode: code, ErrorMessage: message,
		BlockedTools: tools.RecoverableCallErrorBlockedToolKeys(err), AllowCompletion: tools.RecoverableCallErrorAllowsCompletion(err)}
}

const toolOutcomeEvidenceVersion = "budget-tool-outcome-opaque-v1"

type persistedToolOutcome struct {
	toolOutcomeReceipt
	Evidence *jsoncontract.OpaqueEvidence `json:"_originalEvidence"`
}

func encodeToolOutcome(r toolOutcomeReceipt) (json.RawMessage, error) {
	if _, err := r.terminalError(r.Result.Receipt.ExecutionID); err != nil {
		return nil, err
	}
	seal, err := jsoncontract.SealOpaque(toolOutcomeEvidenceVersion, r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(persistedToolOutcome{toolOutcomeReceipt: r, Evidence: &seal})
}

func decodeToolOutcome(raw json.RawMessage, id string) (toolOutcomeReceipt, error) {
	var saved persistedToolOutcome
	if err := json.Unmarshal(raw, &saved); err != nil {
		return toolOutcomeReceipt{}, err
	}
	r := saved.toolOutcomeReceipt
	if err := jsoncontract.RestoreOpaque(toolOutcomeEvidenceVersion, &r, saved.Evidence); err != nil {
		return toolOutcomeReceipt{}, errors.Join(ErrToolReceiptMismatch, err)
	}
	if _, err := r.terminalError(id); err != nil {
		return toolOutcomeReceipt{}, err
	}
	return r, nil
}

// terminalError distinguishes an exact original refusal from invalid evidence.
// The second error never reaches the model as a recoverable Tool error.
func (r toolOutcomeReceipt) terminalError(id string) (error, error) {
	var refusal error
	if r.ErrorCode != "" {
		refusal = tools.NewRecoverableCallErrorWithBlockedTools(r.ErrorCode, r.ErrorMessage, nil, r.BlockedTools...)
		var typed *tools.RecoverableCallError
		if !errors.As(refusal, &typed) || typed.Code != r.ErrorCode || typed.Message != r.ErrorMessage ||
			!slices.Equal(typed.BlockedToolKeys, r.BlockedTools) {
			return nil, ErrToolReceiptMismatch
		}
		typed.AllowCompletion = r.AllowCompletion
	} else if r.ErrorMessage != "" || len(r.BlockedTools) != 0 || r.AllowCompletion {
		return nil, ErrToolReceiptMismatch
	}
	if terminal, _ := qualifyToolReturn(r.Result, refusal, id); !terminal {
		return nil, ErrToolReceiptMismatch
	}
	return refusal, nil
}
