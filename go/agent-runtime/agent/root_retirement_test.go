package agent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

var errRootRetirementACKLost = errors.New("fixture lost committed root retirement response")

type rootRetirementFaultStore struct {
	*memory.Store
	cut                    string
	hit                    bool
	grant                  kernel.ChildExecutionGrant
	releases               int
	unavailableAfterFinish bool
	markCalls              int
	finishCommits          int
	finishCalls            int
}

func (s *rootRetirementFaultStore) lose(cut string) bool {
	if !s.hit && s.cut == cut {
		s.hit = true
		return true
	}
	return false
}
func (s *rootRetirementFaultStore) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, outcome kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	err := s.Store.RecordOwnedPhysicalCallbackReturn(ctx, g, revision, outcome, p)
	if err == nil && s.lose("record") {
		return errRootRetirementACKLost
	}
	return err
}
func (s *rootRetirementFaultStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.markCalls++
	return s.Store.MarkChildExecutionSettled(ctx, g, p)
}
func (s *rootRetirementFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.finishCalls++
	s.grant = g
	if s.cut == "finish-never-committed" {
		s.hit = true
		return errRootRetirementACKLost
	}
	err := s.Store.FinishChildExecution(ctx, g, p)
	if err == nil {
		s.finishCommits++
	}
	if err == nil && s.lose("death") {
		s.unavailableAfterFinish = true
		return errRootRetirementACKLost
	}
	if err == nil && s.lose("finish") {
		return errRootRetirementACKLost
	}
	return err
}
func (s *rootRetirementFaultStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.unavailableAfterFinish {
		return kernel.OwnedRetirementReceipt{}, errRootRetirementACKLost
	}
	return s.Store.LoadOwnedRetirement(ctx, id)
}

func (s *rootRetirementFaultStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	s.releases++
	return s.Store.ReleaseExecution(ctx, l)
}

func TestRootRetirementActualCallbackSurvivesCommittedACKLoss(t *testing.T) {
	for _, cut := range []string{"record", "finish"} {
		t.Run(cut, func(t *testing.T) {
			store := &rootRetirementFaultStore{Store: memory.NewStore(), cut: cut}
			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			model := &nativeLeaseFixtureModel{}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: model})
			if err != nil {
				t.Fatal(err)
			}
			completed, err := runner.StartRun(t.Context(), agent.StartRequest{ID: "retirement-" + cut, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "retain original successful callback"})
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || !store.hit || model.calls != 1 {
				t.Fatalf("callback result=%#v err=%v hit=%v calls=%d", completed, err, store.hit, model.calls)
			}
			persisted, err := rt.Load(t.Context(), completed.Run.ID)
			if err != nil || !reflect.DeepEqual(persisted, completed) {
				t.Fatalf("original full result changed: %#v %v", persisted, err)
			}
			receipt, err := store.LoadOwnedRetirement(t.Context(), store.grant.ID)
			if err != nil || !receipt.Valid() || receipt.Evidence.Grant != store.grant || !reflect.DeepEqual(receipt.Evidence.Snapshot, completed) || !receipt.Evidence.Completion.HostCompleted || !receipt.Evidence.Completion.LeaseOwned {
				t.Fatalf("missing original durable ACK: %#v %v", receipt, err)
			}
			if store.releases != 0 {
				t.Fatalf("ordinary Release bypassed atomic ACK: %d", store.releases)
			}
			if err := store.FinishChildExecution(t.Context(), store.grant, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("generic missing Finish changed contract: %v", err)
			}
		})
	}
}

