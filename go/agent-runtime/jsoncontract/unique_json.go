package jsoncontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
)

// ErrAmbiguousJSON means a JSON input contains invalid, duplicate or
// case-fold-equivalent member names that cannot be interpreted uniquely.
// This is a trust-boundary rejection, not an untrusted body diagnostic.
var ErrAmbiguousJSON = errors.New("invalid or ambiguous JSON")

// MaxUniqueJSONDepth is a structural guard shared with Provider decoding.
const MaxUniqueJSONDepth = 256

// ValidateUniqueJSONKeys checks a complete JSON value before unmarshalling
// into Go maps or structs. encoding/json silently replaces duplicate members
// and matches struct fields case-insensitively; neither behavior is acceptable
// for provider effect identities or frozen ModelOptions.
func ValidateUniqueJSONKeys(data []byte) error {
	if ValidateCanonicalJSONUnicode(data) != nil {
		return ErrAmbiguousJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := readUniqueJSONValue(decoder, 0); err != nil {
		return ErrAmbiguousJSON
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrAmbiguousJSON
	}
	return nil
}

// ValidateUniqueJSONObject additionally requires a non-null JSON object.
// This preserves the original physical Tool argument contract and supplies
// the same lossless pre-commit ModelOptions validation to Harness.
func ValidateUniqueJSONObject(data []byte) error {
	if err := ValidateUniqueJSONKeys(data); err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return ErrAmbiguousJSON
	}
	return nil
}

func readUniqueJSONValue(decoder *json.Decoder, depth int) error {
	if depth >= MaxUniqueJSONDepth {
		return ErrAmbiguousJSON
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrAmbiguousJSON
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
				return ErrAmbiguousJSON
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrAmbiguousJSON
			}
			folded := foldJSONMemberName(key)
			if _, duplicate := seen[folded]; duplicate {
				return ErrAmbiguousJSON
			}
			seen[folded] = struct{}{}
			if err := readUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return ErrAmbiguousJSON
		}
	case '[':
		for decoder.More() {
			if err := readUniqueJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return ErrAmbiguousJSON
		}
	default:
		return ErrAmbiguousJSON
	}
	return nil
}

// Mirror encoding/json's Unicode simple-fold field matching so e.g. "Role"
// and "role" never compete for one physical authorization field.
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
