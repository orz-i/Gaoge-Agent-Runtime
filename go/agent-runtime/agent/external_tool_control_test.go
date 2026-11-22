package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type externalCancellationOwner struct {
	original agent.ExternalToolControl
	receipt  string
	started  bool
	reads    int
}

func (owner *externalCancellationOwner) VerifyExternalToolNoEffect(_ context.Context, request tools.ExecutionRequest, control agent.ExternalToolControl) (agent.ExternalToolNoEffectReceipt, error) {
	owner.reads++
	if owner.started || owner.receipt == "" || owner.original != control || request.RunID != control.RunID || request.Call.ID != control.CallID ||
		request.Call.ToolKey != "test.external" || string(request.Call.Arguments) != "{}" {
		return agent.ExternalToolNoEffectReceipt{}, agent.ErrToolOutcomeUnknown
	}
	return agent.ExternalToolNoEffectReceipt{ExternalToolControl: control, ReceiptID: owner.receipt}, nil
}

func newControlWait(t *testing.T) (*externalFixture, *externalCancellationOwner, kernel.Snapshot) {
	t.Helper()
	f := newExternalFixture(t)
	owner := &externalCancellationOwner{receipt: "browser-cancel-at-prepared"}
	f.controlOwner = owner
	f.restart(t, f.store)
	wait := f.start(t)
	owner.original = agent.ExternalToolControl{Actor: wait.Run.Actor, RunID: wait.Run.ID, CallID: "external-call",
		OperationID: "external-operation", InputDigest: strings.Repeat("a", 64), ControlID: "original-cancel"}
	return f, owner, wait
}

func TestExternalNoEffectRequiresTheOriginalWaitAndReadOnlyOwner(t *testing.T) {
	f, owner, wait := newControlWait(t)
	ctx := t.Context()
	g, err := f.runtime.LookupChildExecution(ctx, wait.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*agent.ExternalToolControl){
		func(c *agent.ExternalToolControl) { c.Actor.ActorID = "foreign" },
		func(c *agent.ExternalToolControl) { c.ParentRunID = "foreign" },
		func(c *agent.ExternalToolControl) { c.RunID = "foreign" },
		func(c *agent.ExternalToolControl) { c.CallID = "foreign" },
		func(c *agent.ExternalToolControl) { c.OperationID = "foreign" },
		func(c *agent.ExternalToolControl) { c.InputDigest = strings.Repeat("b", 64) },
		func(c *agent.ExternalToolControl) { c.ControlID = "foreign" },
	} {
		foreign := owner.original
		change(&foreign)
		if _, err = f.runner.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, foreign); err == nil {
			t.Fatalf("accepted %+v", foreign)
		}
	}
	if err = f.runner.VerifyExternalToolControlWait(ctx, wait.Run.Revision+1, owner.original); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	owner.started = true
	if _, err = f.runner.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, owner.original); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("started: %v", err)
	}
	owner.started = false
	owner.receipt = ""
	if _, err = f.runner.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, owner.original); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("missing: %v", err)
	}
	if current, err := f.runtime.LookupChildExecution(ctx, wait.Run.ID); current != g || err != nil {
		t.Fatalf("lost original grant %+v %v", current, err)
	}
	now, err := f.runtime.Load(ctx, wait.Run.ID)
	if err != nil || now.Run.Revision != wait.Run.Revision || f.model.calls != 1 || f.executions != 1 {
		t.Fatalf("unexpected effects: %+v, %v", now.Run, err)
	}
}

func TestExternalNoEffectSurvivesLostResponseAndParksWorker(t *testing.T) {
	f, owner, wait := newControlWait(t)
	ctx := t.Context()
	if err := f.runner.VerifyExternalToolControlWait(ctx, wait.Run.Revision, owner.original); err != nil {
		t.Fatal(err)
	}
	if owner.reads != 0 {
		t.Fatal("native preflight invoked external owner")
	}
	s, err := f.runner.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, owner.original)
	if err != nil {
		t.Fatal(err)
	}
	if s.Run.Status != kernel.RunStatusRunning || s.Run.Revision != wait.Run.Revision+1 || s.EventHead != wait.EventHead {
		t.Fatalf("control effect %+v", s.Run)
	}
	f.restart(t, f.store)
	duplicate, err := f.runner.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, owner.original)
	if err != nil || duplicate.Run.Revision != s.Run.Revision {
		t.Fatalf("lost return %+v %v", duplicate.Run, err)
	}
	owner.receipt = "different"
	if _, err = f.runner.ReconcileExternalToolNoEffectForControl(ctx, s.Run.Revision, owner.original); !errors.Is(err, agent.ErrToolReceiptMismatch) {
		t.Fatalf("swapped receipt: %v", err)
	}
	owner.receipt = "browser-cancel-at-prepared"
	parked, err := f.runner.Resume(ctx, s.Run.ID, s.Run.Revision)
	if err != nil || parked.Run.Revision != s.Run.Revision {
		t.Fatalf("parked %+v %v", parked.Run, err)
	}
	view, err := agent.ViewState(s)
	if err != nil || view.PendingTool != nil || view.Budget.Usage.ToolCalls != 0 || f.model.calls != 1 || f.executions != 1 || f.lookups != 0 {
		t.Fatalf("unexpected execution %+v %v", view, err)
	}
	if _, err = f.runtime.Cancel(ctx, s.Run.ID, s.Run.Revision, "original control"); err != nil {
		t.Fatal(err)
	}
}

type withoutExternalReturn struct{ *memory.Store }

func (s withoutExternalReturn) RecordOwnedPhysicalCallbackReturn(context.Context, kernel.ChildExecutionGrant, uint64, kernel.OwnedPhysicalCallbackOutcome, kernel.OwnedPhysicalCallbackReturn) error {
	return errors.New("original callback return was not persisted")
}

func TestExternalNoEffectRequiresDurableOriginalCallbackReturn(t *testing.T) {
	f := newExternalFixture(t)
	owner := &externalCancellationOwner{receipt: "valid-host-withdrawal"}
	f.controlOwner = owner
	f.restart(t, withoutExternalReturn{f.store})
	s, err := f.runner.StartRun(t.Context(), startRequest("external-run", "external-request", "compute", "test.external"))
	if err == nil {
		t.Fatal("missing callback was acknowledged")
	}
	owner.original = agent.ExternalToolControl{Actor: s.Run.Actor, RunID: s.Run.ID, CallID: "external-call", OperationID: "external-operation", InputDigest: strings.Repeat("a", 64), ControlID: "original-control"}
	if _, err = f.runner.ReconcileExternalToolNoEffectForControl(t.Context(), s.Run.Revision, owner.original); !errors.Is(err, agent.ErrRootOutcomeUnknown) || owner.reads != 0 {
		t.Fatalf("missing callback: reads=%d err=%v", owner.reads, err)
	}
}
