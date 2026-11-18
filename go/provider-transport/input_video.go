package transport

import (
	"encoding/base64"
	"strings"
)

const nativeVideoURLKey = "video_url"
const nativeVideoSourceKey = "source"
const nativeMediaTypeKey = "media_type"

func nativeVideoMIME(part ContentPart) string {
	mime := strings.ToLower(strings.TrimSpace(part.MimeType))
	if mime == InputVideoMIMEQuickTime {
		// MiniMax requires video/mov for inline MOV, while object storage uses quicktime.
		return InputVideoMIMEMOV
	}
	return mime
}

func nativeVideoDataURL(part ContentPart) string {
	return "data:" + nativeVideoMIME(part) + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
}
