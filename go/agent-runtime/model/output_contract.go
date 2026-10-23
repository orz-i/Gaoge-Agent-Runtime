package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// OutputContract is an opt-in, strict, provider-neutral JSON Schema output
// contract. Nil means the existing plain-text behavior. The schema is frozen
// inside each durable ModelInvocation request and is never inferred from a
// system prompt or an untrusted model response.
type OutputContract struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

var ErrInvalidOutputContract = errors.New("invalid strict model output contract")

const maxOutputSchemaBytes = 64 << 10
const maxOutputSchemaDepth = 12

var outputContractName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CloneOutputContract preserves value isolation across Agent, Context, and
// Model middleware boundaries. Nil retains legacy text-only wire contracts.
func CloneOutputContract(value *OutputContract) *OutputContract {
	if value == nil {
		return nil
	}
	next := *value
	next.Schema = append(json.RawMessage(nil), value.Schema...)
	return &next
}

// ValidateOutputContract checks a deliberately narrow, portable strict Schema
// subset shared by the OpenAI Responses and Anthropic Messages adapters.
// Unsupported keywords fail closed; neither provider may silently downgrade to
// unconstrained JSON mode. Validation is always repeated on the terminal value.
func ValidateOutputContract(value *OutputContract) error {
	if value == nil {
		return nil
	}
	if !outputContractName.MatchString(value.Name) ||
		len(value.Schema) == 0 || len(value.Schema) > maxOutputSchemaBytes ||
		!json.Valid(value.Schema) {
		return ErrInvalidOutputContract
	}
	if err := validStrictOutputNode(value.Schema, 0); err != nil {
		return err
	}
	if _, err := jsoncontract.Compile(value.Schema); err != nil {
		return errors.Join(ErrInvalidOutputContract, err)
	}
	return nil
}

// The portable subset intentionally excludes $ref, additional open properties,
// conditional keywords and numeric/length constraints: adapters must not claim
// identical strict guarantees for unsupported provider-specific schema dialects.
func validStrictOutputNode(raw json.RawMessage, depth int) error {
	if depth >= maxOutputSchemaDepth {
		return ErrInvalidOutputContract
	}
	var node map[string]json.RawMessage
	if json.Unmarshal(raw, &node) != nil || len(node) == 0 {
		return ErrInvalidOutputContract
	}
	var kind string
	if json.Unmarshal(node["type"], &kind) != nil {
		return ErrInvalidOutputContract
	}
	allowed := map[string]bool{"type": true, "description": true, "title": true, "enum": true}
	switch kind {
	case "object":
		allowed["properties"], allowed["required"], allowed["additionalProperties"] = true, true, true
		if !bytes.Equal(bytes.TrimSpace(node["additionalProperties"]), []byte("false")) {
			return ErrInvalidOutputContract
		}
		var properties map[string]json.RawMessage
		var required []string
		if json.Unmarshal(node["properties"], &properties) != nil || properties == nil ||
			json.Unmarshal(node["required"], &required) != nil || len(required) != len(properties) {
			return ErrInvalidOutputContract
		}
		seen := make(map[string]bool, len(required))
		for _, key := range required {
			if _, ok := properties[key]; !ok || seen[key] {
				return ErrInvalidOutputContract
			}
			seen[key] = true
		}
		for _, child := range properties {
			if err := validStrictOutputNode(child, depth+1); err != nil {
				return err
			}
		}
	case "array":
		allowed["items"] = true
		if len(node["items"]) == 0 {
			return ErrInvalidOutputContract
		}
		if err := validStrictOutputNode(node["items"], depth+1); err != nil {
			return err
		}
	case "string", "number", "integer", "boolean", "null":
	default:
		return ErrInvalidOutputContract
	}
	for key, value := range node {
		if !allowed[key] || len(value) == 0 {
			return ErrInvalidOutputContract
		}
	}
	return nil
}

// OutputContractSupport is a request-specific, read-only provider capability
// check. A provider/router may reject an unsupported model, format or option
// BEFORE the immutable Agent ModelInvocation intent is persisted. It cannot
// guarantee the remote service's future availability or override server-side
// model compatibility checks.
type OutputContractSupport interface {
	CheckOutputContract(context.Context, Request) error
}
