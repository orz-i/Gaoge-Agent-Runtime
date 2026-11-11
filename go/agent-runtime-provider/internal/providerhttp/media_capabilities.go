package providerhttp

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"

// RequireMediaCapabilities validates only model-specific advanced input
// permissions. Message structure, URL/FileID authorization and provider wire
// encoding remain owned by their existing validators and the Host.
// Unknown/unlisted models have an empty profile and cannot dispatch media.
func RequireMediaCapabilities(messages []model.Message, profile model.Capabilities) error {
	for _, message := range messages {
		for _, part := range message.Content {
			switch part.Kind {
			case model.ContentText:
				// Ordinary text requires no model capability certification.
			case model.ContentImage:
				if !profile.SupportsImageInput {
					return model.ErrCapabilityUnavailable
				}
			case model.ContentFile:
				if !profile.SupportsPDFInput {
					return model.ErrCapabilityUnavailable
				}
			case model.ContentAudio, model.ContentVideo:
				return model.ErrCapabilityUnavailable
			default:
				// Future unknown media kinds must not inherit a text grant.
				return model.ErrCapabilityUnavailable
			}
		}
	}
	return nil
}
