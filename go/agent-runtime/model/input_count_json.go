package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// MarshalRequestForInputCount retains the precise typed JSON encoding of a
// frozen Model request for a provider-native input counter. Generic JSON
// normalization changes RawMessage object key order, and json.Marshal's
// default HTML escaping can alter signed Tool arguments while their opaque
// ProviderReplay still contains the original argument spelling.
//
// This is NOT a provider-native wire serializer, an execution authorization
// or a signed-replay verifier. The adapter still validates the original
// Tool/ProviderReplay byte equality and selects its own physical protocol.
func MarshalRequestForInputCount(request Request) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(request); err != nil {
		return nil, err
	}
	// Encoder.Encode adds precisely one trailing newline; the corresponding
	// counter bridge enforces an exact typed round trip with the same function.
	encoded := output.Bytes()
	return append([]byte(nil), encoded[:len(encoded)-1]...), nil
}

// PhysicalRequestHash identifies the exact typed Model request at the SDK
// dispatch boundary, including every sampling restriction and invocation ID.
// It is not a hash of a provider-native HTTP payload or a logical reservation.
func PhysicalRequestHash(request Request) (string, error) {
	raw, err := MarshalRequestForInputCount(CloneRequest(request))
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}
