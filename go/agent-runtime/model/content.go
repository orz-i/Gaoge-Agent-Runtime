package model

import (
	"errors"
	"net/url"
	"strings"
)

// ContentPartKind is the closed provider-neutral set of ordered input blocks.
// Local Tool calls and opaque ProviderReplay remain separate fields.
type ContentPartKind string

const (
	ContentText  ContentPartKind = "text"
	ContentImage ContentPartKind = "image"
	ContentFile  ContentPartKind = "file"
	ContentAudio ContentPartKind = "audio"
)

// MediaSourceKind chooses one Host-authorized remote reference.
// Model transcripts never persist raw base64/media bytes or data URIs.
type MediaSourceKind string

const (
	MediaSourceURL    MediaSourceKind = "url"
	MediaSourceFileID MediaSourceKind = "file_id"
)

// MediaSource is one immutable remote media identity. A file ID is scoped to
// the configured Provider and must not be reused across Provider bindings.
type MediaSource struct {
	Kind   MediaSourceKind `json:"kind"`
	URL    string          `json:"url,omitempty"`
	FileID string          `json:"fileID,omitempty"`
}

// ContentPart is an ordered, tagged, strictly validated Message content block.
// Exactly one of Text or Source is set by Kind. Provider adapters translate
// each neutral block or reject it, never silently flatten media into text.
type ContentPart struct {
	Kind      ContentPartKind `json:"kind"`
	Text      string          `json:"text,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Source    *MediaSource    `json:"source,omitempty"`
}

var ErrInvalidContent = errors.New("invalid model message content")

// TextParts constructs the canonical text-message representation.
// Empty text has no block (e.g. Assistant with Tool calls/replay only).
func TextParts(text string) []ContentPart {
	if text == "" {
		return nil
	}
	return []ContentPart{{Kind: ContentText, Text: text}}
}

// PlainText requires ALL blocks to be text. It never silently flattens
// image/audio/file evidence to text or drops it.
func PlainText(parts []ContentPart) (string, bool) {
	var text strings.Builder
	for _, part := range parts {
		if part.Kind != ContentText || part.Source != nil || part.MediaType != "" {
			return "", false
		}
		text.WriteString(part.Text)
	}
	return text.String(), true
}

// TextValue is only a non-authoritative text projection for internal
// heuristics that already operate on text-only RoleTool/RoleAssistant
// messages. Media content returns empty; providers must use PlainText's
// explicit boolean and must never call TextValue to serialize a request.
func TextValue(parts []ContentPart) string {
	text, _ := PlainText(parts)
	return text
}

// ValidateMessage is the single normalized role/content authority.
func ValidateMessage(message Message) error {
	if len(message.Content) > 64 {
		return ErrInvalidContent
	}
	var nonempty bool
	for _, part := range message.Content {
		if err := validatePart(part); err != nil {
			return err
		}
		if part.Kind == ContentText {
			nonempty = nonempty || strings.TrimSpace(part.Text) != ""
		} else {
			nonempty = true
			if message.Role != RoleUser {
				return ErrInvalidContent
			}
		}
	}
	switch message.Role {
	case RoleSystem, RoleUser:
		if !nonempty || len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ProviderReplay != nil {
			return ErrInvalidContent
		}
	case RoleAssistant:
		if message.ToolCallID != "" {
			return ErrInvalidContent
		}
		// May contain only ToolCalls or ProviderReplay, with no visible text.
	case RoleTool:
		if message.ToolCallID == "" || len(message.ToolCalls) != 0 || message.ProviderReplay != nil || !nonempty {
			return ErrInvalidContent
		}
	default:
		return ErrInvalidContent
	}
	return nil
}

func validatePart(part ContentPart) error {
	switch part.Kind {
	case ContentText:
		if part.Source != nil || part.MediaType != "" || strings.TrimSpace(part.Text) == "" {
			return ErrInvalidContent
		}
		return nil
	case ContentImage, ContentFile, ContentAudio:
		if part.Text != "" || part.Source == nil ||
			strings.TrimSpace(part.MediaType) != part.MediaType || part.MediaType == "" ||
			len(part.MediaType) > 128 {
			return ErrInvalidContent
		}
		switch part.Kind {
		case ContentText:
			return ErrInvalidContent // Guarded by the outer union switch.
		case ContentImage:
			switch part.MediaType {
			case "image/png", "image/jpeg", "image/webp", "image/gif":
			default:
				return ErrInvalidContent
			}
		case ContentFile:
			if part.MediaType != "application/pdf" {
				return ErrInvalidContent
			}
		case ContentAudio:
			if part.MediaType != "audio/mpeg" && part.MediaType != "audio/wav" {
				return ErrInvalidContent
			}
		}
		switch part.Source.Kind {
		case MediaSourceFileID:
			if part.Source.URL != "" || strings.TrimSpace(part.Source.FileID) != part.Source.FileID ||
				part.Source.FileID == "" || len(part.Source.FileID) > 512 {
				return ErrInvalidContent
			}
		case MediaSourceURL:
			if part.Source.FileID != "" || len(part.Source.URL) > 4096 ||
				strings.TrimSpace(part.Source.URL) != part.Source.URL {
				return ErrInvalidContent
			}
			address, err := url.Parse(part.Source.URL)
			if err != nil || address.Scheme != "https" || address.Host == "" ||
				address.User != nil || address.Fragment != "" || address.Opaque != "" {
				return ErrInvalidContent
			}
		default:
			return ErrInvalidContent
		}
		return nil
	default:
		return ErrInvalidContent
	}
}

// CloneContentParts isolates the slice and mutable media source pointers.
func CloneContentParts(parts []ContentPart) []ContentPart {
	if parts == nil {
		return nil
	}
	cloned := make([]ContentPart, len(parts))
	for i, part := range parts {
		cloned[i] = part
		if part.Source != nil {
			source := *part.Source
			cloned[i].Source = &source
		}
	}
	return cloned
}
