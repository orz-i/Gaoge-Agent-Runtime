package providerhttp

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

// ValidateUniqueJSONKeys rejects ambiguous provider-authored JSON before any
// authority-bearing field is decoded. encoding/json accepts case-folded member
// aliases when decoding structs, as well as exact duplicate object members;
// both can overwrite the original owner/status/Tool identity.
// Input sizes are already bounded by ReadJSON and the adapters' SSE framing.
func ValidateUniqueJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := readUniqueJSONValue(decoder, 0); err != nil {
		return ErrProtocol
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrProtocol
	}
	return nil
}

// Bound structural nesting independently of input byte limits. A legitimate
// provider response does not require thousands of recursively nested objects.
const maxProviderJSONDepth = 256

func readUniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth >= maxProviderJSONDepth {
		return ErrProtocol
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrProtocol
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ErrProtocol
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrProtocol
			}
			// Go's struct field decoder matches folded names even if the
			// physical JSON keys are distinct (e.g. "Role" / "role").
			// Reject aliases at the same object level before unmarshalling.
			folded := foldJSONMemberName(key)
			if _, duplicate := seen[folded]; duplicate {
				return ErrProtocol
			}
			seen[folded] = struct{}{}
			if err := readUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return ErrProtocol
		}
	case '[':
		for decoder.More() {
			if err := readUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

// Use the same Unicode simple-fold equivalence as encoding/json's struct
// field matching, not just ASCII lowercasing. The smallest rune in each
// fold cycle is a stable canonical key, keeping validation linear in bytes.
func foldJSONMemberName(name string) string {
	var folded strings.Builder
	folded.Grow(len(name))
	for _, original := range name {
		canonical := original
		for next := unicode.SimpleFold(original); next != original; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		folded.WriteRune(canonical)
	}
	return folded.String()
}
