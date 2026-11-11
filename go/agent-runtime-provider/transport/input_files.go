package transport

import (
	"encoding/base64"
	"strings"
)

func nativeFileDataURL(part ContentPart) string {
	return "data:" + strings.ToLower(strings.TrimSpace(part.MimeType)) + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
}
