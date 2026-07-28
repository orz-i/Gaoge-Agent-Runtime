package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

var errMultipleWorkflowJSONValues = errors.New("workflow JSON must contain one value")

func validateWorkflowSchema(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", ErrWorkflowSchemaInvalid
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	if err := ensureSingleWorkflowJSONValue(decoder); err != nil {
		return "", errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	if err := rejectRemoteWorkflowSchemaRefs(value); err != nil {
		return "", err
	}
	canonical, err := canonicalWorkflowJSON(value)
	if err != nil {
		return "", errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	const resource = "urn:gaoge:workflow:schema"
	if err = compiler.AddResource(resource, bytes.NewReader(canonical)); err != nil {
		return "", errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	if _, err = compiler.Compile(resource); err != nil {
		return "", errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	return string(canonical), nil
}

func validateWorkflowJSON(schemaRaw json.RawMessage, value interface{}) error {
	canonical, err := validateWorkflowSchema(schemaRaw)
	if err != nil {
		return err
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft2020
	const resource = "urn:gaoge:workflow:instance-schema"
	if err = compiler.AddResource(resource, strings.NewReader(canonical)); err != nil {
		return errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return errors.Join(ErrWorkflowSchemaInvalid, err)
	}
	if err = schema.Validate(value); err != nil {
		return errors.Join(ErrWorkflowSchemaValidation, err)
	}
	return nil
}

func decodeWorkflowJSON(raw json.RawMessage) (interface{}, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result interface{}
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if err := ensureSingleWorkflowJSONValue(decoder); err != nil {
		return nil, err
	}
	return result, nil
}

func ensureSingleWorkflowJSONValue(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errMultipleWorkflowJSONValues
		}
		return err
	}
	return nil
}

func rejectRemoteWorkflowSchemaRefs(value interface{}) error {
	switch typed := value.(type) {
	case map[string]interface{}:
		return rejectRemoteWorkflowSchemaObject(typed)
	case []interface{}:
		return rejectRemoteWorkflowSchemaArray(typed)
	}
	return nil
}

func rejectRemoteWorkflowSchemaObject(value map[string]interface{}) error {
	for key, child := range value {
		if err := validateWorkflowSchemaReferenceKeyword(key, child); err != nil {
			return err
		}
		if err := rejectRemoteWorkflowSchemaRefs(child); err != nil {
			return err
		}
	}
	return nil
}

func rejectRemoteWorkflowSchemaArray(value []interface{}) error {
	for _, child := range value {
		if err := rejectRemoteWorkflowSchemaRefs(child); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowSchemaReferenceKeyword(key string, value interface{}) error {
	switch key {
	case "$ref":
		ref, ok := value.(string)
		if !ok || !strings.HasPrefix(ref, "#") {
			return fmt.Errorf("%w: remote $ref is forbidden", ErrWorkflowSchemaInvalid)
		}
	case "$id":
		id, ok := value.(string)
		if !ok || strings.Contains(id, "://") {
			return fmt.Errorf("%w: remote $id is forbidden", ErrWorkflowSchemaInvalid)
		}
	}
	return nil
}
