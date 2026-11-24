package model

import (
	"bytes"
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
