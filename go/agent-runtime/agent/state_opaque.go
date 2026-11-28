package agent

import (
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// The current seal preserves missing RawMessage values at required JSON
// positions. Model identity and receipts share the same lossless codec.
const originalStateEvidenceVersion = "agent-state-opaque-v4"

type originalStateEvidence = jsoncontract.OpaqueEvidence
type originalStateRawField = jsoncontract.OpaqueField
type persistedRunState struct {
	runState
	OriginalStateEvidence *originalStateEvidence `json:"_originalStateEvidence"`
}

func encodeOriginalRunState(state runState) (json.RawMessage, error) {
	seal, err := jsoncontract.SealOpaque(originalStateEvidenceVersion, state)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	encoded, err := json.Marshal(persistedRunState{runState: state, OriginalStateEvidence: &seal})
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	return encoded, nil
}
func decodeOriginalRunState(encoded json.RawMessage) (runState, error) {
	var saved persistedRunState
	if err := json.Unmarshal(encoded, &saved); err != nil {
		return runState{}, errors.Join(ErrInvalidRequest, err)
	}
	if err := jsoncontract.RestoreOpaque(originalStateEvidenceVersion, &saved.runState, saved.OriginalStateEvidence); err != nil {
		return runState{}, errors.Join(ErrInvalidRequest, err)
	}
	return saved.runState, nil
}
