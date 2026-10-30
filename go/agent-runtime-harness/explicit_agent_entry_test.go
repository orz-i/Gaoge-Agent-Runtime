package harness_test

import (
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// An implicit default Agent route cannot be added back to the public Harness API.
func TestExplicitAgentStartDoesNotExposeLegacyGenericStart(t *testing.T) {
	api := reflect.TypeOf((*harness.Runner)(nil))
	if _, present := api.MethodByName("Start"); present {
		t.Fatal("generic Runner.Start reintroduced an implicit Agent selector")
	}
	if _, present := api.MethodByName("StartAgentTurn"); !present {
		t.Fatal("explicit Agent Turn API unavailable")
	}
}
