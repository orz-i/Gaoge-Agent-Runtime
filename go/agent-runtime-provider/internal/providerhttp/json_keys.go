package providerhttp

import (
	"bytes"
	"encoding/json"
	"io"
)

// ValidateUniqueJSONKeys rejects ambiguous provider-authored JSON before any
// authority-bearing field is decoded. encoding/json otherwise silently accepts
// duplicate object members, retaining the last owner/status/Tool identity.
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
			if _, duplicate := seen[key]; duplicate {
				return ErrProtocol
			}
			seen[key] = struct{}{}
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
