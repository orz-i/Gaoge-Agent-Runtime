package budget

// HasRunTokenLimits reports whether the original Turn or any ancestor requires metering.
func (ledger Ledger) HasRunTokenLimits(runID string) bool {
	if HasTokenLimits(ledger.Limits) {
		return true
	}
	for runID != "" {
		binding := ledger.Runs[runID]
		if HasTokenLimits(binding.Limits) {
			return true
		}
		runID = binding.ParentRunID
	}
	return false
}

// RemainingHostedCalls returns the new native execution allowance across all ancestor limits.
func (ledger Ledger) RemainingHostedCalls(runID string) (int, bool) {
	maximum, limited := int(^uint(0)>>1), false
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		if view.Limits.MaxHostedToolCalls > 0 {
			limited = true
			maximum = min(maximum, max(0, view.Limits.MaxHostedToolCalls-view.Usage.HostedToolCalls-view.Reserved.HostedToolCalls))
		}
		if view.Limits.MaxToolCalls > 0 {
			// The shared Tool ceiling also covers provider-hosted executions.
			// A deployment need not configure a separate hosted-only limit.
			limited = true
			maximum = min(maximum, max(0, view.Limits.MaxToolCalls-view.Usage.ToolCalls-view.Reserved.ToolCalls))
		}
		if current == "" {
			return maximum, limited
		}
	}
}
