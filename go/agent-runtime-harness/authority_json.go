package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
)

// Persisted effect-authority JSON is a proof, not a user preference. Its
// meaning must not depend on "last duplicate key wins", implicit field
// coercion, unknown fields, object key ordering or whitespace. This is used
// by ALL four Harness Item proof families and their SQL/Memory adapters.
var errInvalidAuthorityJSON = errors.New("invalid or ambiguous authority JSON")

const maxAuthorityJSONDepth = 512

// decodeUniqueAuthorityJSON rejects duplicate keys at every nesting level,
// including keys which differ only in their JSON escaping. json.Unmarshal
// does NOT reject such keys. UseNumber also preserves exact numeric tokens
// when comparing an original Resolved Interaction to its immutable Item.
func decodeUniqueAuthorityJSON(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	parsed, err := parseUniqueAuthorityJSON(decoder, 0)
	if err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return parsed, true
}

func parseUniqueAuthorityJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth >= maxAuthorityJSONDepth {
		return nil, errInvalidAuthorityJSON
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delim {
	case '{':
		fields := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errInvalidAuthorityJSON
			}
			if _, duplicated := fields[key]; duplicated {
				return nil, errInvalidAuthorityJSON
			}
			value, err := parseUniqueAuthorityJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			fields[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errInvalidAuthorityJSON
		}
		return fields, nil
	case '[':
		var values []any
		for decoder.More() {
			value, err := parseUniqueAuthorityJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errInvalidAuthorityJSON
		}
		return values, nil
	default:
		return nil, errInvalidAuthorityJSON
	}
}

// authorityStringFields accepts exactly the named keys, each a JSON string.
// It rejects unknown, missing or duplicate keys. Production emitters use
// fixed struct payloads, so this is an intentional pre-v1 hard-cut.
func authorityStringFields(raw json.RawMessage, names ...string) (map[string]string, bool) {
	value, ok := decodeUniqueAuthorityJSON(raw)
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != len(names) {
		return nil, false
	}
	fields := make(map[string]string, len(names))
	for _, name := range names {
		entry, found := object[name]
		if !found {
			return nil, false
		}
		text, ok := entry.(string)
		if !ok {
			return nil, false
		}
		fields[name] = text
	}
	return fields, true
}

// decodeTypedAuthorityJSON accepts exactly one unambiguous first-party
// typed proof: duplicate JSON keys (including nested keys), unknown fields,
// case-aliases and non-canonical values cannot change the decoded authority.
//
// The decoded struct must round-trip to the same JSON value with canonical
// field names. This rejects e.g. "Status" and "status" as separate keys
// that encoding/json would otherwise map onto the same Go struct field.
func decodeTypedAuthorityJSON(raw json.RawMessage, value any) bool {
	original, ok := decodeUniqueAuthorityJSON(raw)
	if !ok {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return false
	}
	normalized, ok := decodeUniqueAuthorityJSON(canonical)
	return ok && reflect.DeepEqual(original, normalized)
}

func sameUniqueAuthorityJSON(left, right json.RawMessage) bool {
	l, lOK := decodeUniqueAuthorityJSON(left)
	r, rOK := decodeUniqueAuthorityJSON(right)
	return lOK && rOK && reflect.DeepEqual(l, r)
}
