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

// RunEA1RootAgentAdmission covers explicit native AR-S P0/P1.
// Default configuration uses the same native authority, never a fallback.
func RunEA1RootAgentAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	runEA1RootAgentAdmission(t, factory, false)
}

// RunEA1RootAgentDefaultAdmission verifies the public nil configuration uses
// the same persistent authority without requiring new caller configuration.
func RunEA1RootAgentDefaultAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store)) {
	runEA1RootAgentAdmission(t, factory, true)
}
func runEA1RootAgentAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store), automatic bool) {
	ea1RootBoundedContext(t, factory, automatic)
	ea1RootBoundedTool(t, factory, automatic)

	if automatic {
		t.Run("P0/default-generated-id", func(t *testing.T) {
			backing, peer := factory(t)
			wrapped := ea1AgentAdmissionWrap(t, backing)
			rt := ea1StartRuntime(t, wrapped)
			observer := ea1StartRuntime(t, peer)
			m := &ea1AdmissionModel{}
			runner := ea1AdmissionConfiguredRunner(t, rt, m, nil, true)
			req := ea1AdmissionRequest()
			req.ID = ""
			var createdID string
			wrapped.afterCreate = func(_ context.Context, s kernel.Snapshot) error { createdID = s.Run.ID; return nil }
			wrapped.afterOwned = func(ctx context.Context, _ kernel.ChildExecutionGrant) error {
				g, e := observer.LookupChildExecution(ctx, createdID)
				if e != nil || g.ChildRunID != createdID || g.ExecutionLeaseID != wrapped.acquired.LeaseID {
					t.Fatalf("automatic ID was not durably admitted: %v", e)
				}
				return nil
			}
			result, e := runner.StartRun(t.Context(), req)
			if e != nil || result.Run.ID == "" || result.Run.ID != createdID || result.Run.Status != kernel.RunStatusCompleted || m.calls.Load() != 1 {
				t.Fatalf("default automatic ID path: %v", e)
			}
			req.ID = createdID
			if !runner.VerifyFrozenStartRequest(result, req) {
				t.Fatal("automatic ID changed original source")
			}
			ea1CompBarrierUnchanged(t, observer, result)
		})
	}

	t.Run("P0/origin-and-claim", func(t *testing.T) {
		backing, peer := factory(t)
		wrapped := ea1AgentAdmissionWrap(t, backing)
		rt := ea1StartRuntime(t, wrapped)
		observer := ea1StartRuntime(t, peer)
		m := &ea1AdmissionModel{}
		runner := ea1AdmissionConfiguredRunner(t, rt, m, nil, automatic)
		req := ea1AdmissionRequest()
		for _, vector := range []string{"missing-id", "empty-goal", "child-origin-on-root"} {
			if automatic && vector == "missing-id" {
				continue
			}
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
			if m.calls.Load() != 0 {
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
		persisted, e := observer.Load(t.Context(), req.ID)
		if e != nil || !runner.VerifyFrozenStartRequest(persisted, req) {
			t.Fatalf("original immutable source missing: %v", e)
		}
		if e = peer.(kernel.ExecutionStore).CheckExecution(t.Context(), req.ID, &competitor); e != nil {
			t.Fatalf("failed Start revoked independent owner: %v", e)
		}
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
			if m.calls.Load() != 0 {
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

	t.Run("P0/control-before-owned-admission", func(t *testing.T) {
		backing, peer := factory(t)
		wrapped := ea1AgentAdmissionWrap(t, backing)
		rt := ea1StartRuntime(t, wrapped)
		observer := ea1StartRuntime(t, peer)
		m := &ea1AdmissionModel{}
		runner := ea1AdmissionConfiguredRunner(t, rt, m, nil, automatic)
		req := ea1AdmissionRequest()
		var winner kernel.Snapshot
		wrapped.beforeOwned = func(ctx context.Context, grant kernel.ChildExecutionGrant) error {
			before, e := observer.Load(ctx, req.ID)
			if e != nil {
				return e
			}
			winner, e = observer.Cancel(ctx, req.ID, before.Run.Revision, "control wins before physical admission")
			return e
		}
		_, e := runner.StartRun(t.Context(), req)
		if e == nil || winner.Run.Status != kernel.RunStatusCancelled || m.calls.Load() != 0 || wrapped.acquireCalls != 0 {
			t.Fatalf("pre-admission control did not win without callback: %v", e)
		}
		ea1CompBarrierUnchanged(t, observer, winner)
		if !runner.VerifyFrozenStartRequest(winner, req) {
			t.Fatal("pre-admission control changed original source")
		}
		if _, e = observer.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("failed admission left physical scope: %v", e)
		}
	})

	t.Run("P0/original-external-owner-reuse", func(t *testing.T) {
		backing, peer := factory(t)
		rt := ea1StartRuntime(t, backing)
		observer := ea1StartRuntime(t, peer)
		m := &ea1AdmissionModel{}
		setup, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m})
		if e != nil {
			t.Fatal(e)
		}
		req := ea1AdmissionRequest()
		prepared, e := setup.PrepareRun(t.Context(), req)
		if e != nil {
			t.Fatal(e)
		}
		if m.calls.Load() != 0 {
			t.Fatal("Prepare dispatched Model")
		}
		original, e := observer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: req.ID, OwnerID: "external-original", LeaseDuration: 10 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		wrapped := ea1AgentAdmissionWrap(t, backing)
		externalRT, e := ea1StartRuntime(t, wrapped).WithExecution(original)
		if e != nil {
			t.Fatal(e)
		}
		wrapped.afterOwned = func(ctx context.Context, _ kernel.ChildExecutionGrant) error {
			grant, err := observer.LookupChildExecution(ctx, req.ID)
			if err != nil || grant.ExecutionOwnerID != original.OwnerID || grant.ExecutionLeaseID != original.LeaseID || grant.ExecutionEpoch != original.Epoch || grant.AdmissionOperation != string(plugin.RunResume) {
				t.Fatalf("external original owner not bound: %v", err)
			}
			return peer.(kernel.ExecutionStore).CheckExecution(ctx, req.ID, &original)
		}
		owner := ea1AdmissionConfiguredRunner(t, externalRT, m, nil, automatic)
		final, e := owner.Resume(t.Context(), req.ID, prepared.Run.Revision)
		if e != nil || final.Run.Status != kernel.RunStatusCompleted || m.calls.Load() != 1 {
			t.Fatalf("original external owner continuation: %v", e)
		}
		if wrapped.acquireCalls != 0 || wrapped.acquired.RunID != "" || wrapped.releaseCalls != 0 || wrapped.renewCalls != 0 {
			t.Fatal("reacquired or released caller-owned lease")
		}
		if !owner.VerifyFrozenStartRequest(final, req) {
			t.Fatal("owner reuse changed source")
		}
		if _, e = observer.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("successful whole chain left physical scope: %v", e)
		}
		if e = observer.ReleaseExecution(t.Context(), original); e != nil {
			t.Fatalf("caller could not release its original lease: %v", e)
		}
		ea1CompBarrierUnchanged(t, observer, final)
	})

	t.Run("P0/same-external-lease-two-callers", func(t *testing.T) {
		backing, peer := factory(t)
		rt := ea1StartRuntime(t, backing)
		observer := ea1StartRuntime(t, peer)
		m := &ea1AdmissionModel{}
		setup, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m})
		if e != nil {
			t.Fatal(e)
		}
		req := ea1AdmissionRequest()
		prepared, e := setup.PrepareRun(t.Context(), req)
		if e != nil {
			t.Fatal(e)
		}
		lease, e := observer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: req.ID, OwnerID: "shared-external-original", LeaseDuration: 3 * time.Minute})
		if e != nil {
			t.Fatal(e)
		}
		a, b := ea1AgentAdmissionWrap(t, backing), ea1AgentAdmissionWrap(t, peer)
		gate := make(chan struct{})
		release := make(chan struct{})
		modelEntered := make(chan struct{}, 1)
		var ready atomic.Int32
		before := func(ctx context.Context, _ kernel.ChildExecutionGrant) error {
			if ready.Add(1) == 2 {
				close(gate)
			}
			select {
			case <-gate:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		a.beforeOwned = before
		b.beforeOwned = before
		barrier := agent.NewModelAdmissionBarrier(modelEntered, release)
		type outcome struct {
			s kernel.Snapshot
			e error
		}
		done := make(chan outcome, 2)
		for _, store := range []*ea1AgentAdmissionStore{a, b} {
			bound, err := ea1StartRuntime(t, store).WithExecution(lease)
			if err != nil {
				t.Fatal(err)
			}
			owner := ea1AdmissionConfiguredRunner(t, bound, m, barrier, automatic)
			go func() { s, err := owner.Resume(t.Context(), req.ID, prepared.Run.Revision); done <- outcome{s, err} }()
		}
		results := []outcome{}
		select {
		case loser := <-done:
			results = append(results, loser)
			if !errors.Is(loser.e, kernel.ErrChildExecutionBusy) && !errors.Is(loser.e, kernel.ErrExecutionConflict) {
				t.Errorf("competing same-lease caller not rejected: %v", loser.e)
			}
		case <-time.After(5 * time.Second):
			t.Error("same-lease callers failed to elect one scope; possibly both entered Host")
		}
		select {
		case <-modelEntered:
		case <-time.After(5 * time.Second):
			t.Error("elected owner did not enter Host")
		}
		if m.calls.Load() != 0 {
			t.Errorf("before winner release Model=%d", m.calls.Load())
		}
		close(release)
		for len(results) < 2 {
			select {
			case r := <-done:
				results = append(results, r)
			case <-time.After(5 * time.Second):
				t.Fatal("same-lease caller did not return")
			}
		}
		wins := 0
		for _, r := range results {
			if r.e == nil {
				wins++
				if r.s.Run.Status != kernel.RunStatusCompleted {
					t.Fatal("winner incomplete")
				}
				ea1CompBarrierUnchanged(t, observer, r.s)
			}
		}
		if wins != 1 || m.calls.Load() != 1 {
			t.Fatalf("same-lease duplicated original dispatch: wins=%d Model=%d", wins, m.calls.Load())
		}
		if a.acquireCalls+b.acquireCalls+a.releaseCalls+b.releaseCalls+a.renewCalls+b.renewCalls != 0 {
			t.Fatal("same-lease callers changed external ownership")
		}
		if e = observer.ReleaseExecution(t.Context(), lease); e != nil {
			t.Fatal(e)
		}
	})

	for _, phase := range []string{"model-admission-barrier", "owned-admission-before-model"} {
		t.Run("P1/"+phase, func(t *testing.T) {
			backing, peer := factory(t)
			wrapped := ea1AgentAdmissionWrap(t, backing)
			rt := ea1StartRuntime(t, wrapped)
			observer := ea1StartRuntime(t, peer)
			req := ea1AdmissionRequest()
			m := &ea1AdmissionModel{}
			enteredModel, release := make(chan struct{}, 1), make(chan struct{})
			runner := ea1AdmissionConfiguredRunner(t, rt, m, agent.NewModelAdmissionBarrier(enteredModel, release), automatic)
			observed := false
			check := func(ctx context.Context) error {
				entered := phase == "model-admission-barrier"
				observed = true
				before, e := observer.Load(ctx, req.ID)
				if e != nil {
					return e
				}
				if !runner.VerifyFrozenStartRequest(before, req) {
					t.Error("P1 original frozen source missing")
				}
				// Admission preserves the original lease and grant but grants no
				// callback authority until the physical entry is committed.
				lease := wrapped.acquired
				leaseOK := lease.RunID == req.ID && lease.OwnerID != "" && (automatic || lease.OwnerID == "original-owner") && lease.LeaseID != ""
				checkEntry := func() bool {
					err := peer.(kernel.ExecutionStore).CheckExecution(ctx, req.ID, &lease)
					if entered {
						return err == nil
					}
					return errors.Is(err, kernel.ErrExecutionConflict) && errors.Is(err, kernel.ErrChildExecutionBusy)
				}
				grant, grantErr := observer.LookupChildExecution(ctx, req.ID)
				grantOK := grantErr == nil && grant.ChildRunID == req.ID && grant.ExpectedRevision <= before.Run.Revision && grant.ExecutionOwnerID == lease.OwnerID && grant.ExecutionLeaseID == lease.LeaseID && grant.ExecutionEpoch == lease.Epoch && grant.AdmissionSourceHash != "" && grant.AdmissionOperation == string(plugin.RunStart)
				if !leaseOK || !grantOK || !checkEntry() {
					t.Errorf("EA03/G01: arbitrary Root callback reached before authoritative owner: lease=%+v grantErr=%v", lease, grantErr)
				}
				if m.calls.Load() != 0 {
					t.Error("P1 already dispatched Model")
				}

				// Same original native token without the private physical permit
				// must not authorize an independent writer mid-callback.
				fenced, bindErr := observer.WithExecution(lease)
				if bindErr != nil {
					t.Fatal(bindErr)
				}
				for _, writer := range []*kernel.Runtime{observer, fenced} {
					_, writeErr := writer.Apply(ctx, req.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State})
					if !errors.Is(writeErr, kernel.ErrChildExecutionBusy) && !errors.Is(writeErr, kernel.ErrExecutionRequired) && !errors.Is(writeErr, kernel.ErrExecutionConflict) {
						t.Errorf("independent self Apply accepted: %v", writeErr)
					}
					ea1CompBarrierUnchanged(t, observer, before)
				}
				_, takeoverErr := observer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: req.ID, OwnerID: "competing-owner", LeaseDuration: 3 * time.Minute})
				if !errors.Is(takeoverErr, kernel.ErrChildExecutionBusy) && !errors.Is(takeoverErr, kernel.ErrExecutionConflict) {
					t.Errorf("physical scope allowed Acquire: %v", takeoverErr)
				}
				releaseErr := observer.ReleaseExecution(ctx, lease)
				if !errors.Is(releaseErr, kernel.ErrChildExecutionBusy) && !errors.Is(releaseErr, kernel.ErrExecutionConflict) {
					t.Errorf("physical scope allowed Release: %v", releaseErr)
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
					if !checkEntry() {
						t.Error("competing Cancel changed original entry qualification")
					}
				}
				if grantOK {
					same, e := observer.LookupChildExecution(ctx, req.ID)
					if e != nil || same != grant {
						t.Error("competing Cancel changed original physical grant")
					}
				}
				return nil
			}
			if phase == "owned-admission-before-model" {
				wrapped.afterOwned = func(ctx context.Context, _ kernel.ChildExecutionGrant) error {
					return check(ctx)
				}
			}
			done := make(chan error, 1)
			go func() { _, e := runner.StartRun(t.Context(), req); done <- e }()
			select {
			case <-enteredModel:
			case e := <-done:
				t.Fatalf("Start did not reach model admission: %v", e)
			case <-time.After(5 * time.Second):
				t.Fatal("Start model admission timed out")
			}
			if phase == "model-admission-barrier" {
				if e := check(t.Context()); e != nil {
					t.Error(e)
				}
			}
			if m.calls.Load() != 0 {
				t.Fatal("P1 denial unexpectedly dispatched Model")
			}
			close(release)
			if e := <-done; e != nil || !observed || m.calls.Load() != 1 {
				t.Fatalf("original owner did not finish after P1 checks: observed=%t err=%v", observed, e)
			}
		})
	}
}

