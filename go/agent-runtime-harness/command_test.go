package harness_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestFirstPartyCommandCatalogIsStaticAndDeterministic(t *testing.T) {
	t.Parallel()
	catalog, err := harness.NewCommandCatalog(harness.FirstPartyCommandDescriptors()...)
	if err != nil {
		t.Fatal(err)
	}
	values := catalog.List()
	if len(values) != 3 || values[0].ID != "plan" || values[1].ID != "team" || values[2].ID != "workflow" {
		t.Fatalf("commands=%#v", values)
	}
	workflow, err := catalog.Resolve("workflow")
	if err != nil || workflow.Trigger != "/workflow" || workflow.CapabilityKey != harness.CapabilityWorkflow ||
		workflow.ExecutionClass != harness.ExecutionWorkflow {
		t.Fatalf("workflow=%#v err=%v", workflow, err)
	}
}

func TestCommandCatalogRejectsDuplicateIDsOrTriggers(t *testing.T) {
	t.Parallel()
	values := harness.FirstPartyCommandDescriptors()
	duplicate := values[0]
	duplicate.ID = values[1].ID
	if _, err := harness.NewCommandCatalog(values[1], duplicate); err == nil {
		t.Fatal("duplicate command ID accepted")
	}
	duplicate = values[0]
	duplicate.Trigger = values[1].Trigger
	if _, err := harness.NewCommandCatalog(values[1], duplicate); err == nil {
		t.Fatal("duplicate command trigger accepted")
	}
}
