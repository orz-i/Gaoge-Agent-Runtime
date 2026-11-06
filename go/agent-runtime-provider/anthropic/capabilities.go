package anthropic

import (
	"context"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Capabilities checks the selected model, not the provider in general.
// Unknown model IDs must be explicitly enabled by the host.
func (c *Client) Capabilities(_ context.Context, modelID string) (model.Capabilities, error) {
	if c == nil {
		return model.Capabilities{}, model.ErrCapabilityUnavailable
	}
	if modelID == "" {
		modelID = c.defaultModel
	}
	if modelID == "" || strings.TrimSpace(modelID) != modelID {
		return model.Capabilities{}, model.ErrCapabilityUnavailable
	}
	profile := c.profiles[modelID]
	profile.EffortLevels = slices.Clone(profile.EffortLevels)
	return profile, nil
}

var _ model.CapabilityProvider = (*Client)(nil)