func ea1AdmissionRequest() agent.StartRequest {
	return agent.StartRequest{ID: "native-original", RequestID: "original-effect", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original bounded task", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
}
func ea1AdmissionConfiguredRunner(t *testing.T, rt *kernel.Runtime, m model.Client, barrier *agent.ModelAdmissionBarrier, automatic bool) *agent.Runner {
	t.Helper()
	var execution *agent.RunExecution
	if !automatic {
		execution = &agent.RunExecution{OwnerID: "original-owner", LeaseDuration: 3 * time.Minute}
	}
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, RunExecution: execution, ModelAdmissionBarrier: barrier})
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

type ea1AgentAdmissionStore struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ControlStore
	kernel.ChildExecutionStore
	kernel.ExecutionQuarantineStore
	kernel.OwnedPhysicalExecutionStore
	acquired     kernel.ExecutionLease
	renewals     ea1LeaseRenewals
	releaseCalls int
	renewCalls   int
	acquireCalls int
	afterCreate  func(context.Context, kernel.Snapshot) error
	afterOwned   func(context.Context, kernel.ChildExecutionGrant) error
	beforeOwned  func(context.Context, kernel.ChildExecutionGrant) error
	afterEntry   func(context.Context) error
	afterAcquire func(context.Context)
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
	owned, ok := s.(kernel.OwnedPhysicalExecutionStore)
	if !ok {
		t.Fatal("missing owned physical Store")
	}
	return &ea1AgentAdmissionStore{OwnedPhysicalExecutionStore: owned, Store: s, ExecutionStore: execution, ControlStore: control, ChildExecutionStore: physical, ExecutionQuarantineStore: quarantine}
}
func (s *ea1AgentAdmissionStore) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	v, e := s.Store.Create(ctx, r, events)
	if e == nil && s.afterCreate != nil {
		e = s.afterCreate(ctx, v)
	}
	return v, e
}
func (s *ea1AgentAdmissionStore) AcquireExecution(ctx context.Context, r kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	s.acquireCalls++
	l, e := s.ExecutionStore.AcquireExecution(ctx, r)
	if e != nil {
		return l, e
	}
	s.acquired = l
	if s.afterAcquire != nil {
		s.afterAcquire(ctx)
	}
	return l, e
}

