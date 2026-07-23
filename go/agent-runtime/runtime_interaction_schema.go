package agentruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

const (
	valueStringFB5188AC = "string"
	valueType981B0B96   = "type"
)

const (
	valueAdditionalProperties957F5897 = "additionalProperties"
	valueBoolean530FF2A7              = "boolean"
	valueObjectE97B31A9               = "object"
)

type runInteractionSchema struct {
	Type                 string                     `json:"type"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
	Enum                 []interface{}              `json:"enum"`
	MinLength            *int                       `json:"minLength"`
	MaxLength            *int                       `json:"maxLength"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
}

var runInteractionSchemaKeywords = map[string]struct{}{
	valueType981B0B96: {}, "required": {}, "properties": {}, "enum": {}, "minLength": {}, "maxLength": {}, valueAdditionalProperties957F5897: {},
}

type runInteractionDefinitionValidator func(runInteractionSchema) error

type runInteractionNodeValidator func(runInteractionSchema, interface{}, string) error

func validateRunInteractionResponse(schemaJSON string, response interface{}) error {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(schemaJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return fmt.Errorf("%w: invalid schema JSON", ErrRunInteractionSchemaIncompatible)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing schema content", ErrRunInteractionSchemaIncompatible)
	}
	if err := validateRunInteractionSchemaDefinition(raw, true); err != nil {
		return err
	}
	return validateRunInteractionSchemaNode(raw, response, "$", true)
}

func validateRunInteractionSchemaDefinition(raw map[string]json.RawMessage, root bool) error {
	if err := validateRunInteractionSchemaKeywords(raw); err != nil {
		return err
	}
	schema, err := decodeRunInteractionSchema(raw)
	if err != nil {
		return err
	}
	if err = validateRunInteractionSchemaShape(schema, root); err != nil {
		return err
	}
	validator, supported := runInteractionDefinitionValidatorFor(schema.Type)
	if !supported {
		return fmt.Errorf("%w: unsupported type %s", ErrRunInteractionSchemaIncompatible, schema.Type)
	}
	return validator(schema)
}

func validateRunInteractionSchemaNode(raw map[string]json.RawMessage, value interface{}, path string, root bool) error {
	schema, err := decodeRunInteractionSchema(raw)
	if err != nil {
		return err
	}
	if err = validateRunInteractionSchemaShape(schema, root); err != nil {
		return err
	}
	validator, supported := runInteractionNodeValidatorFor(schema.Type)
	if !supported {
		return fmt.Errorf("%w: unsupported type %s", ErrRunInteractionSchemaIncompatible, schema.Type)
	}
	if err = validator(schema, value, path); err != nil {
		return err
	}
	return validateRunInteractionEnum(schema.Enum, value, path)
}

func runInteractionDefinitionValidatorFor(schemaType string) (runInteractionDefinitionValidator, bool) {
	switch schemaType {
	case valueObjectE97B31A9:
		return validateRunInteractionObjectDefinition, true
	case valueStringFB5188AC, valueBoolean530FF2A7:
		return validateRunInteractionScalarDefinition, true
	default:
		return nil, false
	}
}

func runInteractionNodeValidatorFor(schemaType string) (runInteractionNodeValidator, bool) {
	switch schemaType {
	case valueObjectE97B31A9:
		return validateRunInteractionObjectNode, true
	case valueStringFB5188AC:
		return validateRunInteractionStringNode, true
	case valueBoolean530FF2A7:
		return validateRunInteractionBooleanNode, true
	case "":
		return validateRunInteractionEnumOnlyNode, true
	default:
		return nil, false
	}
}

func validateRunInteractionSchemaShape(schema runInteractionSchema, root bool) error {
	if root && schema.Type != valueObjectE97B31A9 {
		return fmt.Errorf("%w: root type must be object", ErrRunInteractionSchemaIncompatible)
	}
	if !validRunInteractionStringBounds(schema) {
		return fmt.Errorf("%w: invalid string bounds", ErrRunInteractionSchemaIncompatible)
	}
	return nil
}

func validateRunInteractionScalarDefinition(schema runInteractionSchema) error {
	if len(schema.Properties) != 0 || len(schema.Required) != 0 || schema.AdditionalProperties != nil {
		return fmt.Errorf("%w: scalar schema contains object keywords", ErrRunInteractionSchemaIncompatible)
	}
	return nil
}

func validateRunInteractionBooleanNode(_ runInteractionSchema, value interface{}, path string) error {
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("%w: %s must be a boolean", ErrRunInteractionResponseInvalid, path)
	}
	return nil
}

func validateRunInteractionEnumOnlyNode(schema runInteractionSchema, _ interface{}, _ string) error {
	if len(schema.Enum) == 0 {
		return fmt.Errorf("%w: schema type is missing", ErrRunInteractionSchemaIncompatible)
	}
	return nil
}

