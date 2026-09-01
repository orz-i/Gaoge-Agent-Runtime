package team_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

var errInjectedTeamCrash = errors.New("injected team crash")

func TestTeamRecoversCrashAfterTopologyCommitBeforeChildRelations(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &teamFaultStore{Store: base}
	children := newFakeChildren(childCompletes)
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), teamClock{})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newTeamFaultRuntime(t, faults)
	runner := newTeamFaultRunner(t, runtime, coordinator, relations)
	request := teamRequest(team.ExecutionParallel, handoff.JoinAll)
	request.ID = "team-start-crash"
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, errInjectedTeamCrash) {
		t.Fatalf("start error = %v", err)
	}
	if children.startCount() != 0 {
		t.Fatalf("children started before recovery: %d", children.startCount())
	}
	if items, listErr := relations.ListChildren(t.Context(), request.ID); listErr != nil || len(items) != 0 {
		t.Fatalf("relations existed before recovery: %#v, %v", items, listErr)
	}

	restartedRuntime := newTeamFaultRuntime(t, base)
	restarted := newTeamFaultRunner(t, restartedRuntime, coordinator, relations)
	crashed, err := restartedRuntime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), crashed.Run.ID, crashed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	assertCompletedTeam(t, completed, 2)
	if children.startCount() != 2 {
		t.Fatalf("recovery child starts = %d", children.startCount())
	}
	items, err := relations.ListChildren(t.Context(), request.ID)
	assertTeamRelations(t, items, err)
}

type teamFaultStore struct {
	kernel.Store
	failed bool
}

func (store *teamFaultStore) Create(
	ctx context.Context,
	record kernel.Record,
	events []kernel.EventDraft,
) (kernel.Snapshot, error) {
	if !store.failed {
		for _, event := range events {
			if event.Type == "team.started" {
				store.failed = true
				snapshot, err := store.Store.Create(ctx, record, events)
				if err != nil {
					return snapshot, err
				}
				return kernel.Snapshot{}, errInjectedTeamCrash
			}
		}
	}
	return store.Store.Create(ctx, record, events)
}

func newTeamFaultRuntime(t *testing.T, store kernel.Store) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: teamClock{}, IDs: &teamIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newTeamFaultRunner(
	t *testing.T,
	runtime *kernel.Runtime,
	coordinator *handoff.Coordinator,
	relations *runrelation.Registry,
) *team.Runner {
	t.Helper()
	runner, err := team.NewRunner(team.Dependencies{
		Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
