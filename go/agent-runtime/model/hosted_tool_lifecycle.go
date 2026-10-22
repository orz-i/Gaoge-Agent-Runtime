package model

import "strings"

// HostedToolTerminalStatus is the provider-neutral boundary for authoritative
// hosted Tool completion. Empty status is a legacy terminal receipt; unknown
// nonempty statuses are conservatively unfinished, never a zero-risk charge.
func HostedToolTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "completed", "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// CollapseHostedToolCalls observes each named call once per model response.
// A terminal fact outranks any earlier/later running observation, regardless
// of order. Preserve anonymous calls individually rather than inventing IDs.
// For contradictory terminal facts, retain the first authoritative terminal.
func CollapseHostedToolCalls(calls []HostedToolCall) []HostedToolCall {
	type identity struct{ toolKey, callID string }
	positions := make(map[identity]int, len(calls))
	result := make([]HostedToolCall, 0, len(calls))
	for _, call := range calls {
		if call.ID == "" {
			result = append(result, call)
			continue
		}
		id := identity{toolKey: call.ToolKey, callID: call.ID}
		if at, ok := positions[id]; ok {
			if !HostedToolTerminalStatus(result[at].Status) && HostedToolTerminalStatus(call.Status) {
				result[at] = call
			}
			continue
		}
		positions[id] = len(result)
		result = append(result, call)
	}
	return result
}