func validateRunInteractionSchemaKeywords(raw map[string]json.RawMessage) error {
	for keyword := range raw {
		if _, ok := runInteractionSchemaKeywords[keyword]; !ok {
			return fmt.Errorf("%w: unsupported keyword %s", ErrRunInteractionSchemaIncompatible, keyword)
		}
	}
	return nil
}

func decodeRunInteractionSchema(raw map[string]json.RawMessage) (runInteractionSchema, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return runInteractionSchema{}, fmt.Errorf("%w: schema encoding", ErrRunInteractionSchemaIncompatible)
	}
	var schema runInteractionSchema
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&schema); err != nil {
		return runInteractionSchema{}, fmt.Errorf("%w: %w", ErrRunInteractionSchemaIncompatible, err)
	}
	return schema, nil
}

func validRunInteractionStringBounds(schema runInteractionSchema) bool {
	if schema.MinLength != nil && *schema.MinLength < 0 || schema.MaxLength != nil && *schema.MaxLength < 0 {
		return false
	}
	return schema.MinLength == nil || schema.MaxLength == nil || *schema.MinLength <= *schema.MaxLength
}

func validateRunInteractionObjectDefinition(schema runInteractionSchema) error {
	if schema.AdditionalProperties == nil {
		return fmt.Errorf("%w: additionalProperties policy is required", ErrRunInteractionSchemaIncompatible)
	}
	seen := make(map[string]struct{}, len(schema.Required))
	for _, required := range schema.Required {
		if _, duplicate := seen[required]; duplicate {
			return fmt.Errorf("%w: duplicate required property %s", ErrRunInteractionSchemaIncompatible, required)
		}
		seen[required] = struct{}{}
		if _, declared := schema.Properties[required]; !declared {
			return fmt.Errorf("%w: required property %s is undeclared", ErrRunInteractionSchemaIncompatible, required)
		}
	}
	for name, child := range schema.Properties {
		childRaw, valid := decodeRunInteractionChildSchema(child)
		if !valid {
			return fmt.Errorf("%w: invalid property schema %s", ErrRunInteractionSchemaIncompatible, name)
		}
		if err := validateRunInteractionSchemaDefinition(childRaw, false); err != nil {
			return err
		}
	}
	return nil
}

func decodeRunInteractionChildSchema(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var child map[string]json.RawMessage
	err := json.Unmarshal(raw, &child)
	return child, err == nil && child != nil
}

func validateRunInteractionObjectNode(schema runInteractionSchema, value interface{}, path string) error {
	object, ok := value.(map[string]interface{})
	if !ok {
		return fmt.Errorf("%w: %s must be an object", ErrRunInteractionResponseInvalid, path)
	}
	if err := validateRequiredRunInteractionProperties(schema, object, path); err != nil {
		return err
	}
	if schema.AdditionalProperties == nil {
		return fmt.Errorf("%w: additionalProperties policy is required", ErrRunInteractionSchemaIncompatible)
	}
	for key, child := range object {
		childSchema, declared := schema.Properties[key]
		if !declared {
			if !*schema.AdditionalProperties {
				return fmt.Errorf("%w: %s.%s is not allowed", ErrRunInteractionResponseInvalid, path, key)
			}
			continue
		}
		childRaw, valid := decodeRunInteractionChildSchema(childSchema)
		if !valid {
			return fmt.Errorf("%w: invalid property schema %s", ErrRunInteractionSchemaIncompatible, key)
		}
		if err := validateRunInteractionSchemaNode(childRaw, child, path+"."+key, false); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredRunInteractionProperties(schema runInteractionSchema, object map[string]interface{}, path string) error {
	for _, required := range schema.Required {
		if _, declared := schema.Properties[required]; !declared {
			return fmt.Errorf("%w: required property %s is undeclared", ErrRunInteractionSchemaIncompatible, required)
		}
		if _, present := object[required]; !present {
			return fmt.Errorf("%w: %s.%s is required", ErrRunInteractionResponseInvalid, path, required)
		}
	}
	return nil
}

func validateRunInteractionStringNode(schema runInteractionSchema, value interface{}, path string) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%w: %s must be a string", ErrRunInteractionResponseInvalid, path)
	}
	length := utf8.RuneCountInString(text)
	if schema.MinLength != nil && length < *schema.MinLength || schema.MaxLength != nil && length > *schema.MaxLength {
		return fmt.Errorf("%w: %s length is out of range", ErrRunInteractionResponseInvalid, path)
	}
	return nil
}

func validateRunInteractionEnum(allowedValues []interface{}, value interface{}, path string) error {
	if len(allowedValues) == 0 {
		return nil
	}
	for _, allowed := range allowedValues {
		if reflect.DeepEqual(allowed, value) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is not an allowed value", ErrRunInteractionResponseInvalid, path)
}