func TestRootRetirementPreservesBorrowedLease(t *testing.T) {
	store := &rootRetirementFaultStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	model := &nativeLeaseFixtureModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := runner.PrepareRun(t.Context(), agent.StartRequest{ID: "borrowed-retirement", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "preserve caller native owner"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: prepared.Run.ID, OwnerID: "external-caller", LeaseDuration: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := rt.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	borrower, err := agent.NewRunner(agent.Dependencies{Runtime: bound, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := borrower.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || model.calls != 1 {
		t.Fatalf("borrowed result=%#v err=%v calls=%d", completed, err, model.calls)
	}
	receipt, err := store.LoadOwnedRetirement(t.Context(), store.grant.ID)
	if err != nil || !receipt.Valid() || receipt.Evidence.Completion.LeaseOwned || !store.grant.MatchesExecution(lease) {
		t.Fatalf("borrowed receipt=%#v err=%v", receipt, err)
	}
	if store.releases != 0 {
		t.Fatalf("borrowed lease released: %d", store.releases)
	}
	// Terminal CheckExecution intentionally cannot attest lease ownership; the
	// caller's actual original Release succeeds only if its exact token survived.
	if err := store.Store.ReleaseExecution(t.Context(), lease); err != nil {
		t.Fatalf("caller lease no longer owned: %v", err)
	}
	persisted, err := rt.Load(t.Context(), completed.Run.ID)
	if err != nil || !reflect.DeepEqual(persisted, completed) {
		t.Fatalf("caller release mutated source: %#v %v", persisted, err)
	}
}

type retirementHiddenStore struct {
	kernel.Store
	kernel.ExecutionStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	creates int
}

func (s *retirementHiddenStore) Create(ctx context.Context, r kernel.Record, e []kernel.EventDraft) (kernel.Snapshot, error) {
	s.creates++
	return s.Store.Create(ctx, r, e)
}

func TestRootRetirementCapabilityRequiredBeforeCreate(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		base := memory.NewStore()
		store := &retirementHiddenStore{Store: base, ExecutionStore: base, ChildExecutionStore: base, OwnedPhysicalExecutionStore: base}
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		model := &nativeLeaseFixtureModel{}
		host := &rootCountingRunMiddleware{}
		deps := agent.Dependencies{Runtime: rt, Model: model, RunMiddleware: []plugin.RunMiddleware{host}}
		if explicit {
			deps.RunExecution = &agent.RunExecution{OwnerID: "explicit-owner", LeaseDuration: 3 * time.Minute}
		}
		if _, err := agent.NewRunner(deps); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("missing retirement accepted explicit=%v: %v", explicit, err)
		}
		if store.creates != 0 || model.calls != 0 || host.calls.Load() != 0 {
			t.Fatalf("unsupported adapter changed state: creates=%d host=%d sends=%d", store.creates, host.calls.Load(), model.calls)
		}
	}
}

// Memory retains durable state while the old adapter becomes unavailable. SQL
// counterparts must close all old handles and reopen; this is not a disk-restart
// assertion. The recovery call receives only RunID, never the original grant ID.
func TestRootRetirementFreshRunnerRecoversFinishedCallByRunID(t *testing.T) {
	base := memory.NewStore()
	store := &rootRetirementFaultStore{Store: base, cut: "death"}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	model := &nativeLeaseFixtureModel{}
	old, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	original, err := old.StartRun(t.Context(), startRequest("finished-before-ack", "original-request", "finish exactly once"))
	if !errors.Is(err, agent.ErrRootOutcomeUnknown) || !errors.Is(err, errRootRetirementACKLost) || !store.hit || model.calls != 1 {
		t.Fatalf("death cut not reached: result=%#v err=%v hit=%v calls=%d", original, err, store.hit, model.calls)
	}
	before, err := base.Load(t.Context(), original.Run.ID)
	if err != nil || !reflect.DeepEqual(before, original) {
		t.Fatalf("lost original source: %#v %v", before, err)
	}
	retired, err := base.LoadOwnedRetirement(t.Context(), store.grant.ID)
	if err != nil || !retired.Valid() {
		t.Fatalf("Finish did not commit before death: %#v %v", retired, err)
	}
	if _, err := base.LookupChildExecution(t.Context(), original.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("active grant survived Finish: %v", err)
	}
	reopened, err := kernel.New(kernel.Dependencies{Store: base})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := agent.NewRunner(agent.Dependencies{Runtime: reopened, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.ReconcileRootPhysicalExecution(t.Context(), original.Run.ID); err != nil {
		t.Fatalf("public only-RunID recovery failed: %v", err)
	}
	after, err := base.Load(t.Context(), original.Run.ID)
	if err != nil || !reflect.DeepEqual(before, after) || model.calls != 1 {
		t.Fatalf("recovery mutated source/replayed: %#v %v calls=%d", after, err, model.calls)
	}
	acknowledged, err := base.LoadOwnedRetirement(t.Context(), store.grant.ID)
	if err != nil || !acknowledged.Valid() || acknowledged.Identity() != retired.Identity() {
		t.Fatalf("recovery did not acknowledge original evidence: %#v %v", acknowledged, err)
	}
	if err := fresh.ReconcileRootPhysicalExecution(t.Context(), original.Run.ID); err != nil {
		t.Fatalf("RunID selected historical ACK: %v", err)
	}
}

func TestRootRetirementFinishRequiresActualCommit(t *testing.T) {
	for _, committed := range []bool{true, false} {
		cut := "finish-never-committed"
		if committed {
			cut = "finish"
		}
		t.Run(cut, func(t *testing.T) {
			base := memory.NewStore()
			store := &rootRetirementFaultStore{Store: base, cut: cut}
			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			model := &nativeLeaseFixtureModel{}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: model})
			if err != nil {
				t.Fatal(err)
			}
			result, callErr := runner.StartRun(t.Context(), startRequest("persistent-"+cut, "original-request", "finish without inventing receipt"))
			if !store.hit || store.markCalls != 0 || store.finishCalls != 1 || model.calls != 1 || store.releases != 0 {
				t.Fatalf("wrong actual cut counts: mark=%d committed=%d finish=%d model=%d release=%d hit=%v", store.markCalls, store.finishCommits, store.finishCalls, model.calls, store.releases, store.hit)
			}
			actual, err := base.Load(t.Context(), result.Run.ID)
			if err != nil || !reflect.DeepEqual(actual, result) {
				t.Fatalf("full original aggregate changed: %#v %v", actual, err)
			}
			if committed {
				if callErr != nil || result.Run.Status != kernel.RunStatusCompleted || store.finishCommits != 1 {
					t.Fatalf("committed marker not recovered: %#v err=%v commits=%d", result, callErr, store.finishCommits)
				}
				receipt, err := base.LoadOwnedRetirement(t.Context(), store.grant.ID)
				if err != nil || !receipt.Valid() || receipt.Evidence.Grant != store.grant || !reflect.DeepEqual(receipt.Evidence.Snapshot, result) {
					t.Fatalf("wrong durable retirement: %#v %v", receipt, err)
				}
			} else {
				if !errors.Is(callErr, agent.ErrRootOutcomeUnknown) || !errors.Is(callErr, errRootRetirementACKLost) || store.finishCommits != 0 {
					t.Fatalf("missing marker was accepted/error chain lost: result=%#v err=%v commits=%d", result, callErr, store.finishCommits)
				}
				if _, err := base.LoadOwnedRetirement(t.Context(), store.grant.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("fabricated retirement without marker: %v", err)
				}
				retained, err := base.LookupChildExecution(t.Context(), result.Run.ID)
				if err != nil || retained != store.grant {
					t.Fatalf("original barrier changed: %#v %v", retained, err)
				}
				if err := base.FinishChildExecution(t.Context(), retained, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatalf("ordinary Finish accepted absent marker: %v", err)
				}
			}
		})
	}
}
