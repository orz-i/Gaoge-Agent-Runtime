package harnesspostgres_test

import (
	"context"
	"errors"
	"testing"

	postgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type retirementKernelPorts interface {
	kernel.Store
	kernel.ExecutionStore
	kernel.ChildExecutionStore
	kernel.OwnedPhysicalExecutionStore
	kernel.OwnedRetirementStore
	kernel.ControlStore
	kernel.ExecutionCustodyStore
}

var errBeforeRetirementACK = errors.New("process died before retirement ACK commit")

type retirementACKBeforeCommitCut struct {
	retirementKernelPorts
	deny, finished bool
}

func (s *retirementACKBeforeCommitCut) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	e := s.retirementKernelPorts.FinishChildExecution(ctx, g, p)
	if e == nil && s.deny {
		s.finished = true
		return errBeforeRetirementACK
	}
	return e
}
func (s *retirementACKBeforeCommitCut) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.deny && s.finished {
		return kernel.OwnedRetirementReceipt{}, errBeforeRetirementACK
	}
	return s.retirementKernelPorts.LoadOwnedRetirement(ctx, id)
}
func TestRetiredCallbackClosurePrecedesEveryNewGeneration(t *testing.T) {
	for _, backend := range generationBackends {
		for _, cancelInstead := range []bool{false, true} {
			name := backend + "/resume"
			if cancelInstead {
				name = backend + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				var base retirementKernelPorts = memory.NewStore()
				if backend == "sql" {
					_, db := newGenerationSQLStore(t)
					if err := postgres.Migrate(db); err != nil {
						t.Fatal(err)
					}
					base = postgres.NewKernelStore(db)
				}
				if backend == "native-sqlite" {
					base = generationNativeSQLite(t)
				}
				cut := &retirementACKBeforeCommitCut{retirementKernelPorts: base, deny: true}
				rt, err := kernel.New(kernel.Dependencies{Store: cut})
				if err != nil {
					t.Fatal(err)
				}
				m := &generationModel{calls: 10}
				original, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m})
				if err != nil {
					t.Fatal(err)
				}
				prepared, err := original.PrepareRun(t.Context(), agent.StartRequest{ID: "unacknowledged-prepare", Actor: kernel.ActorRef{TenantID: "t", ActorID: "a"},
					Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, RequestID: "start", Goal: "close original callback", Model: "model"})
				if !errors.Is(err, errBeforeRetirementACK) {
					t.Fatalf("pre-ACK crash not exercised: %v", err)
				}
				prior, err := base.LookupOwnedRetirementForRun(t.Context(), prepared.Run.ID)
				if err != nil || !prior.Valid() {
					t.Fatalf("original genuine retirement missing: %+v %v", prior, err)
				}
				cut.deny = false
				fresh, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m})
				if err != nil {
					t.Fatal(err)
				}
				var done kernel.Snapshot
				if cancelInstead {
					done, err = rt.Cancel(t.Context(), prepared.Run.ID, prepared.Run.Revision, "cancel at ACK frontier")
				} else {
					done, err = fresh.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
				}
				if err != nil {
					t.Fatalf("successor failed original closure: %v", err)
				}
				closed, err := base.LoadOwnedRetirement(t.Context(), prior.Evidence.Grant.ID)
				if err != nil || !closed.Valid() || closed.Identity() != prior.Identity() {
					t.Fatalf("original identity was lost or silently rewritten: %+v %v", closed, err)
				}
				if err = rt.VerifyExecutionCustody(t.Context(), done, kernel.CustodyRetired); err != nil {
					t.Fatalf("completed successor stranded original custody: %v", err)
				}
				wantCalls := 11
				if cancelInstead {
					wantCalls = 10
				}
				if m.calls != wantCalls {
					t.Fatalf("unexpected physical callback replay: %d", m.calls)
				}
			})
		}
	}
}
