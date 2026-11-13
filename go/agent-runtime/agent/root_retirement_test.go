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
	acknowledgements       []kernel.OwnedRetirementAckResult
	releases               int
	unavailableAfterFinish bool
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
	err := s.Store.MarkChildExecutionSettled(ctx, g, p)
	if err == nil {
		s.grant = g
		if s.lose("mark") {
			return errRootRetirementACKLost
		}
	}
	return err
}
func (s *rootRetirementFaultStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	err := s.Store.FinishChildExecution(ctx, g)
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

func (s *rootRetirementFaultStore) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	result, err := s.Store.AcknowledgeOwnedRetirement(ctx, g, p)
	if err == nil {
		s.acknowledgements = append(s.acknowledgements, result)
		if s.lose("ack") {
			return kernel.OwnedRetirementAckResult{}, errRootRetirementACKLost
		}
	}
	return result, err
}
func (s *rootRetirementFaultStore) ReleaseExecution(ctx context.Context, l kernel.ExecutionLease) error {
	s.releases++
	return s.Store.ReleaseExecution(ctx, l)
}

func TestRootRetirementActualCallbackSurvivesCommittedACKLoss(t *testing.T) {
	for _, cut := range []string{"record", "mark", "finish", "ack"} {
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
			if err != nil || !receipt.Valid() || receipt.Phase != "ACKNOWLEDGED" || receipt.Evidence.Grant != store.grant || !reflect.DeepEqual(receipt.Evidence.Snapshot, completed) || !receipt.Evidence.Completion.HostCompleted || !receipt.Evidence.Completion.LeaseOwned {
				t.Fatalf("missing original durable ACK: %#v %v", receipt, err)
			}
			if store.releases != 0 {
				t.Fatalf("ordinary Release bypassed atomic ACK: %d", store.releases)
			}
			expectedACKs := 1
			if cut == "ack" {
				expectedACKs = 2
			}
			if len(store.acknowledgements) != expectedACKs {
				t.Fatalf("ACKs=%#v", store.acknowledgements)
			}
			for i, ack := range store.acknowledgements {
				if ack.ReceiptID != store.grant.ID || ack.SettledSourceDigest != kernel.ManagedDigest(completed) || ack.Disposition != "released_owned" || ack.PreviouslyAcknowledged != (i > 0) {
					t.Fatalf("wrong ACK %d: %#v", i, ack)
				}
			}
			if err := store.FinishChildExecution(t.Context(), store.grant); !errors.Is(err, kernel.ErrExecutionConflict) {
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
	if err != nil || receipt.Phase != "ACKNOWLEDGED" || receipt.Evidence.Completion.LeaseOwned || !store.grant.MatchesExecution(lease) {
		t.Fatalf("borrowed receipt=%#v err=%v", receipt, err)
	}
	last := store.acknowledgements[len(store.acknowledgements)-1]
	if last.Disposition != "preserved_borrowed" || store.releases != 0 {
		t.Fatalf("borrowed lease release: %#v count=%d", last, store.releases)
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
	if err != nil || retired.Phase != "RETIRED" {
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
	if err != nil || acknowledged.Phase != "ACKNOWLEDGED" || acknowledged.Identity() != retired.Identity() {
		t.Fatalf("recovery did not acknowledge original evidence: %#v %v", acknowledged, err)
	}
	if err := fresh.ReconcileRootPhysicalExecution(t.Context(), original.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("RunID selected historical ACK: %v", err)
	}
}
