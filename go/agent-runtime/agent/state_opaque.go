package agent

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

// An Agent Run state is the original authority for Model intents, Tool calls,
// signed Provider replay, and their receipts. encoding/json compacts and HTML-
// escapes json.RawMessage even when the logical value is unchanged. Original
// physical bytes cannot be recovered from that normalized value on resume.
//
// Keep the existing typed State JSON and store *only* the RawMessage fields
// which the standard encoder would rewrite in one authenticated-by-structure
// sidecar. The internal digest detects accidental corruption; it is NOT a
// signature against a privileged database writer. Paths bind each original
// byte sequence to its exact durable field and position. No new execution or
// provider replay policy is introduced.
const originalStateEvidenceVersion = "agent-state-opaque-v1"

type originalStateEvidence struct {
	Version string                  `json:"version"`
	Fields  []originalStateRawField `json:"fields"`
	Digest  string                  `json:"digest"`
}

type originalStateRawField struct {
	Path string `json:"path"`
	Raw  []byte `json:"raw"`
}

type persistedRunState struct {
	runState
	OriginalStateEvidence *originalStateEvidence `json:"_originalStateEvidence"`
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()

func encodeOriginalRunState(state runState) (json.RawMessage, error) {
	canonical, err := json.Marshal(state)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	fields := make([]originalStateRawField, 0)
	err = walkStateRaw(reflect.ValueOf(&state).Elem(), "", false, func(path string, value reflect.Value) error {
		original := value.Interface().(json.RawMessage)
		if len(original) == 0 {
			return nil
		}
		encoded, e := json.Marshal(original)
		if e != nil {
			return e
		}
		if !bytes.Equal(original, encoded) {
			fields = append(fields, originalStateRawField{Path: path, Raw: bytes.Clone(original)})
		}
		return nil
	})
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	seal, err := sealOriginalState(canonical, fields)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	encoded, err := json.Marshal(persistedRunState{runState: state, OriginalStateEvidence: &seal})
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	return encoded, nil
}

func decodeOriginalRunState(encoded json.RawMessage) (runState, error) {
	var saved persistedRunState
	if err := json.Unmarshal(encoded, &saved); err != nil || saved.OriginalStateEvidence == nil ||
		saved.OriginalStateEvidence.Version != originalStateEvidenceVersion {
		return runState{}, ErrInvalidRequest
	}
	canonical, err := json.Marshal(saved.runState)
	if err != nil {
		return runState{}, ErrInvalidRequest
	}
	recorded := saved.OriginalStateEvidence
	seal, err := sealOriginalState(canonical, recorded.Fields)
	if err != nil || seal.Digest != recorded.Digest {
		return runState{}, ErrInvalidRequest
	}
	byPath := make(map[string][]byte, len(recorded.Fields))
	for _, field := range recorded.Fields {
		if field.Path == "" || len(field.Raw) == 0 || !json.Valid(field.Raw) {
			return runState{}, ErrInvalidRequest
		}
		if _, duplicate := byPath[field.Path]; duplicate {
			return runState{}, ErrInvalidRequest
		}
		byPath[field.Path] = field.Raw
	}
	consumed := 0
	err = walkStateRaw(reflect.ValueOf(&saved.runState).Elem(), "", true, func(path string, value reflect.Value) error {
		original, present := byPath[path]
		if !present {
			return nil
		}
		if !value.CanSet() {
			return ErrInvalidRequest
		}
		normalized, e := json.Marshal(json.RawMessage(original))
		if e != nil || bytes.Equal(normalized, original) ||
			!bytes.Equal(normalized, value.Interface().(json.RawMessage)) {
			return ErrInvalidRequest
		}
		value.Set(reflect.ValueOf(json.RawMessage(bytes.Clone(original))))
		consumed++
		return nil
	})
	if err != nil || consumed != len(recorded.Fields) {
		return runState{}, ErrInvalidRequest
	}
	return saved.runState, nil
}

func sealOriginalState(canonical []byte, fields []originalStateRawField) (originalStateEvidence, error) {
	encoded, err := json.Marshal(fields)
	if err != nil {
		return originalStateEvidence{}, err
	}
	content := make([]byte, 0, len(originalStateEvidenceVersion)+len(canonical)+len(encoded)+2)
	content = append(content, originalStateEvidenceVersion...)
	content = append(content, 0)
	content = append(content, canonical...)
	content = append(content, 0)
	content = append(content, encoded...)
	sum := sha256.Sum256(content)
	return originalStateEvidence{Version: originalStateEvidenceVersion, Fields: fields, Digest: hex.EncodeToString(sum[:])}, nil
}

// walkStateRaw walks the *actual* typed Run state, not a JSON-value copy which
// would already have lost opaque bytes. Every raw field, including those inside
// nested ModelInvocations, approvals, receipts and future typed fields, crosses
// the same path; map keys are sorted for deterministic persistence. The caller
// owns the mutable state and does not share it across parallel decoders.
func walkStateRaw(value reflect.Value, path string, restore bool, visit func(string, reflect.Value) error) error {
	if !value.IsValid() {
		return nil
	}
	if value.Type() == rawMessageType {
		return visit(path, value)
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		if !restore {
			return walkStateRaw(value.Elem(), path, false, visit)
		}
		if !value.CanSet() {
			return ErrInvalidRequest
		}
		copy := reflect.New(value.Elem().Type()).Elem()
		copy.Set(value.Elem())
		if err := walkStateRaw(copy, path, true, visit); err != nil {
			return err
		}
		value.Set(copy)
	case reflect.Pointer:
		if !value.IsNil() {
			return walkStateRaw(value.Elem(), path, restore, visit)
		}
	case reflect.Struct:
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			if typ.Field(i).PkgPath != "" { // Never traverse unexported foreign internals (e.g. time.Time).
				continue
			}
			if err := walkStateRaw(value.Field(i), path+"."+typ.Field(i).Name, restore, visit); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < value.Len(); i++ {
			if err := walkStateRaw(value.Index(i), path+"["+strconv.Itoa(i)+"]", restore, visit); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return ErrInvalidRequest
		}
		keys := value.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
		for _, key := range keys {
			next := value.MapIndex(key)
			// Map values are never directly addressable. Copy back only a
			// value which may have restored raw evidence.
			copy := reflect.New(next.Type()).Elem()
			copy.Set(next)
			if err := walkStateRaw(copy, path+"{"+strconv.Quote(key.String())+"}", restore, visit); err != nil {
				return err
			}
			if restore {
				value.SetMapIndex(key, copy)
			}
		}
	}
	return nil
}
