package modelcap

import "testing"

func TestDefaultRegistryReportsInferredProvenance(t *testing.T) {
	resolution := Default.Resolve("gpt-4.1-mini", "")
	if resolution.ContextWindow != 1_047_576 || resolution.ContextWindowSource != SourceInferred || resolution.MatchedRule != "openai.gpt-4.1" {
		t.Fatalf("inferred resolution = %#v", resolution)
	}
}

func TestResolveStructuredOutputRequiresExplicitCapability(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		mode   StructuredOutputMode
		status ConfigurationStatus
	}{
		{name: "absent", raw: `{}`, mode: StructuredOutputUnsupported, status: ConfigurationAbsent},
		{name: "strict", raw: `{"structuredOutput":{"mode":"strict_json_schema"}}`, mode: StructuredOutputStrictJSONSchema, status: ConfigurationValid},
		{name: "json object", raw: `{"structuredOutputMode":"json_object"}`, mode: StructuredOutputJSONObject, status: ConfigurationValid},
		{name: "json text", raw: `{"structuredOutput":"json_text"}`, mode: StructuredOutputJSONText, status: ConfigurationValid},
		{name: "canonical wins over alias", raw: `{"structuredOutput":{"mode":"json_text"},"structuredOutputMode":"json_object"}`, mode: StructuredOutputJSONText, status: ConfigurationValid},
		{name: "explicit unsupported", raw: `{"structuredOutput":{"mode":"unsupported"}}`, mode: StructuredOutputUnsupported, status: ConfigurationValid},
		{name: "invalid mode", raw: `{"structuredOutput":{"mode":"yaml"}}`, mode: StructuredOutputUnsupported, status: ConfigurationInvalid},
		{name: "invalid json", raw: `{`, mode: StructuredOutputUnsupported, status: ConfigurationInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveStructuredOutput(test.raw)
			if got.Mode != test.mode || got.ConfigurationStatus != test.status {
				t.Fatalf("resolution = %+v", got)
			}
		})
	}
}

func TestDefaultRegistryReportsConfiguredProvenance(t *testing.T) {
	configured := Default.Resolve("gpt-4.1-mini", `{"context_window_tokens":"64000","maxOutputTokens":12000}`)
	if configured.ContextWindow != 64_000 || configured.MaxOutputTokens != 12_000 {
		t.Fatalf("configured limits = %#v", configured)
	}
	if configured.ContextWindowSource != SourceConfigured || configured.MaxOutputTokensSource != SourceConfigured || configured.ConfigurationStatus != ConfigurationValid {
		t.Fatalf("configured provenance = %#v", configured)
	}
}

func TestDefaultRegistryReportsDefaultProvenance(t *testing.T) {
	unknown := Default.Resolve("custom-enterprise-model", "")
	if unknown.ContextWindowSource != SourceDefault || unknown.MaxOutputTokensSource != SourceDefault || unknown.MatchedRule != "" {
		t.Fatalf("default provenance = %#v", unknown)
	}
}

func TestDefaultRegistryMarksMalformedConfigurationWithoutDiscardingFallback(t *testing.T) {
	resolution := Default.Resolve("claude-3.5-sonnet", `{`)
	if resolution.ConfigurationStatus != ConfigurationInvalid {
		t.Fatalf("configuration status = %s", resolution.ConfigurationStatus)
	}
	if resolution.ContextWindow != 200_000 || resolution.ContextWindowSource != SourceInferred {
		t.Fatalf("fallback resolution = %#v", resolution)
	}
}

func TestEffectiveContextBudgetUsesResolvedLimits(t *testing.T) {
	resolution := Default.Resolve("custom-enterprise-model", `{"contextWindow":64000,"maxOutputTokens":12000}`)
	if got := resolution.EffectiveContextBudget(); got != 39_000 {
		t.Fatalf("effective context budget = %d", got)
	}
}
