package anthropic

// Effort support is explicitly per model ID. A proxy alias cannot inherit
// capability from a family name. Per-model xhigh availability differs from
// max availability (e.g. Sonnet 4.6, Opus 4.6 support max but not xhigh).
// See Anthropic's current "Effort" reference for per-model support.
func (c *Client) effortLevels(id string) []string {
	if levels, ok := c.effortModelLevels[id]; ok {
		return append([]string(nil), levels...)
	}
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
