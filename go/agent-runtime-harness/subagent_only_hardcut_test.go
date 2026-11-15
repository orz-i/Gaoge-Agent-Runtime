package harness

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// A pre-stable composite Invocation must not become executable through an
// old deserializer, a command alias, or the generic physical control path.
func TestRemovedCompositeExecutionClassesAreNotAdmitted(t *testing.T) {
	for _, name := range []ExecutionClass{"team", "group_chat", "workflow"} {
		if validExecutionClass(name) {
			t.Fatalf("removed execution class %q accepted", name)
		}
		descriptor := CommandDescriptor{
			ID: "obsolete", CapabilityKey: "runtime." + string(name), DefinitionVersion: "v1",
			ExecutionClass: name, Source: "first_party", InputSchema: json.RawMessage(`{"type":"object"}`),
		}
		if _, err := NewCommandCatalog(descriptor); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("removed command %q accepted: %v", name, err)
		}
	}
	api := reflect.TypeOf((*Runner)(nil))
	for _, method := range []string{"StartTeamTurn", "StartGroupChatTurn", "StartWorkflowTurn", "StartTeamInvocation", "StartWorkflowInvocation"} {
		if _, ok := api.MethodByName(method); ok {
			t.Fatalf("removed method %s reintroduced", method)
		}
	}
	deps := reflect.TypeOf(Dependencies{})
	for _, field := range []string{"Teams", "GroupChats", "Workflows"} {
		if _, ok := deps.FieldByName(field); ok {
			t.Fatalf("removed dependency %s reintroduced", field)
		}
	}
}
