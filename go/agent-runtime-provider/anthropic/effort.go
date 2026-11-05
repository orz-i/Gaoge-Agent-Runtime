package anthropic

// Only exact documented IDs are certified. These models use adaptive
// thinking; their supported effort levels differ (e.g. Sonnet 4.6 accepts
// max but not xhigh).
func knownEffortLevels(id string) []string {
	switch id {
	case "claude-fable-5-1", "claude-mythos-5-1",
		"claude-fable-5", "claude-mythos-5",
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8",
		"claude-opus-4-7",
		"claude-sonnet-5-5", "claude-sonnet-5", "claude-haiku-5-5":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "claude-opus-4-6", "claude-sonnet-4-6":
		return []string{"low", "medium", "high", "max"}
	default:
		return nil
	}
}

func (c *Client) effortLevels(id string) []string {
	if levels, ok := c.effortModelLevels[id]; ok {
		return append([]string(nil), levels...)
	}
	return knownEffortLevels(id)
}

// Anthropic documents nondefault sampling as unsupported on these adaptive
// models even when output_config.effort is omitted. Other models are handled
// by the explicit effort check in the adapter; do not infer by prefix.
func knownNoSampling(id string) bool {
	switch id {
	case "claude-fable-5-1", "claude-mythos-5-1",
		"claude-fable-5", "claude-mythos-5", "claude-mythos-preview",
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7",
		"claude-sonnet-5-5", "claude-sonnet-5", "claude-haiku-5-5":
		return true
	default:
		return false
	}
}
