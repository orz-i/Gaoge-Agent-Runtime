package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func schemaTestPointer[T any](value T) *T { return &value }

func TestBaselineSchemaCheckExplainsOnlyFixedFacts(t *testing.T) {
	validator, err := jsoncontract.Compile(probeSchema)
	if err != nil {
		t.Fatal("fixed probe schema did not compile")
	}
	for _, tc := range []struct {
		name, content, kind, okType string
		schemaValid                 bool
		okPresent, okTrue           *bool
		extra                       *int
	}{
		{name: "empty", kind: "invalid"},
		{name: "plain-text", content: "private-schema-fixture-key", kind: "invalid"},
		{name: "code-fence", content: "```json\n{\"ok\":true}\n```", kind: "invalid"},
		{name: "two-documents", content: `{"ok":true}{"ok":true}`, kind: "invalid"},
		{name: "string", content: `"private-schema-fixture-key"`, kind: "string"},
		{name: "array", content: `["private-schema-fixture-key"]`, kind: "array"},
		{name: "number", content: "17", kind: "number"},
		{name: "boolean", content: "true", kind: "boolean"},
		{name: "null", content: "null", kind: "null"},
		{name: "missing-ok", content: "{}", kind: "object", okType: "missing",
			okPresent: schemaTestPointer(false), extra: schemaTestPointer(0)},
		{name: "wrong-ok-type", content: `{"ok":"private-schema-fixture-key"}`, kind: "object", okType: "string",
			okPresent: schemaTestPointer(true), extra: schemaTestPointer(0)},
		{name: "false-ok", content: `{"ok":false}`, kind: "object", okType: "boolean",
			okPresent: schemaTestPointer(true), okTrue: schemaTestPointer(false), extra: schemaTestPointer(0)},
		{name: "extra-property", content: `{"ok":true,"private-schema-fixture-key":"private-schema-fixture-key"}`,
			kind: "object", okType: "boolean", okPresent: schemaTestPointer(true), okTrue: schemaTestPointer(true), extra: schemaTestPointer(1)},
		{name: "valid", content: `{"ok":true}`, kind: "object", okType: "boolean", schemaValid: true,
			okPresent: schemaTestPointer(true), okTrue: schemaTestPointer(true), extra: schemaTestPointer(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			valid := validator.Validate(json.RawMessage(tc.content)) == nil
			if valid != tc.schemaValid {
				t.Fatal("unexpected result from the unchanged schema validator")
			}
			check := diagnoseProbeSchema(model.Response{Content: tc.content}, valid)
			want := &probeSchemaCheck{
				ContentPresent: tc.content != "", JSONValid: tc.kind != "invalid", JSONType: tc.kind,
				SchemaValid: valid, OKPresent: tc.okPresent, OKType: tc.okType, OKIsTrue: tc.okTrue,
				ExtraPropertyCount: tc.extra,
			}
			if !reflect.DeepEqual(check, want) {
				t.Fatalf("fixed schema facts differ: got=%+v want=%+v", check, want)
			}
			raw, err := json.Marshal(preflightEvent{SchemaCheck: check})
			if err != nil || bytes.Contains(raw, []byte("private-schema-fixture-key")) {
				t.Fatal("schema diagnosis retained an untrusted property name or value")
			}
			var wire struct {
				Check map[string]json.RawMessage `json:"schemaCheck"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal("schema diagnosis did not encode")
			}
			_, hasOK := wire.Check["okPresent"]
			_, hasTrue := wire.Check["okIsTrue"]
			_, hasExtra := wire.Check["extraPropertyCount"]
			if hasOK != (tc.kind == "object") || hasExtra != hasOK || hasTrue != (tc.okType == "boolean") {
				t.Fatal("schema diagnosis invented facts for an inapplicable field")
			}
		})
	}
}

func TestBaselineSchemaCheckKeepsValidatorDecisionAndCountsOnly(t *testing.T) {
	const secret = "private-schema-fixture-key"
	response := model.Response{
		Content: `{"ok":true}`, Continue: true, ResponseID: secret,
		ToolCalls:       []tools.Call{{ID: secret}, {ID: secret}},
		HostedToolCalls: []model.HostedToolCall{{ID: secret, ToolKey: secret}},
		Artifacts:       []model.ArtifactRef{{ID: secret, Name: secret}},
		Citations:       []string{secret},
	}
	check := diagnoseProbeSchema(response, false)
	if check.SchemaValid || !check.JSONValid || check.ToolCallCount != 2 ||
		check.HostedToolCallCount != 1 || check.ArtifactCount != 1 || !check.Continue {
		t.Fatal("diagnostic parsing replaced the validator decision or lost response facts")
	}
	raw, err := json.Marshal(check)
	if err != nil || bytes.Contains(raw, []byte(secret)) {
		t.Fatal("response metadata escaped the count-only projection")
	}
}
