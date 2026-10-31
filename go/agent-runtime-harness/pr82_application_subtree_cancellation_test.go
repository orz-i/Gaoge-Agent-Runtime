package harness

import (
	"context"
	"errors"
	"testing"
)

type auditIntermediateUnknownExecutor struct{}

func (auditIntermediateUnknownExecutor) ExecuteApplicationCapability(context.Context, ApplicationCapabilityRequest) (ApplicationCapabilityResult, error) {
	return ApplicationCapabilityResult{}, errors.New("lost external result")
}
func TestAuditPR82TerminalApplicationIntermediateMustNotHideUnknownChild(t *testing.T) {
	f, parent, parentItem, _ := newPR82NestedApplicationParent(t, InvocationRunning)
	f.runner.applications = auditIntermediateUnknownExecutor{}
	_, err := f.runner.StartApplicationInvocation(t.Context(), f.turn.ID, ApplicationInvocationRequest{ParentItemID: parentItem, RequestID: "unknown-grandchild", Goal: "unknown external effect", CapabilityKey: "app.grandchild", DefinitionVersion: "1"})
	if !errors.Is(err, ErrApplicationOutcomeUnknown) {
		t.Fatalf("setup child: %v", err)
	}
	_, err = f.runner.completeApplicationInvocation(t.Context(), f.turn, parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.runner.CancelInvocation(t.Context(), parent.ID, "stop descendants")
	if !errors.Is(err, ErrApplicationOutcomeUnknown) {
		t.Fatalf("P1: intermediate Application cancellation hid running descendant: %v", err)
	}
}
