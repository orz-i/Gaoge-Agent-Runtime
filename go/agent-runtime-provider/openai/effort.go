package openai

// Exact IDs: no family-prefix inference for third-party routes. The
// provider's published reasoning guidance excludes sampling parameters when
// effort is non-none; the documented default for these models is non-none.
func knownEffortLevels(id string) []string {
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

// Host certification is reserved for otherwise unknown private model IDs;
// it cannot re-enable an unsupported level on a documented public model.
func (c *Client) effortLevels(id string) []string {
	if levels, ok := c.effortModelLevels[id]; ok {
		return append([]string(nil), levels...)
	}
	return knownEffortLevels(id)
}
