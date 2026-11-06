package anthropic_test

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"

// Model capability facts are supplied by the Host in tests, not hardcoded in
// the SDK's Anthropic Messages adapter.
func fixtureAnthropicProfiles() model.StaticModelProfiles {
	all := []string{"low", "medium", "high", "xhigh", "max"}
	withoutXHigh := []string{"low", "medium", "high", "max"}
	result := model.StaticModelProfiles{
		"claude-test":    {StructuredJSONSchema: true},
		"claude-private": {StructuredJSONSchema: true},
	}
	for _, id := range []string{"claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5",
		"claude-opus-5-5-20261001"} {
		result[id] = model.Capabilities{StructuredJSONSchema: true}
	}
	for _, id := range []string{"claude-fable-5-1", "claude-mythos-5-1",
		"claude-fable-5", "claude-mythos-5",
		"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7",
		"claude-sonnet-5-5", "claude-sonnet-5", "claude-haiku-5-5"} {
		result[id] = model.Capabilities{
			StructuredJSONSchema: true, EffortLevels: all, DisallowSamplingWhenEffortOmitted: true,
		}
	}
	for _, id := range []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-mythos-preview"} {
		result[id] = model.Capabilities{
			StructuredJSONSchema: true, EffortLevels: withoutXHigh, DisallowSamplingWhenEffortOmitted: true,
		}
	}
	// The Host, not the SDK, supplies unsupported forced-Tool mode for
	// exactly the selected models (including a dated snapshot).
	for _, id := range []string{"claude-opus-5-5", "claude-opus-5-5-20261001",
		"claude-sonnet-5-5", "claude-fable-5-1", "claude-mythos-5-1"} {
		profile := result[id]
		profile.DisallowForcedToolChoice = true
		result[id] = profile
	}
	return result
}
