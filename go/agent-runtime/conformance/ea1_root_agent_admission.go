package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// RunEA1RootAgentAdmission covers ONLY native AR-S P0/P1. Native RunExecution
// is explicit; no legacy runner or fabricated Workflow grant supplies evidence.
func RunEA1RootAgentAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	t.Run("P0/origin-and-claim", func(t *testing.T) {
		backing, peer := factory(t)
		wrapped := ea1AgentAdmissionWrap(t, backing)
		rt := ea1StartRuntime(t, wrapped)
		observer := ea1StartRuntime(t, peer)
		m := &ea1AdmissionModel{}
		middleware := &ea1AdmissionMiddleware{}
		runner := ea1AdmissionRunner(t, rt, m, middleware)
		req := ea1AdmissionRequest()
		for _, vector := range []string{"missing-id", "empty-goal", "child-origin-on-root"} {
			bad := req
			bad.ID = "invalid-" + vector
			switch vector {
			case "missing-id":
				bad.ID = ""
			case "empty-goal":
				bad.Goal = ""
			case "child-origin-on-root":
				bad.OriginFingerprint = "foreign-origin"
			}
			_, e := runner.StartRun(t.Context(), bad)
			if !errors.Is(e, agent.ErrInvalidRequest) {
				t.Fatalf("invalid %s: %v", vector, e)
			}
			if bad.ID != "" {
				ea1StartAbsent(t, rt, bad.ID)
			}
			if m.calls.Load() != 0 || middleware.calls.Load() != 0 {
				t.Fatal("preflight entered arbitrary callback")
			}
		}
		var competitor kernel.ExecutionLease
		wrapped.afterCreate = func(ctx context.Context, s kernel.Snapshot) error {
			var e error
			competitor, e = observer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: s.Run.ID, OwnerID: "competing-owner", LeaseDuration: 3 * time.Minute})
			return e
		}
		created, e := runner.StartRun(t.Context(), req)
		if !errors.Is(e, kernel.ErrExecutionConflict) || created.Run.ID != req.ID || m.calls.Load() != 0 {
			t.Fatalf("claim conflict concealed created Root or dispatched: %v %+v", e, created.Run)
		}
		if middleware.calls.Load() != 0 {
			t.Errorf("EA01/P0: failed original owner admission entered effect-capable Host middleware %d times", middleware.calls.Load())
		}
		persisted, e := observer.Load(t.Context(), req.ID)
		if e != nil || !runner.VerifyFrozenStartRequest(persisted, req) {
			t.Fatalf("original immutable source missing: %v", e)
		}
		if e = peer.(kernel.ExecutionStore).CheckExecution(t.Context(), req.ID, &competitor); e != nil {
			t.Fatalf("failed Start revoked independent owner: %v", e)
		}
		beforeMiddleware := middleware.calls.Load()
		changes := []struct {
			name   string
			change func(*agent.StartRequest)
		}{
			{"same", func(*agent.StartRequest) {}},
			{"tenant", func(r *agent.StartRequest) { r.Actor.TenantID = "foreign" }},
			{"actor", func(r *agent.StartRequest) { r.Actor.ActorID = "foreign" }},
			{"thread", func(r *agent.StartRequest) { r.Thread.ID = "foreign" }},
			{"origin", func(r *agent.StartRequest) { r.RequestID = "foreign-effect" }},
			{"model", func(r *agent.StartRequest) { r.Model = "foreign-model" }},
			{"options", func(r *agent.StartRequest) { r.ModelOptions = json.RawMessage(`{"temperature":0.9}`) }},
			{"limits", func(r *agent.StartRequest) { r.Limits.MaxLLMCalls = 8 }},
			{"goal", func(r *agent.StartRequest) { r.Goal = "foreign goal" }},
		}
		for _, c := range changes {
			duplicate := req
			c.change(&duplicate)
			_, e = runner.StartRun(t.Context(), duplicate)
			if !errors.Is(e, kernel.ErrAlreadyExists) {
				t.Fatalf("duplicate %s: %v", c.name, e)
			}
			ea1CompBarrierUnchanged(t, observer, persisted)
			if c.name != "same" && runner.VerifyFrozenStartRequest(persisted, duplicate) {
				t.Fatalf("forged %s accepted as original source", c.name)
			}
			if m.calls.Load() != 0 || middleware.calls.Load() != beforeMiddleware {
				t.Fatal("duplicate entered callbacks")
			}
			if e = peer.(kernel.ExecutionStore).CheckExecution(t.Context(), req.ID, &competitor); e != nil {
				t.Fatalf("duplicate changed independent owner: %v", e)
			}
		}
		if e = observer.ReleaseExecution(t.Context(), competitor); e != nil {
			t.Fatal(e)
		}
		finished, e := runner.Resume(t.Context(), req.ID, persisted.Run.Revision)
		if e != nil || finished.Run.Status != kernel.RunStatusCompleted || m.calls.Load() != 1 {
			t.Fatalf("explicit recovery: %v calls=%d", e, m.calls.Load())
		}
		if !runner.VerifyFrozenStartRequest(finished, req) {
			t.Fatal("recovery changed immutable source")
		}
	})
	for _, phase := range []string{"middleware-before-physical", "acquired-before-model"} {
		t.Run("P1/"+phase, func(t *testing.T) {
			backing, peer := factory(t)
			wrapped := ea1AgentAdmissionWrap(t, backing)
			rt := ea1StartRuntime(t, wrapped)
			observer := ea1StartRuntime(t, peer)
			req := ea1AdmissionRequest()
			m := &ea1AdmissionModel{}
			middleware := &ea1AdmissionMiddleware{}
			runner := ea1AdmissionRunner(t, rt, m, middleware)
			stop := errors.New("test-only stop after authoritative P1 observation")
			observed := false
			check := func(ctx context.Context) error {
				observed = true
				before, e := observer.Load(ctx, req.ID)
				if e != nil {
					return e
				}
				if !runner.VerifyFrozenStartRequest(before, req) {
					t.Error("P1 original frozen source missing")
				}
				// Accept either a verified native lease or a real Store physical grant;
				// existence of a Run alone is not authority for arbitrary Host effects.
				lease := wrapped.acquired
				leaseOK := lease.RunID == req.ID && lease.OwnerID == "original-owner" && lease.LeaseID != "" && peer.(kernel.ExecutionStore).CheckExecution(ctx, req.ID, &lease) == nil
				grant, grantErr := observer.LookupChildExecution(ctx, req.ID)
				grantOK := grantErr == nil && grant.ChildRunID == req.ID && grant.ExpectedRevision == before.Run.Revision
				if !leaseOK && !grantOK {
					t.Errorf("EA03/G01: arbitrary Root callback reached before authoritative owner: lease=%+v grantErr=%v", lease, grantErr)
				}
				if m.calls.Load() != 0 {
					t.Error("P1 already dispatched Model")
				}
				_, cancelErr := observer.Cancel(ctx, req.ID, before.Run.Revision, "competing control during physical owner")
				if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) && !errors.Is(cancelErr, kernel.ErrExecutionConflict) {
					t.Errorf("EA03/G01: competing Cancel did not reject: %v", cancelErr)
				}
				after, loadErr := observer.Load(ctx, req.ID)
				if loadErr != nil || !reflect.DeepEqual(after, before) {
					t.Error("competing control changed full original snapshot")
				}
				if leaseOK {
					if e = peer.(kernel.ExecutionStore).CheckExecution(ctx, req.ID, &lease); e != nil {
						t.Errorf("competing Cancel revoked original owner: %v", e)
					}
				}
				if grantOK {
					same, e := observer.LookupChildExecution(ctx, req.ID)
					if e != nil || same != grant {
						t.Error("competing Cancel changed original physical grant")
					}
				}
				return stop
			}
			if phase == "middleware-before-physical" {
				middleware.before = check
			} else {
				wrapped.afterAcquire = func(ctx context.Context, _ kernel.ExecutionLease) error { return check(ctx) }
			}
			_, e := runner.StartRun(t.Context(), req)
			if !observed || !errors.Is(e, stop) {
				t.Fatalf("actual Start did not reach exact P1 cutpoint: observed=%t err=%v", observed, e)
			}
			if m.calls.Load() != 0 {
				t.Fatal("P1 denial unexpectedly dispatched Model")
			}
		})
	}
}

