package openai

import (
	"context"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Capabilities is deliberately per exact routed model, not provider-wide.
// The host may opt in newer/private/gateway model IDs after verifying support.
func (c *Client) Capabilities(_ context.Context, modelID string) (model.Capabilities, error) {
	if c == nil {
		return model.Capabilities{}, model.ErrCapabilityUnavailable
	}
	if modelID == "" {
		modelID = c.defaultModel
	}
	if strings.TrimSpace(modelID) != modelID || modelID == "" {
		return model.Capabilities{}, model.ErrCapabilityUnavailable
	}
	return model.Capabilities{StructuredJSONSchema: c.structuredOutputModels[modelID] || knownStructuredModel(modelID)}, nil
}

func knownStructuredModel(id string) bool {
	// Exact documented aliases and snapshots only. Prefix matches such as
	// gpt-5-private or gpt-4.1-unverified could otherwise opt in models whose
	// native JSON Schema behavior has never been established.
	switch id {
	case "gpt-4o", "gpt-4o-2024-08-06", "gpt-4o-2024-11-20",
		"gpt-4o-mini", "gpt-4o-mini-2024-07-18",
		"gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano",
		"gpt-5", "gpt-5-2025-08-07", "gpt-5-mini", "gpt-5-nano":
		return true
	}
	return false
}

var _ model.CapabilityProvider = (*Client)(nil)
