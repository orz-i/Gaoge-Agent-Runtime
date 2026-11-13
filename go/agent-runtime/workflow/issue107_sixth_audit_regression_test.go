package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// This Store wrapper models a separate trusted worker that advances the same
// child Run revision after Resume's snapshot Load but before physical Grant
// admission. It does not mutate an actual effect receipt or call a Tool.
type auditRevisionAdvanceStore struct {
	*memory.Store
	runtime    *kernel.Runtime
	armed      bool
	advanced   bool
	advanceErr error
	onBegin    func(context.Context, string) error
}

func (s *auditRevisionAdvanceStore) BeginChildExecution(ctx context.Context, grant kernel.ChildExecutionGrant) error {
	if s.armed {
		s.armed = false
		if s.onBegin != nil {
			s.advanceErr = s.onBegin(ctx, grant.ChildRunID)
		} else {
			snapshot, err := s.runtime.Load(ctx, grant.ChildRunID)
			if err != nil {
				s.advanceErr = err
				return err
			}
			_, s.advanceErr = s.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
				Status: snapshot.Run.Status, State: snapshot.State, Checkpoint: snapshot.Checkpoint,
			})
		}
		s.advanced = s.advanceErr == nil
		if s.advanceErr != nil {
			return s.advanceErr
		}
	}
	return s.Store.BeginChildExecution(ctx, grant)
}

type auditRevisionEffect struct{ calls []string }

func (e *auditRevisionEffect) Execute(_ context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
	e.calls = append(e.calls, request.Kind)
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "receipt_" + request.EffectID, Output: json.RawMessage(`{"ok":true}`),
	}, nil
}

// Resume's initial expectedRevision check succeeds. Another worker then
// commits a new Run revision BEFORE BeginChildExecution, but Begin only sees
// current status Running. Workflow interprets its stale pre-Grant pending
// Effect and sends the external physical request. Its later Kernel CAS fails,
// which is too late to recall that physical request.
func TestAuditIssue107WorkflowResumeStaleRevisionDispatchesEffect(t *testing.T) {
	store := &auditRevisionAdvanceStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	store.runtime = rt
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "parent", Kind: workflow.RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "parent", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	effects := &auditRevisionEffect{}
	runner := newWorkflowRunner(t, rt, effects)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("first", "first.effect"),
		effectNode("second", "second.effect"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{MaxActivationsPerSegment: 8})
	request := workflowRequest(definition)
	request.ID = "stale-revision-child"
	request.ParentRunID = parent.Run.ID
	request.Actor = parent.Run.Actor
	request.Thread = parent.Run.Thread
	yielded, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, workflow.ErrSegmentYielded) || len(effects.calls) != 1 {
		t.Fatalf("expected one completed Effect and next one scheduled: err=%v status=%s calls=%v", err, yielded.Run.Status, effects.calls)
	}
	if _, err = rt.LookupChildExecution(t.Context(), yielded.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("yielded segment not physically quiescent: %v", err)
	}
	store.armed = true
	resumed, resumeErr := runner.Resume(t.Context(), yielded.Run.ID, yielded.Run.Revision)
	t.Logf("interveningRevisionAdvanced=%v secondEffectDispatched=%v resumeStatus=%s resumeErr=%v", store.advanced, len(effects.calls) > 1, resumed.Run.Status, resumeErr)
	if !store.advanced || store.advanceErr != nil {
		t.Fatalf("fixture did not create intended concurrent revision: %v", store.advanceErr)
	}
	if len(effects.calls) > 1 {
		t.Fatal("stale pre-Grant Workflow snapshot dispatched an external Effect after another worker committed a newer Run revision")
	}
}

// Unlike a partial external Effect, a stale Wait checkpoint is pure.
// If another worker increments the waiting child's revision between Load
// and Begin, the current implementation admits Grant then fails its first
// CAS. The callback has issued NO effect, but withChildPhysicalGrant
// retains an UNKNOWN Grant indefinitely because the CAS error is not a
// trusted no-effect proof. The parent can no longer cancel a waiting child.
func TestAuditIssue107StaleWaitCASPinsNoEffectAncestorGrant(t *testing.T) {
	store := &auditRevisionAdvanceStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	store.runtime = rt
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: "wait-parent", Kind: workflow.RunKind, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "wait parent", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	effects := &auditRevisionEffect{}
	runner, runnerErr := workflow.NewRunner(workflow.Dependencies{
		Runtime: rt, Effects: effects, DeferResumption: true,
	})
	if runnerErr != nil {
		t.Fatal(runnerErr)
	}
	definition := compileWorkflow(t, []workflow.Node{
		{ID: "wait", Type: workflow.NodeWait, Wait: &workflow.WaitNode{Kind: "approval", Payload: json.RawMessage(`{"required":true}`)}},
		effectNode("external", "tool.call"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{})
	request := workflowRequest(definition)
	request.ID = "stale-wait-child"
	request.ParentRunID = parent.Run.ID
	request.Actor = parent.Run.Actor
	request.Thread = parent.Run.Thread
	waiting, err := runner.StartRun(t.Context(), request)
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("waiting fixture=%+v err=%v", waiting.Run, err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), waiting.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("quiescent Wait still holds physical Grant: %v", err)
	}
	store.onBegin = func(ctx context.Context, childID string) error {
		// This is a real second Workflow continuation, not a synthetic
		// malformed Feature state. It legally resolves the original Wait
		// to Running at revision R+1 and returns without any Tool effects.
		_, innerErr := runner.ResolveWait(ctx, childID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`))
		return innerErr
	}
	store.armed = true
	_, waitErr := runner.ResolveWait(t.Context(), waiting.Run.ID, waiting.Run.Revision, json.RawMessage(`{"approved":true}`))
	original, lookupErr := rt.LookupChildExecution(t.Context(), waiting.Run.ID)
	_, cancelErr := rt.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "cancel after stale nonphysical CAS")
	t.Logf("concurrentWaitRevisionAdvanced=%v ResumeErr=%v PhysicalEffectCalls=%v GrantStillLive=%v ParentCancelErr=%v",
		store.advanced, waitErr, effects.calls, lookupErr == nil, cancelErr)
	if !store.advanced || store.advanceErr != nil {
		t.Fatalf("fixture did not bump original wait revision: %v", store.advanceErr)
	}
	if len(effects.calls) == 0 && lookupErr == nil && errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
		t.Fatalf("pure stale Wait CAS created permanently unknown ancestor Grant with no external operation: %+v", original)
	}
}