func (s *ea1AgentAdmissionStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	window, err := s.OwnedPhysicalExecutionStore.CheckOwnedPhysicalEntry(ctx, g, l, source)
	if err == nil && s.afterEntry != nil {
		err = s.afterEntry(ctx)
	}
	return window, err
}

func (s *ea1AgentAdmissionStore) BeginOwnedPhysicalExecution(ctx context.Context, a kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	g := a.Candidate().Grant
	if s.beforeOwned != nil {
		if e := s.beforeOwned(ctx, g); e != nil {
			return kernel.OwnedPhysicalAdmissionResult{}, e
		}
	}
	v, e := s.OwnedPhysicalExecutionStore.BeginOwnedPhysicalExecution(ctx, a)
	if e == nil {
		if a.AcquiresLease() {
			s.acquired = v.Lease
		}
		if s.afterOwned != nil {
			e = s.afterOwned(ctx, v.Grant)
		}
	}
	return v, e
}

func (s *ea1AgentAdmissionStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	s.releaseCalls++
	return s.ExecutionStore.ReleaseExecution(ctx, l)
}

func (s *ea1AgentAdmissionStore) RenewExecution(ctx context.Context, r kernel.ExecutionRenewRequest) (kernel.ExecutionLease, error) {
	s.renewCalls++
	renewed, err := s.ExecutionStore.RenewExecution(ctx, r)
	if err == nil {
		s.renewals.record(r.Lease, renewed)
	}
	return renewed, err
}
