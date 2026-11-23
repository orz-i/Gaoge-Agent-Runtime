package harness

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestDispatchReceiptRequiresOriginalRecognizedPhaseAndBoundary(t *testing.T) {
	grant := Item{ID: "grant-phase", TurnID: "turn-A", InvocationID: "owner-A", RunID: "run-A", Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: ExecutionDispatchParentItemID, Seq: 5, Payload: json.RawMessage(`{"phase":"start","physicalRunID":"run-A"}`)}
	receipt := Item{ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID, InvocationID: grant.InvocationID, RunID: grant.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID, Seq: 6, Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)}
	for _, tc := range []struct {
		name     string
		phase    string
		boundary string
		want     bool
	}{{"valid-start", "start", "physical_dispatch_returned", true}, {"retired-feature-retry", "start-retry", "feature_start_no_effect", false}, {"valid-control", "resolve-subtask-approval", "native_agent_control_no_effect", true}, {"valid-handoff", "handoff-start", "handoff_start_returned", true}, {"valid-native-recovery", "start", "native_kernel_epoch_recovered", true}, {"unknown-stage", "unrecognized", "physical_dispatch_returned", false}, {"blank-stage", "", "physical_dispatch_returned", false}, {"control-no-effect-on-start", "start", "native_agent_control_no_effect", false}, {"start-no-effect-on-resume", "resume", "native_agent_start_no_effect", false}, {"handoff-completed-by-control", "grant-call-allowance", "handoff_start_returned", false}, {"handoff-completed-as-kernel-snapshot", "handoff-start", "physical_dispatch_returned", false}, {"native-recovery-on-resume", "resume", "native_kernel_epoch_recovered", false}} {
		t.Run(tc.name, func(t *testing.T) {
			g := grant
			r := receipt
			g.Payload = json.RawMessage(`{"phase":"` + tc.phase + `","physicalRunID":"` + g.RunID + `"}`)
			r.Payload = json.RawMessage(`{"boundary":"` + tc.boundary + `"}`)
			_, got := VerifiedExecutionDispatchReceipt(g, []Item{r})
			if got != tc.want {
				t.Fatalf("phase=%q boundary=%q verified=%t expected=%t", tc.phase, tc.boundary, got, tc.want)
			}
		})
	}
}
func TestPR82OwnedRunlessItemCannotGrantNewPhysicalAttempt(t *testing.T) {
	runner := &Runner{}
	owner := childAttemptBinding{InvocationID: "root", ExecutionRefID: "root-attempt-two", Attempt: 2}
	for _, kind := range []ItemKind{ItemTool, ItemArtifact, ItemInteraction, ItemContext, ItemDiagnostic, ItemInvocation} {
		item := Item{ID: "old-" + string(kind), InvocationID: owner.InvocationID, Kind: kind}
		if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); !errors.Is(err, ErrConflict) {
			t.Fatalf("owner-stamped %s without RunID crossed Attempt: %v", kind, err)
		}
		item.InvocationID = ""
		if err := runner.requireItemOwnerAttempt(t.Context(), item, owner); err != nil {
			t.Fatalf("unowned Host fact %s rejected: %v", kind, err)
		}
	}
}
