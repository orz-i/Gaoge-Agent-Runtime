package agent_test

import (
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

// Host intent selection is not an Agent SDK capability. Preserve the
// execution-oriented PrepareRun/PrepareModelRequest paths while excluding
// the old conceptual Direct/PrepareIntent mode taxonomy entirely.
func TestAgentAPIHasNoHostIntentOrDirectMode(t *testing.T) {
	t.Parallel()
	api := reflect.TypeOf((*agent.Runner)(nil))
	for _, forbidden := range []string{
		"StartDirect", "StartDirectAgent", "PrepareIntent", "RouteIntent", "IntentRouter", "SelectMode",
	} {
		if _, found := api.MethodByName(forbidden); found {
			t.Fatalf("Host intent operation %s leaked into Agent Runner", forbidden)
		}
	}
	for _, method := range []string{"StartRun", "PrepareRun", "Resume"} {
		if _, found := api.MethodByName(method); !found {
			t.Fatalf("execution operation %s missing from Agent Runner", method)
		}
	}
	for _, value := range []reflect.Type{
		reflect.TypeOf(agent.Dependencies{}), reflect.TypeOf(agent.StartRequest{}),
	} {
		for _, forbidden := range []string{"Direct", "DirectMode", "Intent", "IntentRouter", "PrepareIntent"} {
			if _, found := value.FieldByName(forbidden); found {
				t.Fatalf("Host task-selection field %s leaked into %s", forbidden, value)
			}
		}
	}
	if _, found := reflect.TypeOf(agent.Dependencies{}).FieldByName("PrepareModelRequest"); !found {
		t.Fatal("durable ModelInvocation lost its pre-commit model-visible request projection")
	}
}
