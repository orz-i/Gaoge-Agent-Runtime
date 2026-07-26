package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

const maxToolContractBytes = 256 * 1024

var (
	ErrToolSchemaInvalid    = errors.New("tool schema is invalid")
	ErrToolArgumentsInvalid = errors.New("tool arguments are invalid")
	ErrToolOutputInvalid    = errors.New("tool output is invalid")
	errExternalSchemaRef    = errors.New("external schema references are not allowed")
)

// ToolContractError reports only the failed contract boundary and instance
// path. Argument and output values are intentionally excluded from the error.
type ToolContractError struct {
	Kind   string
	Path   string
	Reason string
}

func validateToolContractSchemas(inputSchema, outputSchema json.RawMessage) error {
	if _, err := compileToolContractSchema(inputSchema); err != nil {
		return err
	}
	if optionalToolContractSchemaAbsent(outputSchema) {
		return nil
	}
	_, err := compileToolContractSchema(outputSchema)
	return err
}

func optionalToolContractSchemaAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func (e *ToolContractError) Error() string {
	if e == nil {
		return "tool contract validation failed"
	}
	path := strings.TrimSpace(e.Path)
	if path == "" {
		path = "$"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "contract validation failed"
	}
	return fmt.Sprintf("tool %s validation failed at %s: %s", e.Kind, path, reason)
}

func (e *ToolContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case "schema":
		return ErrToolSchemaInvalid
	case "output":
		return ErrToolOutputInvalid
	default:
		return ErrToolArgumentsInvalid
	}
}

func (e *ToolContractError) DeterministicToolFailure() bool { return e != nil }

func normalizeToolArgumentsAgainstSchema(raw string, schema json.RawMessage) (string, error) {
	value, err := decodeToolContractJSON(raw, true, "arguments")
	if err != nil {
		return "", err
	}
	if _, ok := value.(map[string]interface{}); !ok {
		return "", newToolContractError("arguments", "$", "root value must be an object")
	}
	return validateAndCanonicalizeToolContract(schema, value, "arguments")
}

func normalizeToolOutputAgainstSchema(raw string, schema json.RawMessage, providerKind string) (string, error) {
	if optionalToolContractSchemaAbsent(schema) {
		if !utf8.ValidString(raw) {
			return "", newToolContractError("output", "$", "value is not valid UTF-8")
		}
		return raw, nil
	}
	value, err := decodeToolContractJSON(raw, false, "output")
	if err != nil {
		return "", err
	}
	if strings.EqualFold(strings.TrimSpace(providerKind), valueMcpCE1A7808) {
		envelope, ok := value.(map[string]interface{})
		if !ok {
			return "", newToolContractError("output", "$", "MCP result must be an object")
		}
		structured, ok := envelope["structuredContent"]
		if !ok {
			return "", newToolContractError("output", "$/structuredContent", "MCP result is missing structuredContent required by outputSchema")
		}
		if _, err = validateAndCanonicalizeToolContract(schema, structured, "output"); err != nil {
			return "", err
		}
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return "", newToolContractError("output", "$", "value cannot be canonicalized")
		}
		return string(encoded), nil
	}
	return validateAndCanonicalizeToolContract(schema, value, "output")
}

func validateAndCanonicalizeToolContract(schema json.RawMessage, value interface{}, kind string) (string, error) {
	compiled, err := compileToolContractSchema(schema)
	if err != nil {
		return "", err
	}
	if err = compiled.Validate(value); err != nil {
		return "", toolContractValidationError(kind, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", newToolContractError(kind, "$", "value cannot be canonicalized")
	}
	return string(encoded), nil
}

func compileToolContractSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, newToolContractError("schema", "$", "schema is required")
	}
	if len(trimmed) > maxToolContractBytes {
		return nil, newToolContractError("schema", "$", "schema exceeds the contract size limit")
	}
	const schemaURL = "https://gaoge.local/runtime/tool-contract.json"
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errExternalSchemaRef
	}
	if err := compiler.AddResource(schemaURL, bytes.NewReader(trimmed)); err != nil {
		return nil, newToolContractError("schema", "$", "schema must be valid JSON Schema")
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, newToolContractError("schema", "$", "schema must be valid JSON Schema")
	}
	return compiled, nil
}

func decodeToolContractJSON(raw string, emptyObject bool, kind string) (interface{}, error) {
	value := strings.TrimSpace(raw)
	if value == "" && emptyObject {
		value = "{}"
	}
	if value == "" {
		return nil, newToolContractError(kind, "$", "value must contain one JSON document")
	}
	if len(value) > maxToolContractBytes {
		return nil, newToolContractError(kind, "$", "value exceeds the contract size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded interface{}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, newToolContractError(kind, "$", "value must be valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, newToolContractError(kind, "$", "value must contain one JSON document")
	}
	return decoded, nil
}

func toolContractValidationError(kind string, err error) error {
	path := "$"
	var validation *jsonschema.ValidationError
	if errors.As(err, &validation) && strings.TrimSpace(validation.InstanceLocation) != "" {
		path = validation.InstanceLocation
	}
	return newToolContractError(kind, path, "value does not satisfy the frozen schema")
}

func newToolContractError(kind, path, reason string) error {
	return &ToolContractError{Kind: kind, Path: path, Reason: reason}
}
