package harness

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestCapabilityArgumentsRejectInvalidTypesBeforeMaterialization(t *testing.T) {
	for _, test := range []struct {
		name      string
		commandID string
		arguments string
		path      string
	}{
		{"string revision", "workflow", `{"definitionReference":{"id":"canvas.example","revision":"1"}}`, "/definitionReference/revision"},
		{"fractional revision", "workflow", `{"definitionReference":{"id":"canvas.example","revision":1.5}}`, "/definitionReference/revision"},
		{"zero revision", "workflow", `{"definitionReference":{"id":"canvas.example","revision":0}}`, "/definitionReference/revision"},
		{"numeric hash", "workflow", `{"definitionReference":{"id":"canvas.example","revision":1,"hash":123}}`, "/definitionReference/hash"},
		{"unknown workflow field", "workflow", `{"definitionReference":{"id":"canvas.example"},"unexpected":true}`, "/"},
		{"team arguments", "team", `{"definitionReference":{"id":"canvas.example"}}`, "/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(capabilityInvocationToolInput{CommandID: test.commandID, Goal: "fixture", Arguments: json.RawMessage(test.arguments)})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = decodeCapabilityToolInput(raw, FirstPartyCommandDescriptors())
			code, message, recoverable := tools.RecoverableCallErrorInfo(err)
			if !recoverable || code != "tool.arguments_schema" || !strings.Contains(message, test.path) {
				t.Fatalf("invalid argument correction = %q, %q, %v", code, message, err)
			}
		})
	}
}

func TestCapabilityArgumentsPreserveValidJSONAndAuthorization(t *testing.T) {
	for _, arguments := range []string{`{}`, `{"definitionReference":{"id":"canvas.example","revision":1,"hash":"frozen"},"input":{"code":"001","enabled":false}}`} {
		raw, err := json.Marshal(capabilityInvocationToolInput{CommandID: "workflow", Goal: "fixture", Arguments: json.RawMessage(arguments)})
		if err != nil {
			t.Fatal(err)
		}
		input, _, err := decodeCapabilityToolInput(raw, FirstPartyCommandDescriptors())
		if err != nil || string(input.Arguments) != arguments {
			t.Fatalf("valid arguments changed: %s, %v", input.Arguments, err)
		}
	}
	_, _, err := decodeCapabilityToolInput(json.RawMessage(`{"commandID":"unauthorized","goal":"fixture","arguments":{"revision":"1"}}`), FirstPartyCommandDescriptors())
	code, _, recoverable := tools.RecoverableCallErrorInfo(err)
	if !recoverable || code != "capability.not_authorized" {
		t.Fatalf("authorization error = %v", err)
	}
	commands := FirstPartyCommandDescriptors()
	commands[1].InputSchema = json.RawMessage(`{"type":"invalid"}`)
	_, _, err = decodeCapabilityToolInput(json.RawMessage(`{"commandID":"workflow","goal":"fixture"}`), commands)
	if _, _, recoverable = tools.RecoverableCallErrorInfo(err); recoverable || !errors.Is(err, tools.ErrInvalidDefinition) {
		t.Fatalf("invalid frozen schema must remain fatal: %v", err)
	}
}

func TestCapabilitySchemaExposesNestedTypesAndRetainsCommandConstraints(t *testing.T) {
	schema, err := capabilityInvocationToolSchema(FirstPartyCommandDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type       string `json:"type"`
			Properties map[string]struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err = json.Unmarshal(schema, &root); err != nil {
		t.Fatal(err)
	}
	if root.Type != "object" || root.Properties["arguments"].Type != "object" || root.Properties["arguments"].Properties["definitionReference"].Properties["revision"].Type != "integer" {
		t.Fatalf("nested type declaration is hidden: %s", schema)
	}
	for _, test := range []struct {
		arguments string
		valid     bool
	}{
		{`{"commandID":"workflow","goal":"fixture","arguments":{"definitionReference":{"id":"canvas.example","revision":1}}}`, true},
		{`{"commandID":"workflow","goal":"fixture","arguments":{"definitionReference":{"id":"canvas.example","revision":"1"}}}`, false},
		{`{"commandID":"team","goal":"fixture","arguments":{}}`, true},
		{`{"commandID":"team","goal":"fixture","arguments":{"definitionReference":{"id":"canvas.example","revision":1}}}`, false},
		{`{"commandID":"unauthorized","goal":"fixture","arguments":{}}`, false},
	} {
		err = tools.ValidateCall(tools.Definition{Key: CapabilityInvocationToolKey, Name: "invoke_capability", InputSchema: schema}, tools.Call{ID: "fixture", ToolKey: CapabilityInvocationToolKey, Arguments: json.RawMessage(test.arguments)})
		if (err == nil) != test.valid {
			t.Fatalf("command contract changed for %s: %v", test.arguments, err)
		}
	}
}

func TestCapabilitySchemaPreservesOpenCommandArguments(t *testing.T) {
	for _, inputSchema := range []string{`{"type":"object"}`, `true`, `{"type":"object","patternProperties":{"^definition":{}},"additionalProperties":false}`} {
		commands := append(FirstPartyCommandDescriptors(), CommandDescriptor{ID: "application", InputSchema: json.RawMessage(inputSchema)})
		schema, err := capabilityInvocationToolSchema(commands)
		if err != nil {
			t.Fatal(err)
		}
		err = tools.ValidateCall(tools.Definition{Key: CapabilityInvocationToolKey, Name: "invoke_capability", InputSchema: schema}, tools.Call{
			ID: "fixture", ToolKey: CapabilityInvocationToolKey,
			Arguments: json.RawMessage(`{"commandID":"application","goal":"fixture","arguments":{"definitionReference":"application-owned-string"}}`),
		})
		if err != nil {
			t.Fatalf("open application schema narrowed by workflow projection: %v", err)
		}
	}
}
