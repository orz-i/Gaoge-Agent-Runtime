// Package jsoncontract compiles and validates JSON Schema contracts shared by
// Runtime features. It is deliberately outside Kernel: schemas are feature
// contracts, not state-machine semantics.
package jsoncontract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	ErrInvalidSchema   = errors.New("invalid JSON schema")
	ErrInvalidDocument = errors.New("invalid JSON document")
	ErrSchemaViolation = errors.New("JSON schema violation")

	compiledSchemas sync.Map // map[string]*Validator
)

// Validator is one immutable compiled JSON Schema contract. Validation is safe
// for concurrent callers because the underlying compiled schema is immutable.
type Validator struct {
	schema *jsonschema.Schema
}

// Violation is a structured instance-validation failure with an RFC 6901-style
// JSON Pointer to the failing value when the validator provides one.
type Violation struct {
	Path  string
	Cause error
}

func (err *Violation) Error() string {
	if err == nil {
		return ErrSchemaViolation.Error()
	}
	path := err.Path
	if path == "" {
		path = "/"
	}
	if err.Cause == nil {
		return fmt.Sprintf("%s at %s", ErrSchemaViolation, path)
	}
	return fmt.Sprintf("%s at %s: %s", ErrSchemaViolation, path, err.Cause)
}

func (err *Violation) Unwrap() error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrSchemaViolation, err.Cause)
}

// Compile validates and compiles one JSON Schema. Identical schema bytes share
// one process-local compiled validator; no remote reference loading is allowed.
func Compile(raw json.RawMessage) (*Validator, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, ErrInvalidSchema
	}
	digest := sha256.Sum256(raw)
	key := hex.EncodeToString(digest[:])
	if cached, ok := compiledSchemas.Load(key); ok {
		return cached.(*Validator), nil
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.Join(ErrInvalidSchema, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectRemoteLoader{})
	location := "https://agent-runtime.invalid/schema/" + key
	if err = compiler.AddResource(location, document); err != nil {
		return nil, errors.Join(ErrInvalidSchema, err)
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return nil, errors.Join(ErrInvalidSchema, err)
	}
	validator := &Validator{schema: compiled}
	actual, _ := compiledSchemas.LoadOrStore(key, validator)
	return actual.(*Validator), nil
}

// Validate parses one JSON value without losing number precision and validates
// it against the compiled schema.
func (validator *Validator) Validate(raw json.RawMessage) error {
	if validator == nil || validator.schema == nil || len(raw) == 0 || !json.Valid(raw) {
		return ErrInvalidDocument
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return errors.Join(ErrInvalidDocument, err)
	}
	if err = validator.schema.Validate(document); err == nil {
		return nil
	}
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return errors.Join(ErrSchemaViolation, err)
	}
	leaf := firstLeaf(validation)
	return &Violation{Path: jsonPointer(leaf.InstanceLocation), Cause: leaf}
}

func firstLeaf(err *jsonschema.ValidationError) *jsonschema.ValidationError {
	for err != nil && len(err.Causes) > 0 {
		err = err.Causes[0]
	}
	return err
}

func jsonPointer(parts []string) string {
	if len(parts) == 0 {
		return "/"
	}
	escaped := make([]string, len(parts))
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~", "~0")
		part = strings.ReplaceAll(part, "/", "~1")
		escaped[index] = part
	}
	return "/" + strings.Join(escaped, "/")
}

type rejectRemoteLoader struct{}

func (rejectRemoteLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("%w: external schema reference %q is disabled", ErrInvalidSchema, url)
}
