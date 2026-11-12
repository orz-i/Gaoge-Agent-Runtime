package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"testing"
	"time"
)

// RunEA1ChildAgentRecoveryUnknownSuite covers AC-Q P2 only. Actual child
// Host/Model/Tool send precedes uncertain receipt; reconciliation never sends.
func RunEA1ChildAgentRecoveryUnknownSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	t.Run("host_middleware", func(t *testing.T) { ea1ChildRecoveryHost(t, factory, false) })
	for _, kind := range []string{"model", "tool"} {
		t.Run(kind, func(t *testing.T) {
			backing, reopen := factory(t)
			native, ok := backing.(kernel.ExecutionStore)
			if !ok {
				t.Fatal("missing native ExecutionStore")
			}
			event := "agent.model_invocation.completed"
			if kind == "tool" {
				event = "tool.completed"
			}
			injected := errors.New("test-only physical response received but original receipt not committed")
			fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: native,
				ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), event: event, injected: injected}
			rt, err := kernel.New(kernel.Dependencies{Store: fault})
			if err != nil {
				t.Fatal(err)
			}
			physical := &ea1UnknownAgentPhysical{tool: kind == "tool", rt: rt}
			catalog, err := tools.NewRegistry([]tools.Registration{{
				Definition: tools.Definition{Key: "effect", Name: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler:    tools.HandlerFunc(physical.execute),
			}})
			if err != nil {
				t.Fatal(err)
			}
			makeRunner := func(runtime *kernel.Runtime, owner string, offset time.Duration) *agent.Runner {
				r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog,
					Clock: ea1UnknownAgentClock{offset: offset}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second},
					Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}})
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			first := makeRunner(rt, "first-owner", 0)
			parent := ea1ChildRecoveryParent(t, rt)
			request := agent.StartRequest{ID: "unknown-child", ParentRunID: parent.Run.ID, RequestID: "original-child-effect", OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child"}`)), Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original physical attempt"}
			if kind == "tool" {
				request.ToolKeys = []string{"effect"}
			}
			_, firstErr := first.StartRun(t.Context(), request)
			if !errors.Is(firstErr, injected) || fault.failures != 1 {
				t.Fatalf("receipt failure not reached: %v failures=%d", firstErr, fault.failures)
			}
			pending, err := rt.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(pending)
			if err != nil || len(view.ModelInvocations) != 1 || pending.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("original pending source missing: %v", err)
			}
			calls, toolCalls := physical.counts()
			if calls != 1 || (kind == "tool" && toolCalls != 1) || (kind == "model" && toolCalls != 0) {
				t.Fatalf("first physical counts model=%d tool=%d", calls, toolCalls)
			}
			invocation := view.ModelInvocations[0]
			if invocation.ID == "" || invocation.RunID != request.ID || invocation.RequestHash == "" || invocation.ExecutionAttempt != 1 {
				t.Fatal("original invocation/attempt missing")
			}
			if kind == "model" && invocation.Status != agent.ModelInvocationPending {
				t.Fatal("uncommitted Model receipt fabricated")
			}
			if kind == "tool" {
				var state struct {
					ToolDispatchID string `json:"toolDispatchID"`
				}
				if e := json.Unmarshal(pending.State, &state); e != nil || state.ToolDispatchID != "original-tool-call" {
					t.Fatalf("original Tool dispatch marker missing: %v", e)
				}
				if invocation.Status != agent.ModelInvocationConsumed || invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.RequestHash == "" ||
					len(invocation.Response.ToolCalls) != 1 || invocation.Response.ToolCalls[0].ID != "original-tool-call" ||
					invocation.Response.ToolCalls[0].ToolKey != "effect" || !bytes.Equal(invocation.Response.ToolCalls[0].Arguments, json.RawMessage(`{"source":"original"}`)) {
					t.Fatal("Tool fixture lost exact consumed Model receipt")
				}
			}
			physical.assertOriginal(t, request, invocation)
			originalJSON, err := json.Marshal(pending)
			if err != nil {
				t.Fatal(err)
			}
			// Await real Store expiry, not Agent clock advancement or private SQL.
			lease := fault.lastLease()
			if lease.RunID != request.ID || lease.OwnerID != "first-owner" {
				t.Fatal("fault did not observe native original lease")
			}
			delay := time.Until(lease.ExpiresAt.Add(150 * time.Millisecond))
			if delay > 5*time.Second {
				t.Fatalf("unexpected native lease duration: %s", delay)
			}
			if delay > 0 {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
			}
			if err = native.CheckExecution(t.Context(), request.ID, &lease); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("old native owner still valid after real expiry: %v", err)
			}
			grant, err := rt.LookupChildExecution(t.Context(), request.ID)
			if err != nil || grant.ChildRunID != request.ID {
				t.Fatalf("original child grant: %v", err)
			}
			fresh := reopen()
			ownerRT, err := kernel.New(kernel.Dependencies{Store: fresh})
			if err != nil {
				t.Fatal(err)
			}
			durable, err := ownerRT.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			durableJSON, err := json.Marshal(durable)
			if err != nil || !bytes.Equal(originalJSON, durableJSON) {
				t.Fatal("original pending source changed across reopen")
			}
			physical.rt = ownerRT
			next := makeRunner(ownerRT, "fresh-owner", 3*time.Minute)
			resumeErr := next.ReconcileChildPhysicalGrant(t.Context(), request.ID)
			if !errors.Is(resumeErr, kernel.ErrChildExecutionBusy) {
				t.Fatalf("unknown child reconciliation: %v", resumeErr)
			}
			ea1CompBarrierUnchanged(t, ownerRT, parent)
			same, lookupErr := ownerRT.LookupChildExecution(t.Context(), request.ID)
			if lookupErr != nil || same != grant {
				t.Fatal("unknown recovery changed original grant")
			}
			afterCalls, afterTools := physical.counts()
			// No provider idempotency or affirmative no-effect proof exists here.
			// Reusing InvocationID is not permission for another physical send.
			if afterCalls != calls || afterTools != toolCalls {
				t.Errorf("EA-06 UNKNOWN replay: model %d->%d tool %d->%d resumeErr=%v originalInvocation=%s attempt=%d", calls, afterCalls, toolCalls, afterTools, resumeErr, invocation.ID, invocation.ExecutionAttempt)
			}
			if kind == "tool" && !errors.Is(resumeErr, kernel.ErrChildExecutionBusy) {
				t.Errorf("Tool unknown must remain typed quarantine: %v", resumeErr)
			}
			// Model has no established ModelUnknown error contract. Assert the safety
			// property instead of inventing an error or accepting TTL as no-effect.
			final, err := ownerRT.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			finalJSON, err := json.Marshal(final)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(originalJSON, finalJSON) {
				t.Errorf("UNKNOWN recovery changed original full Run/invocation/attempt state; resumeErr=%v", resumeErr)
			}
		})
	}
}

