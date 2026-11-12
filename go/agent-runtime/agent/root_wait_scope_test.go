package agent_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

func TestRootPendingSignalRetainsOriginalScopeUntilVerifiedReceipt(t *testing.T) {
	fixture := newExternalFixture(t)
	waiting := fixture.start(t)
	original, err := fixture.runtime.LookupChildExecution(t.Context(), waiting.Run.ID)
	if err != nil || !original.IsOwned() {
		t.Fatalf("original scope = %#v, %v", original, err)
	}
	bad := externalSignal()
	bad.OperationID = "foreign-operation"
	if _, err = fixture.runner.NotifyExternalToolResultReady(t.Context(), bad); !errors.Is(err, agent.ErrToolSignalConflict) {
		t.Fatalf("foreign signal = %v", err)
	}
	ready, err := fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := fixture.runner.NotifyExternalToolResultReady(t.Context(), externalSignal())
	if err != nil || duplicate.Run.Revision != ready.Run.Revision {
		t.Fatalf("duplicate = %#v, %v", duplicate.Run, err)
	}
	retained, err := fixture.runtime.LookupChildExecution(t.Context(), waiting.Run.ID)
	if err != nil || retained != original {
		t.Fatalf("ready changed physical authority: %#v, %v", retained, err)
	}
	view, err := agent.ViewState(ready)
	if err != nil || view.PendingTool == nil {
		t.Fatalf("pending = %#v, %v", view, err)
	}
	pending, err := fixture.runner.QueryPendingTool(t.Context(), ready.Run.ID, "external-call")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := fixture.runner.ResumeToolWait(t.Context(), ready.Run.ID, ready.Run.Revision, pending.Generation+1)
	if err != nil || stale.Run.Revision != ready.Run.Revision || fixture.executions != 1 || fixture.lookups != 0 {
		t.Fatalf("stale generation = %#v, %v", stale.Run, err)
	}
	// Readiness is not a provider receipt and cannot retire the grant.
	if err = fixture.runner.ReconcileRootPhysicalExecution(t.Context(), ready.Run.ID); !errors.Is(err, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("ready-only settlement = %v", err)
	}
	retained, err = fixture.runtime.LookupChildExecution(t.Context(), waiting.Run.ID)
	if err != nil || retained != original {
		t.Fatalf("stale delivery changed scope: %#v, %v", retained, err)
	}
}
