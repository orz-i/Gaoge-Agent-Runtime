package openai

import (
	"context"
	"slices"
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
	profile := c.profiles[modelID]
	profile.EffortLevels = slices.Clone(profile.EffortLevels)
	return profile, nil
}

var _ model.CapabilityProvider = (*Client)(nil)
