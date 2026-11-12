package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// The public Workflow.Cancel invokes beginFailure/runCompensations directly.
// Unlike Start/Resume/ResolveWait it never obtains withChildPhysicalGrant.
// A custom undo Effect can therefore overlap a successful Kernel terminal
// CAS of the SAME Workflow; neither the original per-Revision Grant nor
// physical side-effect unknown-outcome policy is enforced.
type auditUnfencedCompensation struct {
	entered, release chan struct{}
	dispatched       atomic.Int32
}

func (e *auditUnfencedCompensation) Execute(ctx context.Context, r workflow.EffectRequest) (workflow.EffectResult, error) {
	if r.Kind == "resource.release" {
		e.dispatched.Add(1)
		e.entered <- struct{}{}
		select {
		case <-e.release:
		case <-ctx.Done():
			return workflow.EffectResult{}, ctx.Err()
		}
	}
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "receipt_" + r.EffectID, Output: json.RawMessage(`{"ok":true}`),
	}, nil
}
func TestAuditIssue107CancelCompensationBypassesRootPhysicalGrant(t *testing.T) {
	rt := newWorkflowRuntime(t)
	external := &auditUnfencedCompensation{entered: make(chan struct{}, 1), release: make(chan struct{})}
	runner := newWorkflowRunner(t, rt, external)
	definition := compileAdvancedWorkflow(t, []workflow.Node{
		compensationNode("reserve", "resource.reserve", "resource.release"),
		{ID: "approval", Type: workflow.NodeWait, Wait: &workflow.WaitNode{
			Kind: "human.approval", Payload: json.RawMessage(`{"required":true}`),
		}},
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.DefinitionPolicy{MaxCostUnits: 2, CostClass: workflow.CostLow, SideEffectClass: workflow.SideEffectWrite})
	req := workflowRequest(definition)
	req.ID = "cancel-root-physical"
	waiting, err := runner.StartRun(t.Context(), req)
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("fixture did not persist a quiescent Root Wait: status=%s err=%v", waiting.Run.Status, err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), waiting.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("ordinary waiting Root still has physical Grant: %v", err)
	}
	cancelDone := make(chan error, 1)
	go func() {
		_, cancelErr := runner.Cancel(context.Background(), waiting.Run.ID, waiting.Run.Revision, "end workflow and undo")
		cancelDone <- cancelErr
	}()
	select {
	case <-external.entered:
	case <-time.After(4 * time.Second):
		close(external.release)
		t.Fatal("Workflow.Cancel did not begin its registered compensating external Effect")
	}
	during, err := rt.Load(t.Context(), waiting.Run.ID)
	if err != nil {
		close(external.release)
		t.Fatal(err)
	}
	originalGrant, grantErr := rt.LookupChildExecution(t.Context(), waiting.Run.ID)
	terminal, terminalErr := rt.Cancel(t.Context(), waiting.Run.ID, during.Run.Revision,
		"independent Kernel terminal CAS while undo Effect in flight")
	t.Logf("CancelCompensationEffectEntered=true grantPresent=%t grantErr=%v kernelTerminalErr=%v "+
		"kernelStatus=%s undoCalls=%d", grantErr == nil, grantErr, terminalErr, terminal.Run.Status, external.dispatched.Load())
	close(external.release)
	select {
	case <-cancelDone:
	case <-time.After(4 * time.Second):
		t.Fatal("first Workflow.Cancel never returned")
	}
	if terminalErr == nil && terminal.Run.Status == kernel.RunStatusCancelled &&
		grantErr != nil && external.dispatched.Load() == 1 {
		t.Fatal("Workflow.Cancel dispatched an unfenced external compensation; a competing Kernel terminal CAS committed while that effect was in flight")
	}
	_ = originalGrant
}