func ea1AdmissionRequest() agent.StartRequest {
	return agent.StartRequest{ID: "native-original", RequestID: "original-effect", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original bounded task", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
}
func ea1AdmissionRunner(t *testing.T, rt *kernel.Runtime, m model.Client, mw plugin.RunMiddleware) *agent.Runner {
	t.Helper()
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, RunExecution: &agent.RunExecution{OwnerID: "original-owner", LeaseDuration: 3 * time.Minute}, RunMiddleware: []plugin.RunMiddleware{mw}})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

type ea1AdmissionModel struct{ calls atomic.Int32 }

func (m *ea1AdmissionModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls.Add(1)
	return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

type ea1AdmissionMiddleware struct {
	calls  atomic.Int32
	before func(context.Context) error
}

func (m *ea1AdmissionMiddleware) Name() string { return "ea1.original.root.admission" }
func (m *ea1AdmissionMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	m.calls.Add(1)
	if m.before != nil {
		if e := m.before(ctx); e != nil {
			return kernel.Snapshot{}, e
		}
	}
	return next(ctx)
}

type ea1AgentAdmissionStore struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.ExecutionQuarantineStore
	acquired     kernel.ExecutionLease
	afterCreate  func(context.Context, kernel.Snapshot) error
	afterAcquire func(context.Context, kernel.ExecutionLease) error
}

func ea1AgentAdmissionWrap(t *testing.T, s kernel.Store) *ea1AgentAdmissionStore {
	t.Helper()
	execution, ok := s.(kernel.ExecutionStore)
	if !ok {
		t.Fatal("missing authoritative ExecutionStore")
	}
	control, ok := s.(kernel.ControlStore)
	if !ok {
		t.Fatal("missing actual ControlStore")
	}
	physical, ok := s.(kernel.ChildExecutionStore)
	if !ok {
		t.Fatal("missing physical Store observation")
	}
	quarantine, ok := s.(kernel.ExecutionQuarantineStore)
	if !ok {
		t.Fatal("missing quarantine Store")
	}
	return &ea1AgentAdmissionStore{Store: s, ExecutionStore: execution, ControlStore: control, ChildExecutionStore: physical, ExecutionQuarantineStore: quarantine}
}
func (s *ea1AgentAdmissionStore) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	v, e := s.Store.Create(ctx, r, events)
	if e == nil && s.afterCreate != nil {
		e = s.afterCreate(ctx, v)
	}
	return v, e
}
func (s *ea1AgentAdmissionStore) AcquireExecution(ctx context.Context, r kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	l, e := s.ExecutionStore.AcquireExecution(ctx, r)
	if e != nil {
		return l, e
	}
	s.acquired = l
	if s.afterAcquire != nil {
		e = s.afterAcquire(ctx, l)
	}
	return l, e
}
