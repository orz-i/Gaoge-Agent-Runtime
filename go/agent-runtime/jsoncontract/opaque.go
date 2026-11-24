package jsoncontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// ErrOpaqueEvidence denotes a missing or contradictory original-byte sidecar.
var ErrOpaqueEvidence = errors.New("invalid original JSON evidence")

// OpaqueEvidence binds the original typed JSON to raw fields that encoding/json
// would compact or escape. The version domain separates evidence owners. The
// digest detects corruption, not a privileged writer's forgery.
type OpaqueEvidence struct {
	Version string        `json:"version"`
	Fields  []OpaqueField `json:"fields"`
	Digest  string        `json:"digest"`
}

// OpaqueField identifies one original RawMessage by its typed owner path.
// A nil Raw explicitly records a missing value at a position which JSON
// would otherwise write as null. Non-nil Raw always contains valid JSON.
type OpaqueField struct {
	Path string `json:"path"`
	Raw  []byte `json:"raw"`
}

// SealOpaque is the single original-byte identity and persistence codec.
// Only RawMessage fields changed by the ordinary JSON encoder need a sidecar.
// In particular, missing non-omitted fields must not acquire JSON null bytes.
func SealOpaque(version string, value any) (OpaqueEvidence, error) {
	if version == "" {
		return OpaqueEvidence{}, ErrOpaqueEvidence
	}
	if err := ValidateCanonicalUTF8(value); err != nil {
		return OpaqueEvidence{}, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return OpaqueEvidence{}, err
	}
	fields := make([]OpaqueField, 0)
	err = walkOpaqueRaw(reflect.ValueOf(value), "", false, false, func(path string, value reflect.Value, omitNil bool) error {
		original := value.Interface().(json.RawMessage)
		if original == nil {
			if !omitNil {
				fields = append(fields, OpaqueField{Path: path})
			}
			return nil
		}
		encoded, e := json.Marshal(original)
		if e != nil {
			return e
		}
		if !bytes.Equal(original, encoded) {
			fields = append(fields, OpaqueField{Path: path, Raw: bytes.Clone(original)})
		}
		return nil
	})
	if err != nil {
		return OpaqueEvidence{}, err
	}
	return SealOpaqueFields(version, canonical, fields)
}

// RestoreOpaque verifies the original sidecar against an ordinary decoded
// typed value and restores original bytes in place. Use a fresh decode target.
func RestoreOpaque(version string, target any, recorded *OpaqueEvidence) error {
	value := reflect.ValueOf(target)
	if recorded == nil || recorded.Version != version || version == "" || value.Kind() != reflect.Pointer || value.IsNil() {
		return ErrOpaqueEvidence
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	seal, err := SealOpaqueFields(version, canonical, recorded.Fields)
	if err != nil || seal.Digest != recorded.Digest {
		return ErrOpaqueEvidence
	}
	byPath := make(map[string][]byte, len(recorded.Fields))
	for _, field := range recorded.Fields {
		if field.Raw != nil && !json.Valid(field.Raw) {
			return ErrOpaqueEvidence
		}
		if _, duplicate := byPath[field.Path]; duplicate {
			return ErrOpaqueEvidence
		}
		byPath[field.Path] = field.Raw
	}
	consumed := 0
	err = walkOpaqueRaw(value.Elem(), "", true, false, func(path string, value reflect.Value, omitNil bool) error {
		original, present := byPath[path]
		if !present {
			return nil
		}
		if !value.CanSet() {
			return ErrOpaqueEvidence
		}
		if original == nil {
			// Only a required null placeholder can represent missing bytes.
			// For an omittable leaf, explicit null and absence already have
			// different canonical JSON, so an absence marker is contradictory.
			if omitNil || !bytes.Equal(value.Bytes(), []byte("null")) {
				return ErrOpaqueEvidence
			}
			value.SetZero()
		} else {
			normalized, e := json.Marshal(json.RawMessage(original))
			if e != nil || bytes.Equal(normalized, original) || !bytes.Equal(normalized, value.Bytes()) {
				return ErrOpaqueEvidence
			}
			value.Set(reflect.ValueOf(json.RawMessage(bytes.Clone(original))))
		}
		consumed++
		return nil
	})
	if err != nil || consumed != len(recorded.Fields) {
		return ErrOpaqueEvidence
	}
	return ValidateCanonicalUTF8(target)
}

// SealOpaqueFields seals an already collected representation using the
// versioned, length-independent original-evidence hash frame.
func SealOpaqueFields(version string, canonical []byte, fields []OpaqueField) (OpaqueEvidence, error) {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return OpaqueEvidence{}, err
	}
	// Hash the exact same framed bytes incrementally. A combined-size
	// allocation can overflow for large original Run histories and needlessly
	// duplicate all already allocated State/sidecar bytes in memory.
	h := sha256.New()
	_, _ = h.Write([]byte(version))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(canonical)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(encoded)
	return OpaqueEvidence{Version: version, Fields: fields, Digest: hex.EncodeToString(h.Sum(nil))}, nil
}

