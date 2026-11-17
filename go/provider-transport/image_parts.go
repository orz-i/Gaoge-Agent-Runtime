package transport

import "fmt"

// rejectUnsupportedImageMask fails even for an empty, explicitly supplied mask;
// dropping it would silently change the requested edit. Keep the existing
// unsupported-adapter classification across the gateway boundary.
func rejectUnsupportedImageMask(mask *ContentPart, adapter string) error {
	if mask == nil {
		return nil
	}
	return fmt.Errorf("%w: %w: %s", ErrUnsupportedAdapter, ErrUnsupportedImageMask, adapter)
}

// collectImageInputParts 收集消息中可发送给图片编辑类协议的原始图片输入。
func collectImageInputParts(messages []Message) []ContentPart {
	images := make([]ContentPart, 0)
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Kind != ContentPartImage || len(part.Data) == 0 {
				continue
			}
			images = append(images, part)
		}
	}
	return images
}
