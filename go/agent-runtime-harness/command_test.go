package harness_test

import (
	"encoding/json"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// These names, command IDs and input shapes are host test fixtures, not
// built-in SDK slash commands or application-level routing defaults.
func fixtureCapabilityCommands() []harness.CommandDescriptor {
	return []harness.CommandDescriptor{
		{
			ID: "team", CapabilityKey: harness.CapabilityTeam,
			DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionTeam,
			Source:      harness.CommandSourceFirstParty,
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
		{
			ID: "workflow", CapabilityKey: harness.CapabilityWorkflow,
			DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionWorkflow,
			Source:      harness.CommandSourceFirstParty,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"definitionReference":{"type":"object","required":["id"],"properties":{"id":{"type":"string","minLength":1},"revision":{"type":"integer","minimum":1},"hash":{"type":"string","minLength":1}},"additionalProperties":false},"input":{}},"additionalProperties":false}`),
		},
	}
}

func TestCommandCatalogFreezesExplicitCapabilityContracts(t *testing.T) {
	t.Parallel()
	input := fixtureCapabilityCommands()
	catalog, err := harness.NewCommandCatalog(input[1], input[0])
	if err != nil {
		t.Fatal(err)
	}
	values := catalog.List()
	if len(values) != 2 || values[0].ID != "team" || values[1].ID != "workflow" ||
		values[1].CapabilityKey != harness.CapabilityWorkflow || values[1].ExecutionClass != harness.ExecutionWorkflow {
		t.Fatalf("contracts=%#v", values)
	}
	if !strings.Contains(string(values[1].InputSchema), `"definitionReference"`) {
		t.Fatalf("workflow fixture schema lost: %s", values[1].InputSchema)
	}
	input[1].InputSchema[0] = 'x'
	values[0].InputSchema[0] = 'x'
	reloaded := catalog.List()
	if reloaded[0].InputSchema[0] != '{' || reloaded[1].InputSchema[0] != '{' {
		t.Fatalf("catalog aliased caller or returned schema: %#v", reloaded)
	}
	for _, value := range reloaded {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{`"trigger"`, `"title"`, `"description"`} {
			if strings.Contains(string(encoded), key) {
				t.Fatalf("host presentation leaked into execution contract: %s", encoded)
			}
		}
	}
}

func TestCommandDescriptorJSONRejectsHostPresentationAndUnknownFields(t *testing.T) {
	t.Parallel()
	original, err := json.Marshal(fixtureCapabilityCommands()[0])
	if err != nil {
		t.Fatal(err)
	}
	var valid harness.CommandDescriptor
	if err := json.Unmarshal(original, &valid); err != nil || valid.ID != "team" {
		t.Fatalf("valid descriptor rejected: %#v, %v", valid, err)
	}
	for _, forbidden := range []string{"trigger", "title", "description", "extra"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(original, &fields); err != nil {
			t.Fatal(err)
		}
		fields[forbidden] = json.RawMessage(`"legacy"`)
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &valid); err == nil {
			t.Fatalf("legacy or unknown command field %q silently accepted", forbidden)
		}
		configJSON := `{"commands":[` + string(encoded) + `]}`
		var config harness.ConfigSnapshot
		if err := json.Unmarshal([]byte(configJSON), &config); err == nil {
			t.Fatalf("legacy command field %q in persisted config silently accepted", forbidden)
		}
	}
}

func TestCommandCatalogRejectsDuplicatesAndUnsupportedExecution(t *testing.T) {
	t.Parallel()
	values := fixtureCapabilityCommands()
	duplicate := values[0]
	duplicate.ID = values[1].ID
	if _, err := harness.NewCommandCatalog(values[1], duplicate); err == nil {
		t.Fatal("duplicate capability ID accepted")
	}
	for _, test := range []struct {
		name string
		edit func(*harness.CommandDescriptor)
	}{
		{"team with workflow class", func(v *harness.CommandDescriptor) { v.ExecutionClass = harness.ExecutionWorkflow }},
		{"agent implicit route", func(v *harness.CommandDescriptor) { v.ExecutionClass = harness.ExecutionAgent }},
		{"group chat implicit route", func(v *harness.CommandDescriptor) { v.ExecutionClass = harness.ExecutionGroupChat }},
		{"first-party app kind", func(v *harness.CommandDescriptor) { v.ApplicationKind = "host" }},
		{"application without kind", func(v *harness.CommandDescriptor) {
			v.Source = harness.CommandSourceApplication
			v.ExecutionClass = harness.ExecutionApplication
		}},
		{"application with team class", func(v *harness.CommandDescriptor) {
			v.Source = harness.CommandSourceApplication
			v.ApplicationKind = "host"
		}},
		{"unknown source", func(v *harness.CommandDescriptor) { v.Source = "automatic" }},
		{"blank identity", func(v *harness.CommandDescriptor) { v.ID = "  " }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := values[0]
			test.edit(&value)
			if _, err := harness.NewCommandCatalog(value); err == nil {
				t.Fatalf("unsupported child execution admitted: %#v", value)
			}
		})
	}
	application := harness.CommandDescriptor{
		ID: "host.app", CapabilityKey: "host.app", DefinitionVersion: "v1",
		ExecutionClass: harness.ExecutionApplication, Source: harness.CommandSourceApplication,
		ApplicationKind: "host", InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	if _, err := harness.NewCommandCatalog(application); err != nil {
		t.Fatalf("explicit application capability rejected: %v", err)
	}
}
