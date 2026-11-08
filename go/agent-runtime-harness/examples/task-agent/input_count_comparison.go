package main

import (
	"crypto/sha256"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// This key includes every Provider-input-affecting request field (model,
// ModelOptions, OutputContract, Messages, Tools and Tool choice) while
// excluding host execution identity and output-only ceilings. It is an
// ephemeral Host correlation key, not a Provider binding fingerprint and
// never a security or billing authorization.
func inputCountObservationFingerprint(request model.Request) ([32]byte, error) {
	candidate := model.CloneRequest(request)
	candidate.RunID = ""
	candidate.InvocationID = ""
	candidate.MaxOutputTokens = 0
	candidate.HostedToolCallLimit = nil
	raw, err := model.MarshalRequestForInputCount(candidate)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(raw), nil
}

// A positive native count is kept only in memory and indexed by this input
// key. No request bytes, fingerprint, signed replay or Tool arguments leave
// the Host in the report. The comparison never influences Context admission.
func (state *taskState) recordInputCountUsageComparison(request model.Request, usage *tokenUsage, modelErr error) {
	if state == nil || state.suite != qualitySuite {
		return
	}
	fingerprint, err := inputCountObservationFingerprint(request)
	if err != nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	accepted, exists := state.acceptedInputCounts[fingerprint]
	// A positive count is evidence for AT MOST one subsequent physical
	// model invocation. Consume it even when that invocation failed or
	// produced no usable Usage receipt; otherwise a repeated identical
	// request could inherit stale count proof and inflate coverage.
	delete(state.acceptedInputCounts, fingerprint)
	if !exists || accepted <= 0 || modelErr != nil ||
		usage == nil || usage.InputTokens <= 0 {
		return
	}
	comparison := &state.inputCountComparison
	comparison.Compared++
	switch {
	case accepted == usage.InputTokens:
		comparison.Equal++
	case accepted < usage.InputTokens:
		comparison.BelowUsage++
		delta := usage.InputTokens - accepted
		if delta > comparison.MaxUnderestimate {
			comparison.MaxUnderestimate = delta
		}
	default:
		comparison.AboveUsage++
	}
}
