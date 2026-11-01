package harness_test

import (
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// An implicit default Agent route cannot be added back to the public Harness API.
func TestExplicitAgentStartDoesNotExposeLegacyGenericStart(t *testing.T) {
	api := reflect.TypeOf((*harness.Runner)(nil))
	for _, prohibited := range []string{
		"Start", "StartDirect", "StartDirectAgent", "PrepareIntent", "IntentRouter", "RouteIntent", "SelectMode",
	} {
		if _, present := api.MethodByName(prohibited); present {
			t.Fatalf("Host-only intent/mode operation %s reintroduced into Harness", prohibited)
		}
	}
	for _, selected := range []string{
		"StartAgentTurn", "StartTeamTurn", "StartGroupChatTurn", "StartWorkflowTurn", "StartApplicationTurn",
	} {
		if _, present := api.MethodByName(selected); !present {
			t.Fatalf("Host-selected capability entry %s unavailable", selected)
		}
	}
	// A request is an immutable execution input, not a task-classification
	// prompt or a second Direct/Agent execution mode.
	for _, config := range []reflect.Type{reflect.TypeOf(harness.StartRequest{}), reflect.TypeOf(harness.ConfigSnapshot{})} {
		for _, prohibited := range []string{"IntentRouter", "PrepareIntent", "DirectMode", "Direct", "Mode", "Intent"} {
			if _, present := config.FieldByName(prohibited); present {
				t.Fatalf("Host-only selection field %s leaked into %s", prohibited, config)
			}
		}
	}
}
