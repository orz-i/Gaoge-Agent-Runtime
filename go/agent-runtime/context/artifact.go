package context

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// ArtifactEncodingLosslessJSON identifies archives that cannot be encoded with
// the historical float64 codec without changing their JSON number literals.
// The empty encoding retains the old on-disk identity.
const ArtifactEncodingLosslessJSON = "lossless-json-v1"

// Artifact seals durable content removed from the inline active window.
type Artifact struct {
	ID            string          `json:"id"`
	Kind          ArtifactKind    `json:"kind"`
	ScopeID       string          `json:"scopeID"`
	Generation    int             `json:"generation"`
	SourceID      string          `json:"sourceID,omitempty"`
	Content       string          `json:"content,omitempty"`
	ContentJSON   json.RawMessage `json:"contentJSON,omitempty"`
	ContentHash   string          `json:"contentHash"`
	TokenEstimate int64           `json:"tokenEstimate,omitempty"`
	Encoding      string          `json:"encoding,omitempty"`
}

// NewArtifact seals exact text or canonical JSON with the original number
// literals. Compatible JSON retains the historical identity, including for
// already admitted summary recipes. Ambiguous JSON is never normalized away.
func NewArtifact(kind ArtifactKind, scopeID string, generation int, sourceID string, content string, contentJSON json.RawMessage) (Artifact, error) {
	artifact := Artifact{Kind: kind, ScopeID: scopeID, Generation: generation, SourceID: sourceID, Content: content, ContentJSON: contentJSON}
	if err := normalizeArtifact(&artifact, "", 0, true); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func ValidArtifact(value Artifact) bool {
	checked := value
	expectedID := strings.TrimSpace(value.ID)
	expectedHash := strings.TrimSpace(value.ContentHash)
	return normalizeArtifact(&checked, checked.ScopeID, checked.Generation, false) == nil &&
		expectedID != "" && expectedHash != "" && checked.ID == expectedID && checked.ContentHash == expectedHash && value.TokenEstimate == checked.TokenEstimate
}

func normalizeArtifact(value *Artifact, scopeID string, generation int, mint bool) error {
	if value == nil {
		return ErrInvalidInput
	}
	value.ScopeID = strings.TrimSpace(value.ScopeID)
	value.SourceID = strings.TrimSpace(value.SourceID)
	if value.ScopeID == "" {
		value.ScopeID = strings.TrimSpace(scopeID)
	}
	if value.Generation <= 0 {
		value.Generation = generation
	}
	if value.ScopeID == "" || value.Generation <= 0 || (value.Kind != ArtifactCompaction && value.Kind != ArtifactToolResult) ||
		!utf8.ValidString(value.Content) {
		return ErrInvalidInput
	}
	if err := checkArtifactPayloadSize(*value); err != nil {
		return err
	}
	if err := normalizeArtifactJSON(value, mint); err != nil {
		return err
	}
	if err := checkArtifactPayloadSize(*value); err != nil {
		return err
	}
	// These are the legacy sorted envelope keys, with an additional domain
	// tag for lossless JSON. Never pass the new payload through float64.
	payload, err := json.Marshal(struct {
		Content     string          `json:"content,omitempty"`
		ContentJSON json.RawMessage `json:"contentJSON,omitempty"`
		Encoding    string          `json:"encoding,omitempty"`
	}{value.Content, value.ContentJSON, value.Encoding})
	if err != nil {
		return ErrInvalidInput
	}
	value.ContentHash = hashBytes(payload)
	value.TokenEstimate = estimatedTokens(payload)
	value.ID = stableID("ctxa", value.ScopeID, strconv.Itoa(value.Generation), string(value.Kind), value.SourceID, value.ContentHash)
	return nil
}

func checkArtifactPayloadSize(value Artifact) error {
	if len(value.Content) > MaxArtifactPayloadBytes || len(value.ContentJSON) > MaxArtifactPayloadBytes-len(value.Content) {
		return &ArtifactPayloadLimitError{
			PayloadBytes: int64(len(value.Content)) + int64(len(value.ContentJSON)), LimitBytes: MaxArtifactPayloadBytes,
		}
	}
	return nil
}

func normalizeArtifactJSON(value *Artifact, mint bool) error {
	raw := value.ContentJSON
	if len(raw) == 0 {
		if value.Encoding != "" {
			return ErrInvalidInput
		}
		value.ContentJSON = nil
		return nil
	}
	if mint {
		exact, err := canonicalArchiveJSON(raw)
		if err != nil {
			return err
		}
		value.ContentJSON = exact
		if legacy, err := canonicalJSON(json.RawMessage(exact)); err != nil || !bytes.Equal(legacy, exact) { //nolint:unconvert // Preserve RawMessage JSON semantics through the interface.
			value.Encoding = ArtifactEncodingLosslessJSON
		}
		return nil
	}
	var canonical []byte
	var err error
	switch value.Encoding {
	case "":
		canonical, err = canonicalJSON(raw)
	case ArtifactEncodingLosslessJSON:
		canonical, err = canonicalArchiveJSON(raw)
		if err != nil {
			return ErrInvalidInput
		}
		legacy, legacyErr := canonicalJSON(raw)
		if legacyErr == nil && bytes.Equal(legacy, raw) {
			return ErrInvalidInput // Do not mint a second identity for legacy-safe content.
		}
	default:
		return ErrInvalidInput
	}
	// Stored artifacts must already be canonical. In particular an old
	// digest for 9007199254740992 cannot authenticate substituted .993 bytes.
	if err != nil || !bytes.Equal(canonical, raw) {
		return ErrInvalidInput
	}
	return nil
}

func canonicalArchiveJSON(raw []byte) ([]byte, error) {
	if jsoncontract.ValidateUniqueJSONKeys(raw) != nil {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalidInput
	}
	return json.Marshal(value)
}
