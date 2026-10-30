package anthropic

import (
	"context"
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
	return model.Capabilities{StructuredJSONSchema: c.structuredOutputModels[modelID] || knownStructuredModel(modelID)}, nil
}

func knownStructuredModel(id string) bool {
	// Models documented by Claude Platform as supporting output_config.format.
	for _, family := range []string{
		"claude-fable-5", "claude-fable-5-1",
		"claude-mythos-5", "claude-mythos-5-1", "claude-mythos-preview",
		"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5",
		"claude-sonnet-4-5", "claude-sonnet-4-6", "claude-sonnet-5", "claude-sonnet-5-5",
		"claude-haiku-4-5", "claude-haiku-5-5",
	} {
		if id == family {
			return true
		}
		if snapshot, ok := strings.CutPrefix(id, family+"-"); ok && len(snapshot) == 8 && strings.HasPrefix(snapshot, "20") {
			validDateSuffix := true
			for _, digit := range snapshot {
				if digit < '0' || digit > '9' {
					validDateSuffix = false
					break
				}
			}
			if validDateSuffix {
				return true
			}
		}
	}
	return false
}

var _ model.CapabilityProvider = (*Client)(nil)
