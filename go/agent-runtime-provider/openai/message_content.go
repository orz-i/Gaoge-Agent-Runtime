package openai

import (
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// inputContent converts the ordered normalized user blocks into genuine
// Responses input content items. Unsupported media fails BEFORE transport;
// no image, file, or audio evidence may be stringified or dropped.
func inputContent(parts []model.ContentPart) (any, error) {
	if len(parts) == 1 && parts[0].Kind == model.ContentText {
		return parts[0].Text, nil
	}
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case model.ContentText:
			result = append(result, map[string]any{"type": "input_text", "text": part.Text})
		case model.ContentImage:
			block := map[string]any{"type": "input_image"}
			switch part.Source.Kind {
			case model.MediaSourceURL:
				block["image_url"] = part.Source.URL
			case model.MediaSourceFileID:
				block["file_id"] = part.Source.FileID
			default:
				return nil, ErrUnsupported
			}
			result = append(result, block)
		case model.ContentFile:
			block := map[string]any{"type": "input_file"}
			switch part.Source.Kind {
			case model.MediaSourceURL:
				block["file_url"] = part.Source.URL
			case model.MediaSourceFileID:
				block["file_id"] = part.Source.FileID
			default:
				return nil, ErrUnsupported
			}
			result = append(result, block)
		case model.ContentAudio:
			// The Responses input-audio request requires embedded bytes;
			// Model Message intentionally refuses inline binary payloads.
			// No implicit URL download, base64 storage or text fallback.
			return nil, errors.Join(ErrUnsupported, model.ErrCapabilityUnavailable)
		default:
			return nil, ErrUnsupported
		}
	}
	return result, nil
}
