package openai

// Every listed model is an exact, documented API ID. An unknown model
// (including a similarly-prefixed gateway alias) has no implicit capability.
// The Host can certify a private model using Config.EffortModelLevels.
func (c *Client) effortLevels(id string) []string {
	if levels, ok := c.effortModelLevels[id]; ok {
		return append([]string(nil), levels...)
	}
	switch id {
	case "gpt-6-astra", "gpt-6.1-sol":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "gpt-6-sol", "gpt-6-luna",
		"gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return []string{"none", "low", "medium", "high", "xhigh", "max"}
	default:
		return nil
	}
}
