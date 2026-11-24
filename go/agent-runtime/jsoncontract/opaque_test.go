package jsoncontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type presenceEvidenceFixture struct {
	Required json.RawMessage            `json:"required"`
	Optional json.RawMessage            `json:"optional,omitempty"`
	Zero     json.RawMessage            `json:"zero,omitzero"`
	Array    [1]json.RawMessage         `json:"array"`
	Slice    []json.RawMessage          `json:"slice"`
	Map      map[string]json.RawMessage `json:"map"`
}

func TestOpaqueEvidencePreservesPresenceAndBytesAcrossTypedPositions(t *testing.T) {
	const version = "test-opaque-v2"
	for _, raw := range []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage(" null "), json.RawMessage(`{ "x":"<>&", "n":9007199254740993 }`), json.RawMessage("[]"), json.RawMessage("false")} {
		t.Run(string(raw)+"-raw", func(t *testing.T) {
			original := presenceEvidenceFixture{Required: raw, Optional: raw, Zero: raw, Array: [1]json.RawMessage{raw}, Slice: []json.RawMessage{raw}, Map: map[string]json.RawMessage{"x": raw}}
			evidence, err := SealOpaque(version, original)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			encodedSeal, err := json.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			var reopenedSeal OpaqueEvidence
			if err = json.Unmarshal(encodedSeal, &reopenedSeal); err != nil {
				t.Fatal(err)
			}
			var reopened presenceEvidenceFixture
			if err = json.Unmarshal(data, &reopened); err != nil {
				t.Fatal(err)
			}
			if err = RestoreOpaque(version, &reopened, &reopenedSeal); err != nil || !reflect.DeepEqual(reopened, original) {
				t.Fatalf("original presence or bytes lost: got=%#v want=%#v err=%v", reopened, original, err)
			}
			again, err := SealOpaque(version, reopened)
			if err != nil || !reflect.DeepEqual(evidence, again) {
				t.Fatalf("seal changed on round trip: %v", err)
			}
			if raw == nil && len(evidence.Fields) != 4 {
				t.Fatalf("expected missing markers only at required/collection positions: %+v", evidence.Fields)
			}
		})
	}
	original := presenceEvidenceFixture{Slice: []json.RawMessage{nil}, Map: map[string]json.RawMessage{"x": nil}}
	base, err := SealOpaque(version, original)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*presenceEvidenceFixture){
		"required": func(v *presenceEvidenceFixture) { v.Required = json.RawMessage("null") },
		"optional": func(v *presenceEvidenceFixture) { v.Optional = json.RawMessage("null") },
		"omitzero": func(v *presenceEvidenceFixture) { v.Zero = json.RawMessage("null") },
		"array":    func(v *presenceEvidenceFixture) { v.Array[0] = json.RawMessage("null") },
		"slice":    func(v *presenceEvidenceFixture) { v.Slice[0] = json.RawMessage("null") },
		"map":      func(v *presenceEvidenceFixture) { v.Map["x"] = json.RawMessage("null") },
	} {
		t.Run(name, func(t *testing.T) {
			changed := presenceEvidenceFixture{Slice: []json.RawMessage{nil}, Map: map[string]json.RawMessage{"x": nil}}
			change(&changed)
			other, err := SealOpaque(version, changed)
			if err != nil || other.Digest == base.Digest {
				t.Fatalf("missing and explicit null collided: %v", err)
			}
		})
	}
	if _, err := SealOpaque(version, presenceEvidenceFixture{Required: json.RawMessage{}}); err == nil {
		t.Fatal("non-nil malformed empty JSON accepted")
	}
}

func TestOpaqueEvidenceRejectsContradictoryAbsenceMarkers(t *testing.T) {
	const version = "test-opaque-v2"
	original := presenceEvidenceFixture{Required: json.RawMessage(`{}`), Optional: json.RawMessage("null")}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := SealOpaque(version, original)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		field OpaqueField
	}{
		{"required-not-null", OpaqueField{Path: ".Required"}},
		{"optional-present-null", OpaqueField{Path: ".Optional"}},
		{"optional-omitted", OpaqueField{Path: ".Zero"}},
		{"foreign-path", OpaqueField{Path: ".Unknown"}},
		{"empty-not-missing", OpaqueField{Path: ".Required", Raw: []byte{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Recompute the seal to exercise typed-position checks, not merely
			// the outer digest mismatch.
			fields := append(append([]OpaqueField{}, evidence.Fields...), tc.field)
			forged, err := SealOpaqueFields(version, data, fields)
			if err != nil {
				t.Fatal(err)
			}
			var decoded presenceEvidenceFixture
			if err = json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if err = RestoreOpaque(version, &decoded, &forged); !errors.Is(err, ErrOpaqueEvidence) {
				t.Fatalf("contradictory marker accepted: %v", err)
			}
		})
	}
}

func TestOpaqueEvidenceRootMissingRawMessage(t *testing.T) {
	const version = "test-opaque-v2"
	var raw json.RawMessage
	sealed, err := SealOpaque(version, raw)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("null")) {
		t.Fatal("fixture projection")
	}
	var decoded json.RawMessage
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = RestoreOpaque(version, &decoded, &sealed); err != nil || decoded != nil {
		t.Fatalf("root missing field was not restored: %q %v", decoded, err)
	}
}
