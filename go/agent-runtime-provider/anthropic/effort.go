package anthropic

// Effort support is explicitly per model ID; deprecated 4.5 models and
// unverified gateway names cannot inherit the capability by prefix.
func (c *Client) effortLevels(id string) []string {
	if levels, ok := c.effortModelLevels[id]; ok {
		return append([]string(nil), levels...)
	}
	switch id {
	case "claude-fable-5-1", "claude-mythos-5-1",
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8",
		"claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-4-6",
		"claude-haiku-5-5":
		return []string{"low", "medium", "high", "xhigh", "max"}
	default:
		return nil
	}
}
