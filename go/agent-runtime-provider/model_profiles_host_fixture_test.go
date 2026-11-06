package provider_test

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// These are TEST HOST certifications for loopback simulated Provider routes.
// The Runtime and shipping adapters have no public model-ID registry.
// Adversarial Agent+Provider tests need a genuine *positive* mandatory-Tool
// grant to reach paid/rejected Tool receipt and physical SSE validation.
func testHostModelProfiles() model.StaticModelProfiles {
	ids := []string{
		"audit-model", "gpt-audit", "gpt-test", "gpt-4.1", "fixture-model",
		"gpt-fixture", "unused", "structured-model",
		"claude-test", "claude-audit", "claude-opus-5-5",
		"anthropic/claude-opus-5-5", "other-model", "test-model",
	}
	profiles := make(model.StaticModelProfiles, len(ids))
	for _, id := range ids {
		profiles[id] = model.Capabilities{
			SupportsForcedToolChoice:          true,
			SupportsSamplingWhenEffortOmitted: true,
			StructuredJSONSchema:              true,
		}
	}
	return profiles
}

func newAuditedOpenAIClient(config openai.Config) (*openai.Client, error) {
	if config.ModelProfiles == nil {
		config.ModelProfiles = testHostModelProfiles()
	}
	return openai.New(config)
}

func newAuditedAnthropicClient(config anthropic.Config) (*anthropic.Client, error) {
	if config.ModelProfiles == nil {
		config.ModelProfiles = testHostModelProfiles()
	}
	return anthropic.New(config)
}