func ea1ChildRecoveryParent(t *testing.T, rt *kernel.Runtime) kernel.Snapshot {
	t.Helper()
	p, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "workflow", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original parent", State: json.RawMessage(`{}`)})
	if e != nil {
		t.Fatal(e)
	}
	return p
}

// RunEA1ChildAgentRecoveryAdmissionSuite covers AC-Q P0. The API accepts only
// runID, not caller actor, route, attempt or expected revision.
func RunEA1ChildAgentRecoveryAdmissionSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	ea1ChildRecoveryHost(t, factory, true)
}
func ea1ChildRecoveryHost(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store), admission bool) {
	backing, reopen := factory(t)
	rt := ea1StartRuntime(t, backing)
	parent := ea1ChildRecoveryParent(t, rt)
	physical := &ea1AdmissionModel{}
	host := &ea1UnknownHostMiddleware{failure: errors.New("original child Host sent, no receipt")}
	makeRunner := func(runtime *kernel.Runtime) *agent.Runner { return ea1AdmissionRunner(t, runtime, physical, host) }
	runner := makeRunner(rt)
	request := agent.StartRequest{ID: "original-child", ParentRunID: parent.Run.ID, RequestID: "effect", OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"original":true}`)), Actor: parent.Run.Actor, Thread: parent.Run.Thread, Goal: "original Host callback", Model: "original-model"}
	// Prepare isolated invalid inputs before the original active scope.
	rootReq := request
	rootReq.ID = "root"
	rootReq.ParentRunID = ""
	rootReq.OriginFingerprint = ""
	root, e := runner.PrepareRun(t.Context(), rootReq)
	if e != nil {
		t.Fatal(e)
	}
	cleanReq := request
	cleanReq.ID = "clean-child"
	cleanReq.RequestID = "clean-effect"
	clean, e := runner.PrepareRun(t.Context(), cleanReq)
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
	if !errors.Is(e, host.failure) || !errors.Is(e, kernel.ErrChildExecutionBusy) || agent.IsNativeStartNoEffect(e) || host.count() != 1 || physical.calls.Load() != 0 {
		t.Fatalf("actual child Host unknown: %v", e)
	}
	original, e := rt.Load(t.Context(), request.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !runner.VerifyFrozenStartRequest(original, request) {
		t.Fatal("original frozen source missing")
	}
	inv := host.first()
	if inv.Operation != plugin.RunStart || inv.RunID != request.ID || inv.ParentRunID != parent.Run.ID || inv.Actor != request.Actor || inv.Thread != request.Thread {
		t.Fatal("Host invocation source mismatch")
	}
	grant, e := rt.LookupChildExecution(t.Context(), request.ID)
	if e != nil || grant.ChildRunID != request.ID || grant.ExpectedRevision != original.Run.Revision {
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
		if host.count() != 1 || physical.calls.Load() != 0 {
			t.Fatal("recovery replayed callback")
		}
		if !owner.VerifyFrozenStartRequest(original, request) {
			t.Fatal("fresh runner lost source")
		}
	}
	assertOriginal()
	if admission {
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
