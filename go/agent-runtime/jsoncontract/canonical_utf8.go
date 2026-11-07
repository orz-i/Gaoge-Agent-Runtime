package jsoncontract

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrNonCanonicalUTF8 is a fail-closed boundary error for Go strings and raw
// JSON that would be silently rewritten by encoding/json before hashing,
// persistence, replay authorization or provider dispatch.
var ErrNonCanonicalUTF8 = errors.New("noncanonical UTF-8 in JSON-bound data")

var rawMessageType = reflect.TypeFor[json.RawMessage]()

// ValidateCanonicalUTF8 checks the original typed values BEFORE json.Marshal.
// encoding/json silently maps all malformed UTF-8 strings to U+FFFD, making
// physically different inputs share a hash. This check walks exported
// JSON-bound fields, nested slices, maps (including keys) and interfaces.
// Opaque []byte payloads are not text; json.RawMessage has a separate strict
// JSON Unicode contract. Unsupported/cyclic structures must be rejected by
// their actual serialization boundary, not converted to a string here.
func ValidateCanonicalUTF8(value any) error {
	if err := validateUTF8Value(reflect.ValueOf(value), 0); err != nil {
		return ErrNonCanonicalUTF8
	}
	return nil
}

func validateUTF8Value(value reflect.Value, depth int) error {
	if depth >= MaxUniqueJSONDepth {
		return ErrNonCanonicalUTF8
	}
	switch value.Kind() {
	case reflect.Invalid:
		return nil
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return ErrNonCanonicalUTF8
		}
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return validateUTF8Value(value.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.PkgPath != "" || name == "-" {
				continue
			}
			if err := validateUTF8Value(value.Field(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		for iter := value.MapRange(); iter.Next(); {
			if err := validateUTF8Value(iter.Key(), depth+1); err != nil {
				return err
			}
			if err := validateUTF8Value(iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		if value.Type() == rawMessageType {
			return ValidateCanonicalJSONUnicode(value.Bytes())
		}
		// A normal []byte is an opaque binary field encoded as base64, not
		// text. Do not reject valid media/tool evidence on byte content.
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		fallthrough
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateUTF8Value(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		// Primitive values and non-JSON object kinds are not text.
		return nil
	default:
		return ErrNonCanonicalUTF8
	}
	return nil
}

// ValidateCanonicalJSONUnicode rejects both malformed UTF-8 bytes and
// unpaired UTF-16 surrogate escapes. json.Valid and json.Unmarshal alone
// accept unpaired surrogates and encoding/json silently replaces them with
// U+FFFD, which aliases distinct frozen identities.
func ValidateCanonicalJSONUnicode(raw []byte) error {
	if !utf8.Valid(raw) || !json.Valid(raw) {
		return ErrNonCanonicalUTF8
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++ // Escape was validated by json.Valid.
			if raw[i] != 'u' {
				continue
			}
			r, err := parseJSONUnicodeEscape(raw, i)
			if err != nil {
				return err
			}
			i += 4 // Consume the four hex digits.
			if r >= 0xdc00 && r <= 0xdfff {
				return ErrNonCanonicalUTF8 // Lone low surrogate.
			}
			if r >= 0xd800 && r <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return ErrNonCanonicalUTF8
				}
				low, err := parseJSONUnicodeEscape(raw, i+2)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return ErrNonCanonicalUTF8
				}
				i += 6 // Consume the next \uXXXX.
			}
		}
	}
	return nil
}

func parseJSONUnicodeEscape(raw []byte, u int) (uint64, error) {
	if u+4 >= len(raw) || raw[u] != 'u' {
		return 0, ErrNonCanonicalUTF8
	}
	n, err := strconv.ParseUint(string(raw[u+1:u+5]), 16, 16)
	if err != nil {
		return 0, ErrNonCanonicalUTF8
	}
	return n, nil
}