// walkOpaqueRaw walks the *actual* typed value, not a JSON-value copy which
// would already have lost opaque bytes. Every raw field, including those inside
// nested ModelInvocations, approvals, receipts and future typed fields, crosses
// the same path; map keys are sorted for deterministic persistence. The caller
// owns the mutable state and does not share it across parallel decoders.
func walkOpaqueRaw(value reflect.Value, path string, restore, omitNil bool, visit func(string, reflect.Value, bool) error) error {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == rawMessageType {
		return visit(path, value, omitNil)
	}
	// Leaf scalar kinds are not omitted accidentally: by definition they
	// cannot hold typed json.RawMessage evidence.
	switch value.Kind() { //nolint:exhaustive // All scalar reflect kinds are intentional leaves.
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		if !restore {
			return walkOpaqueRaw(value.Elem(), path, false, false, visit)
		}
		if !value.CanSet() {
			return ErrOpaqueEvidence
		}
		copy := reflect.New(value.Elem().Type()).Elem()
		copy.Set(value.Elem())
		if err := walkOpaqueRaw(copy, path, true, false, visit); err != nil {
			return err
		}
		value.Set(copy)
	case reflect.Pointer:
		if !value.IsNil() {
			return walkOpaqueRaw(value.Elem(), path, restore, false, visit)
		}
	case reflect.Struct:
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			tag := field.Tag.Get("json")
			if field.PkgPath != "" || tag == "-" { // Never traverse hidden or non-JSON fields.
				continue
			}
			// A nil direct RawMessage is omitted only by these JSON options.
			// Elements of slices/maps and required struct fields serialize null.
			omit := false
			if field.Type == rawMessageType {
				_, options, _ := strings.Cut(tag, ",")
				flags := strings.Split(options, ",")
				omit = slices.Contains(flags, "omitempty") || slices.Contains(flags, "omitzero")
			}
			if err := walkOpaqueRaw(value.Field(i), path+"."+field.Name, restore, omit, visit); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if err := walkOpaqueRaw(value.Index(i), path+"["+strconv.Itoa(i)+"]", restore, false, visit); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return ErrOpaqueEvidence
		}
		keys := value.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, key := range keys {
			next := value.MapIndex(key)
			// Map values are never directly addressable. Copy back only a
			// value which may have restored raw evidence.
			copy := reflect.New(next.Type()).Elem()
			copy.Set(next)
			if err := walkOpaqueRaw(copy, path+"{"+strconv.Quote(key.String())+"}", restore, false, visit); err != nil {
				return err
			}
			if restore {
				value.SetMapIndex(key, copy)
			}
		}
	default:
		// Scalar values never contain opaque JSON; no physical evidence to
		// restore. Explicitly exclude them from this type-domain traversal.
		return nil
	}
	return nil
}
