package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const valueNewPhase = "new"

func TestWorkbenchSnapshotAndProjectionAreActorScoped(t *testing.T) {
	repo := newTestRepository(t)
	run, step, snapshot, artifacts, checkpoint, events := runtimeStartBundleFixture("run_workbench")
	if _, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, artifacts, &checkpoint, events); err != nil {
		t.Fatal(err)
	}
	projection := domain.WorkbenchProjection{RunID: run.RunID, ProjectionVersion: 2, SourcePresentationEventSeq: 1}
	phases := []domain.PhaseProjection{{PhaseID: "phase_1", RunID: run.RunID, Kind: "execution", Title: "Execute", Status: domain.RunStatusRunning, StartSeq: 1, StepIDsJSON: `["` + step.StepID + `"]`, ToolCallIDsJSON: `[]`, OutputIDsJSON: `[]`, StartedAt: time.Now()}}
	if err := repo.ReplaceWorkbenchProjection(context.Background(), run.Actor, &projection, phases); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadWorkbenchSnapshot(context.Background(), run.Actor, run.RunID)
	assertWorkbenchSnapshot(t, loaded, run, snapshot, err)
	foreign := domain.ActorRef{TenantID: run.Actor.TenantID, ActorID: "foreign"}
	if _, err = repo.LoadWorkbenchSnapshot(context.Background(), foreign, run.RunID); !errors.Is(err, agentruntime.ErrNotFound) {
		t.Fatalf("foreign snapshot error = %v", err)
	}
	foreignProjection := domain.WorkbenchProjection{RunID: run.RunID, ProjectionVersion: 3}
	if err = repo.ReplaceWorkbenchProjection(context.Background(), foreign, &foreignProjection, nil); !errors.Is(err, agentruntime.ErrNotFound) {
		t.Fatalf("foreign replace error = %v", err)
	}
}

func assertWorkbenchSnapshot(t *testing.T, loaded *domain.WorkbenchSnapshot, run domain.Run, snapshot domain.ContextSnapshot, err error) {
	t.Helper()
	if err != nil || loaded.Run.Actor != run.Actor || loaded.Run.Thread != run.Thread || loaded.Context == nil || loaded.Context.SnapshotID != snapshot.SnapshotID {
		t.Fatalf("workbench snapshot core = %#v, err=%v", loaded, err)
	}
	if loaded.Projection == nil || loaded.Projection.ProjectionVersion != 2 || len(loaded.Phases) != 1 || loaded.Phases[0].PhaseID != "phase_1" {
		t.Fatalf("workbench snapshot projection = %#v", loaded)
	}
}

func TestReplaceWorkbenchProjectionAtomicallyReplacesPhases(t *testing.T) {
	repo := newTestRepository(t)
	run, step, snapshot, artifacts, checkpoint, events := runtimeStartBundleFixture("run_workbench_replace")
	if _, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, artifacts, &checkpoint, events); err != nil {
		t.Fatal(err)
	}
	first := domain.WorkbenchProjection{RunID: run.RunID, ProjectionVersion: 1, SourcePresentationEventSeq: 1}
	if err := repo.ReplaceWorkbenchProjection(context.Background(), run.Actor, &first, []domain.PhaseProjection{{PhaseID: "old", RunID: run.RunID, Kind: "planning", Status: domain.RunStatusCompleted, StartSeq: 1, StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	second := domain.WorkbenchProjection{RunID: run.RunID, ProjectionVersion: 2, SourcePresentationEventSeq: 4}
	if err := repo.ReplaceWorkbenchProjection(context.Background(), run.Actor, &second, []domain.PhaseProjection{{PhaseID: valueNewPhase, RunID: run.RunID, Kind: "execution", Status: domain.RunStatusRunning, StartSeq: 4, StartedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadWorkbenchSnapshot(context.Background(), run.Actor, run.RunID)
	if err != nil || loaded.Projection == nil || loaded.Projection.ProjectionVersion != 2 || len(loaded.Phases) != 1 || loaded.Phases[0].PhaseID != valueNewPhase {
		t.Fatalf("replaced snapshot = %#v, err=%v", loaded, err)
	}
}

func TestListPresentationEventsExcludesTransportDeltas(t *testing.T) {
	repo := newTestRepository(t)
	run, step, snapshot, artifacts, checkpoint, events := runtimeStartBundleFixture("run_presentation")
	if _, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, artifacts, &checkpoint, events); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := repo.AppendRunEvents(context.Background(), []domain.Event{
		{EventID: "delta", RunID: run.RunID, Actor: run.Actor, Thread: run.Thread, EventType: "message.delta", Visibility: visibilityUser, StartedAt: now, PayloadJSON: `{}`},
		{EventID: "step_started", RunID: run.RunID, Actor: run.Actor, Thread: run.Thread, EventType: "step.started", StepID: step.StepID, Visibility: visibilityUser, Status: domain.RunStatusRunning, StartedAt: now, PayloadJSON: `{}`},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := repo.ListPresentationEvents(context.Background(), run.Actor, run.RunID, 1)
	if err != nil || len(items) != 1 || items[0].EventID != "step_started" {
		t.Fatalf("presentation events = %#v, err=%v", items, err)
	}
}
