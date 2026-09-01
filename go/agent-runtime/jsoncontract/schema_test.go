package jsoncontract_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

func TestCompileSupportsMixedDraftsAndRejectsInvalidSchemas(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"$schema":"http://json-schema.org/draft-04/schema","type":"object","required":["id"]}`,
		`{"$schema":"http://json-schema.org/draft-07/schema","type":"object"}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`,
	} {
		if _, err := jsoncontract.Compile(json.RawMessage(raw)); err != nil {
			t.Fatalf("compile %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"type":"definitely-not-a-json-schema-type"}`,
		`{"$ref":"https://example.com/schema.json"}`,
		`{"type":`,
	} {
		if _, err := jsoncontract.Compile(json.RawMessage(raw)); !errors.Is(err, jsoncontract.ErrInvalidSchema) {
			t.Fatalf("compile invalid %q: %v", raw, err)
		}
	}
}

func TestValidateReportsInstancePathAndMalformedJSON(t *testing.T) {
	t.Parallel()
	validator, err := jsoncontract.Compile(json.RawMessage(`{
		"type":"object","required":["item"],
		"properties":{"item":{"type":"object","properties":{"count":{"type":"integer","minimum":1}},"required":["count"]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = validator.Validate(json.RawMessage(`{"item":{"count":0}}`)); !errors.Is(err, jsoncontract.ErrSchemaViolation) ||
		!strings.Contains(err.Error(), "/item/count") {
		t.Fatalf("violation = %v", err)
	}
	if err = validator.Validate(json.RawMessage(`{"item":`)); !errors.Is(err, jsoncontract.ErrInvalidDocument) {
		t.Fatalf("malformed document = %v", err)
	}
}

func FuzzValidateJSONDoesNotPanic(f *testing.F) {
	validator, err := jsoncontract.Compile(json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}}}`))
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte(`{"value":1}`))
	f.Add([]byte(`{"value":"wrong"}`))
	f.Add([]byte(`{"value":`))
	f.Fuzz(func(t *testing.T, input []byte) {
		_ = validator.Validate(json.RawMessage(input))
	})
}
