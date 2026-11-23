package harness

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A hard cut of the former generic category must not change an already-issued
// Agent ID or admit a Host-selected non-Agent namespace.
func TestAgentInvocationIDRetainsFixedDomainAndRejectsUnknownSource(t *testing.T) {
	for _, tc := range []struct{ name, turn, parent, request string }{
		{"root", "turn-1", "", "host-request"},
		{"child", "turn-1", "hid_delegate", "child-request"},
		{"another parent", "turn-1", "hid_other", "child-request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := AgentInvocationID(tc.turn, tc.parent, tc.request)
			// This is the ORIGINAL pre-cut stable ID layout. The domain
			// separator is internal, never passed by a Host caller.
			legacyAgentID := stableID("hiv", tc.turn, tc.parent, "runtime.agent", tc.request)
			if err != nil || actual != legacyAgentID || AgentExecutionRefID(actual) != stableID("hr", legacyAgentID) {
				t.Fatalf("original Agent ID/physical Run drift: id=%q expected=%q err=%v", actual, legacyAgentID, err)
			}
			if other := stableID("hiv", tc.turn, tc.parent, "app.legacy", tc.request); other == actual {
				t.Fatal("legacy namespace was promoted to Agent ID")
			}
		})
	}
	for _, tc := range []struct{ turn, parent, request string }{
		{"", "", "request"}, {"turn", "", ""}, {"  ", "p", "r"},
	} {
		if _, err := AgentInvocationID(tc.turn, tc.parent, tc.request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("empty Agent origin accepted: %+v err=%v", tc, err)
		}
	}
	for _, field := range []string{"CapabilityKey", "DefinitionVersion", "ExecutionClass"} {
		if _, ok := reflect.TypeOf(Invocation{}).FieldByName(field); ok {
			t.Fatalf("retired generic execution selector %s was reintroduced", field)
		}
	}
	original := Invocation{ID: "hiv-a", TurnID: "turn-a", Status: InvocationRunning, Attempt: 1, Revision: 1}
	raw, err := json.Marshal(original)
	if err != nil || json.Valid(raw) == false {
		t.Fatal(err)
	}
	for _, field := range []string{`"capabilityKey"`, `"definitionVersion"`, `"executionClass"`} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("retired selector %s persisted in Agent Invocation: %s", field, raw)
		}
	}
}
