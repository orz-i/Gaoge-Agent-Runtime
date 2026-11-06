package openai_test

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"

// Test-only Host fixture: production OpenAI adapter contains no model catalog.
func fixtureOpenAIProfiles() model.StaticModelProfiles {
	withNone := []string{"none", "low", "medium", "high", "xhigh", "max"}
	withoutNone := []string{"low", "medium", "high", "xhigh", "max"}
	result := model.StaticModelProfiles{
		"gpt-test": {StructuredJSONSchema: true},
		"gpt-reasoning-fixture": {
			StructuredJSONSchema: true, EffortLevels: withNone, DisallowSamplingWhenEffortOmitted: true,
		},
	}
	for _, id := range []string{"gpt-4o", "gpt-4o-2024-08-06", "gpt-4o-2024-11-20",
		"gpt-4o-mini", "gpt-4o-mini-2024-07-18", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano",
		"gpt-5", "gpt-5-2025-08-07", "gpt-5-mini", "gpt-5-nano"} {
		result[id] = model.Capabilities{StructuredJSONSchema: true}
	}
	for _, id := range []string{"gpt-6-astra", "gpt-6.1-sol"} {
		result[id] = model.Capabilities{
			StructuredJSONSchema: true, EffortLevels: withoutNone, DisallowSamplingWhenEffortOmitted: true,
		}
	}
	for _, id := range []string{"gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6",
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		result[id] = model.Capabilities{
			StructuredJSONSchema: true, EffortLevels: withNone, DisallowSamplingWhenEffortOmitted: true,
		}
	}
	return result
}
