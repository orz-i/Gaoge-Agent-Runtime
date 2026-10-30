package anthropic

import (
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// inputContent preserves the order of user text and media blocks using the
// Messages API's provider-owned image/document source representations.
func inputContent(parts []model.ContentPart) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.ContentText:
			result = append(result, map[string]any{"type": "text", "text": part.Text})
		case model.ContentImage, model.ContentFile:
			source := map[string]any{}
			switch part.Source.Kind {
			case model.MediaSourceURL:
				source["type"], source["url"] = "url", part.Source.URL
			case model.MediaSourceFileID:
				source["type"], source["file_id"] = "file", part.Source.FileID
			default:
				return nil, ErrUnsupported
			}
			kind := "image"
			if part.Kind == model.ContentFile {
				kind = "document"
			}
			result = append(result, map[string]any{"type": kind, "source": source})
		case model.ContentAudio:
			return nil, errors.Join(ErrUnsupported, model.ErrCapabilityUnavailable)
		default:
			return nil, ErrUnsupported
		}
	}
	return result, nil
}
