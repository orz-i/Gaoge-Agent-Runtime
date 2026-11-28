package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunEA1ChildAgentRecoveryUnknownSuite covers AC-Q P2 only. Actual child
// Model/Tool send precedes uncertain receipt; reconciliation never sends.
func RunEA1ChildAgentRecoveryUnknownSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	ea1AgentUnknownReceipts(t, factory, true)
}

func ea1ChildRecoveryParent(t *testing.T, rt *kernel.Runtime) kernel.Snapshot {
	t.Helper()
	p, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original parent", State: json.RawMessage(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	return p
}

// RunEA1ChildAgentRecoveryAdmissionSuite covers AC-Q P0. The API accepts only
// runID, not caller actor, route, attempt or expected revision.
func RunEA1ChildAgentRecoveryAdmissionSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	ea1ChildRecoveryAdmission(t, factory)
}
func ea1ChildRecoveryAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	backing, reopen := factory(t)
	cut := errors.New("original child Model receipt CAS failed")
	fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: backing.(kernel.PhysicalExecutionStore), event: "agent.model_invocation.completed", injected: cut}
	rt := ea1StartRuntime(t, fault)
	parent := ea1ChildRecoveryParent(t, rt)
	physical := &ea1StartSettlementPhysical{rt: rt}
	var originalRequest agent.StartRequest
	makeRunner := func(runtime *kernel.Runtime) *agent.Runner {
		r, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, RunExecution: &agent.RunExecution{OwnerID: "original-owner", LeaseDuration: 3 * time.Minute}, StartControlPreflight: func(ctx context.Context, req agent.StartRequest) error {
			if !reflect.DeepEqual(req, originalRequest) {
				return agent.ErrInvalidRequest
			}
			p, e := runtime.Load(ctx, req.ParentRunID)
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(p, parent) {
				return agent.ErrInvalidRequest
			}
			return nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	runner := makeRunner(rt)
	request := agent.StartRequest{ID: "original-child", ParentRunID: parent.Run.ID, RequestID: "effect", OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"original":true}`)), Actor: parent.Run.Actor, Thread: parent.Run.Thread, Goal: "original Host callback", Model: "original-model"}
	originalRequest = request
	// PrepareRun creates isolated admission inputs without dispatching a Model.
	setup, setupErr := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: physical, RunExecution: &agent.RunExecution{OwnerID: "setup-owner", LeaseDuration: 3 * time.Minute}})
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	rootReq := request
	rootReq.ID = "root"
	rootReq.ParentRunID = ""
	rootReq.OriginFingerprint = ""
	root, e := setup.PrepareRun(t.Context(), rootReq)
	if e != nil {
		t.Fatal(e)
	}
	cleanReq := request
	cleanReq.ID = "clean-child"
	cleanReq.RequestID = "clean-effect"
	clean, e := setup.PrepareRun(t.Context(), cleanReq)
	if e != nil {
		t.Fatal(e)
	}
	wrong, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "foreign-kind", ParentRunID: parent.Run.ID, Kind: "foreign", Actor: request.Actor, Thread: request.Thread, Goal: "invalid kind", State: json.RawMessage(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	corrupt, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "corrupt", ParentRunID: parent.Run.ID, Kind: agent.RunKind, Actor: request.Actor, Thread: request.Thread, Goal: "corrupt state", State: json.RawMessage(`{"modelInvocations":"invalid"}`)})
	if e != nil {
		t.Fatal(e)
	}
	_, e = runner.StartRun(t.Context(), request)
	if !errors.Is(e, cut) || !errors.Is(e, kernel.ErrChildExecutionBusy) || agent.IsNativeStartNoEffect(e) || fault.failures != 1 || len(physical.requests) != 1 {
		t.Fatalf("actual child Model receipt unknown: %v", e)
	}
	original, e := rt.Load(t.Context(), request.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !runner.VerifyFrozenStartRequest(original, request) {
		t.Fatal("original frozen source missing")
	}
	grant, e := rt.LookupChildExecution(t.Context(), request.ID)
	if e != nil || grant.ChildRunID != request.ID || grant.ExpectedRevision >= original.Run.Revision {
		t.Fatalf("original scope: %v", e)
	}
	fresh := reopen()
	observer := ea1StartRuntime(t, fresh)
	owner := makeRunner(observer)
	assertOriginal := func() {
		for _, want := range []kernel.Snapshot{parent, root, clean, wrong, corrupt, original} {
			ea1CompBarrierUnchanged(t, observer, want)
		}
		same, err := observer.LookupChildExecution(t.Context(), request.ID)
		if err != nil || same != grant {
			t.Fatal("original grant changed")
		}
		if len(physical.requests) != 1 || len(physical.toolRequests) != 0 || fault.failures != 1 {
			t.Fatal("recovery replayed callback")
		}
		if !owner.VerifyFrozenStartRequest(original, request) {
			t.Fatal("fresh runner lost source")
		}
	}
	assertOriginal()
	{
		var nilRunner *agent.Runner
		if e = nilRunner.ReconcileChildPhysicalGrant(context.Background(), request.ID); !errors.Is(e, agent.ErrInvalidRequest) {
			t.Fatalf("nil: %v", e)
		}
		assertOriginal()
		for _, tc := range []struct {
			id   string
			want error
		}{{"", kernel.ErrInvalidInput}, {"missing", kernel.ErrNotFound}, {wrong.Run.ID, agent.ErrInvalidRequest}, {root.Run.ID, agent.ErrInvalidRequest}, {corrupt.Run.ID, kernel.ErrChildExecutionBusy}, {clean.Run.ID, kernel.ErrNotFound}} {
			if e = owner.ReconcileChildPhysicalGrant(t.Context(), tc.id); !errors.Is(e, tc.want) {
				t.Fatalf("input %q: %v want %v", tc.id, e, tc.want)
			}
			assertOriginal()
		}
	}
	for i := 0; i < 2; i++ {
		if e = owner.ReconcileChildPhysicalGrant(t.Context(), request.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unknown Host callback retired: %v", e)
		}
		assertOriginal()
	}
}
