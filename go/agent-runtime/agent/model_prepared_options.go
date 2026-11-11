package agent

import (
	"bytes"
	"encoding/json"

	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// Preparation may add host defaults, never remove or override a caller's option.
// Arrays and scalar values are atomic. Duplicate keys and malformed JSON fail closed.
func additivePreparedOptions(original, candidate json.RawMessage) bool {
	if reflect.DeepEqual(original, candidate) {
		return true
	}
	decode := func(raw json.RawMessage) (map[string]any, bool) {
		if len(raw) == 0 {
			return map[string]any{}, true
		}
		if jsoncontract.ValidateUniqueJSONObject(raw) != nil {
			return nil, false
		}
		var value map[string]any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		err := decoder.Decode(&value)
		return value, err == nil && value != nil
	}
	before, ok := decode(original)
	if !ok {
		return false
	}
	after, ok := decode(candidate)
	if !ok {
		return false
	}
	return containsPreparedOptions(before, after)
}
func containsPreparedOptions(before, after map[string]any) bool {
	for key, value := range before {
		next, ok := after[key]
		if !ok {
			return false
		}
		object, nested := value.(map[string]any)
		if nested {
			nextObject, ok := next.(map[string]any)
			if !ok || !containsPreparedOptions(object, nextObject) {
				return false
			}
		} else if !reflect.DeepEqual(value, next) {
			return false
		}
	}
	return true
}
