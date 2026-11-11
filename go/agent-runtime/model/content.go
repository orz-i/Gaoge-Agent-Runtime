package model

import (
	"encoding/hex"
	"errors"
	"mime"
	"net/url"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// ContentPartKind is the closed provider-neutral set of ordered input blocks.
// Local Tool calls and opaque ProviderReplay remain separate fields.
type ContentPartKind string

const (
	ContentText  ContentPartKind = "text"
	ContentImage ContentPartKind = "image"
	ContentFile  ContentPartKind = "file"
	ContentAudio ContentPartKind = "audio"
	ContentVideo ContentPartKind = "video"
)

// MediaSourceKind chooses one Host-authorized remote reference.
// Model transcripts never persist raw base64/media bytes or data URIs.
type MediaSourceKind string

const (
	MediaSourceURL    MediaSourceKind = "url"
	MediaSourceFileID MediaSourceKind = "file_id"
	// MediaSourceArtifact is an immutable Host-authorized object, resolved only by a Host adapter.
	MediaSourceArtifact MediaSourceKind = "artifact"
)

// MediaSource is one immutable remote media identity. A file ID is scoped to
// the configured Provider and must not be reused across Provider bindings.
type MediaSource struct {
	Kind       MediaSourceKind `json:"kind"`
	URL        string          `json:"url,omitempty"`
	FileID     string          `json:"fileID,omitempty"`
	ArtifactID string          `json:"artifactID,omitempty"`
	SHA256     string          `json:"sha256,omitempty"`
	SizeBytes  int64           `json:"sizeBytes,omitempty"`
}

// ContentPart is an ordered, tagged, strictly validated Message content block.
// Exactly one of Text or Source is set by Kind. Provider adapters translate
// each neutral block or reject it, never silently flatten media into text.
type ContentPart struct {
	Name         string          `json:"name,omitempty"`
	CacheControl *CacheControl   `json:"cacheControl,omitempty"`
	Kind         ContentPartKind `json:"kind"`
	Text         string          `json:"text,omitempty"`
	MediaType    string          `json:"mediaType,omitempty"`
	Source       *MediaSource    `json:"source,omitempty"`
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
	// Validate all JSON-bound fields, including media references, Tool call
	// metadata and opaque ProviderReplay identity, before normalization.
	if jsoncontract.ValidateCanonicalUTF8(message) != nil || len(message.Content) > 64 || !validCacheControl(message.CacheControl) {
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
	if part.MediaType != "" {
		parsed, params, err := mime.ParseMediaType(part.MediaType)
		if err != nil || parsed != part.MediaType || len(params) != 0 {
			return ErrInvalidContent
		}
	}
	if !validCacheControl(part.CacheControl) || len(part.Name) > 512 || strings.TrimSpace(part.Name) != part.Name || strings.ContainsAny(part.Name, "\x00\r\n") {
		return ErrInvalidContent
	}
	switch part.Kind {
	case ContentText:
		if part.Source != nil || part.MediaType != "" || part.Name != "" || strings.TrimSpace(part.Text) == "" {
			return ErrInvalidContent
		}
		return nil
	case ContentImage, ContentFile, ContentAudio, ContentVideo:
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
			if part.MediaType != "application/pdf" && (part.Source.Kind != MediaSourceArtifact || !hostFileMIME(part.MediaType)) {
				return ErrInvalidContent
			}
		case ContentAudio:
			if part.MediaType != "audio/mpeg" && part.MediaType != "audio/wav" && (part.Source.Kind != MediaSourceArtifact || !strings.HasPrefix(part.MediaType, "audio/")) {
				return ErrInvalidContent
			}
		case ContentVideo:
			if !hostVideoMIME(part.MediaType) {
				return ErrInvalidContent
			}
		}
		if part.Source.Kind != MediaSourceArtifact && (part.Source.ArtifactID != "" || part.Source.SHA256 != "" || part.Source.SizeBytes != 0) {
			return ErrInvalidContent
		}
		switch part.Source.Kind {
		case MediaSourceArtifact:
			digest, err := hex.DecodeString(part.Source.SHA256)
			if err != nil || len(digest) != 32 || strings.ToLower(part.Source.SHA256) != part.Source.SHA256 || part.Source.URL != "" || part.Source.FileID != "" || !validBindingValue(part.Source.ArtifactID, 512, false) || part.Source.SizeBytes <= 0 {
				return ErrInvalidContent
			}
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
		cloned[i].CacheControl = CloneCacheControl(part.CacheControl)
		if part.Source != nil {
			source := *part.Source
			cloned[i].Source = &source
		}
	}
	return cloned
}

// CacheControl is a provider-neutral prompt-cache breakpoint hint.
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func validCacheControl(value *CacheControl) bool {
	return value == nil || (value.Type == "ephemeral" && (value.TTL == "" || value.TTL == "5m" || value.TTL == "1h"))
}
func CloneCacheControl(value *CacheControl) *CacheControl {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func hostFileMIME(value string) bool {
	switch value {
	case "application/msword", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.ms-excel", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "text/csv", "text/plain", "text/markdown", "application/json":
		return true
	}
	return false
}
func hostVideoMIME(value string) bool {
	switch value {
	case "video/mp4", "video/x-msvideo", "video/avi", "video/quicktime", "video/mov", "video/x-matroska", "video/webm", "video/mpeg", "video/3gpp":
		return true
	}
	return false
}
