package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

var ErrOutputContract = errors.New("invalid strict model output contract")
var ErrCapabilityUnavailable = errors.New("model capability unavailable")

const MaxOutputSchemaBytes = 64 << 10

var outputContractName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// OutputContract requests provider-enforced JSON Schema output for an Agent's
// terminal model response. It is deliberately not a provider-specific wire
// object and never falls back to prompt-only JSON or non-strict JSON mode.
// Nil means the existing plain-text contract.
// The portable first profile supports object/array/scalar/enum schema trees;
// external references, unions and arbitrary provider-specific keywords are
// intentionally outside this profile.
type OutputContract struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

// Capabilities is model-specific; a provider's general feature list is not
// sufficient to prove a particular model supports strict output.
type Capabilities struct {
	StructuredJSONSchema bool
}

// CapabilityProvider optionally reports support for one concrete model ID.
// The Caller must reject an absent capability provider or a false result when
// a mandatory OutputContract was requested.
type CapabilityProvider interface {
	Capabilities(context.Context, string) (Capabilities, error)
}

// CloneOutputContract isolates schema bytes before hash/persistence/dispatch.
func CloneOutputContract(value *OutputContract) *OutputContract {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Schema = append(json.RawMessage(nil), value.Schema...)
	return &cloned
}

// ValidateOutputContract rejects contracts that cannot be enforced by the
// intentionally narrow cross-provider strict JSON Schema profile.
func ValidateOutputContract(value *OutputContract) error {
	if value == nil {
		return nil
	}
	if !outputContractName.MatchString(value.Name) ||
		len(value.Schema) == 0 || len(value.Schema) > MaxOutputSchemaBytes || !json.Valid(value.Schema) {
		return ErrOutputContract
	}
	if _, err := jsoncontract.Compile(value.Schema); err != nil {
		return errors.Join(ErrOutputContract, err)
	}
	var node any
	if err := json.Unmarshal(value.Schema, &node); err != nil || validateStrictSchemaNode(node, 0, "object") != nil {
		return ErrOutputContract
	}
	return nil
}

// This is the shared guaranteed subset, not an attempt to implement all of
// JSON Schema. Providers may enforce even narrower limits, which they must
// fail explicitly instead of degrading strictness.
func validateStrictSchemaNode(node any, depth int, rootType string) error {
	if depth > 9 {
		return ErrOutputContract
	}
	obj, ok := node.(map[string]any)
	if !ok || len(obj) == 0 {
		return ErrOutputContract
	}
	allowed := map[string]bool{"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true, "description": true, "enum": true, "const": true}
	for key := range obj {
		if !allowed[key] {
			return ErrOutputContract
		}
	}
	kind, ok := obj["type"].(string)
	if !ok || strings.TrimSpace(kind) == "" {
		return ErrOutputContract
	}
	if rootType != "" && kind != rootType {
		return ErrOutputContract
	}
	if description, exists := obj["description"]; exists {
		if _, ok := description.(string); !ok {
			return ErrOutputContract
		}
	}
	switch kind {
	case "object":
		props, ok := obj["properties"].(map[string]any)
		if !ok {
			return ErrOutputContract
		}
		if obj["additionalProperties"] != false {
			return ErrOutputContract
		}
		requiredRaw, ok := obj["required"].([]any)
		if !ok || len(requiredRaw) != len(props) {
			return ErrOutputContract
		}
		required := make(map[string]bool, len(requiredRaw))
		for _, item := range requiredRaw {
			name, ok := item.(string)
			if !ok || required[name] {
				return ErrOutputContract
			}
			if _, exists := props[name]; !exists {
				return ErrOutputContract
			}
			required[name] = true
		}
		for name, child := range props {
			if name == "" || !required[name] || validateStrictSchemaNode(child, depth+1, "") != nil {
				return ErrOutputContract
			}
		}
		if obj["items"] != nil || obj["enum"] != nil || obj["const"] != nil {
			return ErrOutputContract
		}
	case "array":
		items, ok := obj["items"]
		if !ok || validateStrictSchemaNode(items, depth+1, "") != nil {
			return ErrOutputContract
		}
		if obj["properties"] != nil || obj["required"] != nil || obj["additionalProperties"] != nil {
			return ErrOutputContract
		}
	case "string", "integer", "number", "boolean", "null":
		if obj["items"] != nil || obj["properties"] != nil || obj["required"] != nil || obj["additionalProperties"] != nil {
			return ErrOutputContract
		}
	default:
		return ErrOutputContract
	}
	if raw, exists := obj["enum"]; exists {
		values, ok := raw.([]any)
		if !ok || len(values) == 0 || len(values) > 1000 || kind == "array" || kind == "object" {
			return ErrOutputContract
		}
		for _, value := range values {
			if !strictScalarType(kind, value) {
				return ErrOutputContract
			}
		}
	}
	if constant, exists := obj["const"]; exists {
		if kind == "array" || kind == "object" || !strictScalarType(kind, constant) {
			return ErrOutputContract
		}
	}
	return nil
}

// Both providers require scalar enum/const values in the narrow portable
// subset. Reject impossible/mixed-type grammars locally, before dispatch.
func strictScalarType(kind string, value any) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && !math.IsNaN(number) && math.Trunc(number) == number
	case "null":
		return value == nil
	}
	return false
}
