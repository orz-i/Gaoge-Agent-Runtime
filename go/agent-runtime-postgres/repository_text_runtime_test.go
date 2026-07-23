package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestCreateRunStartBundlePersistsRefsOnlyAtomicRoot(t *testing.T) {
	repo := newTestRepository(t)
	run, step, snapshot, artifacts, checkpoint, events := runtimeStartBundleFixture("run_atomic")

	saved, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, artifacts, &checkpoint, events)
	assertRunStartEvents(t, saved, run, err)
	loaded, err := repo.GetRun(context.Background(), run.Actor, run.RunID)
	assertRunStartLoaded(t, loaded, run, err)
	if _, err = repo.GetRun(context.Background(), domain.ActorRef{TenantID: run.Actor.TenantID, ActorID: "foreign"}, run.RunID); !errors.Is(err, agentruntime.ErrNotFound) {
		t.Fatalf("foreign actor read error = %v", err)
	}
	contextSnapshot, err := repo.GetRunContextSnapshot(context.Background(), run.Actor, run.RunID)
	assertRunStartSnapshot(t, contextSnapshot, snapshot, run, err)
	checkpoints, err := repo.ListRunCheckpoints(context.Background(), run.Actor, run.RunID)
	assertRunStartCheckpoints(t, checkpoints, snapshot, err)
}

func assertRunStartEvents(t *testing.T, saved []domain.Event, run domain.Run, err error) {
	t.Helper()
	if err != nil || len(saved) != 1 || saved[0].Seq != 1 || saved[0].Actor != run.Actor || saved[0].Thread != run.Thread {
		t.Fatalf("saved events = %#v, err=%v", saved, err)
	}
}

func assertRunStartLoaded(t *testing.T, loaded *domain.Run, run domain.Run, err error) {
	t.Helper()
	if err != nil || loaded.Thread != run.Thread || loaded.Environment != run.Environment || loaded.LastEventSeq != 1 {
		t.Fatalf("loaded run = %#v, err=%v", loaded, err)
	}
}

func assertRunStartSnapshot(t *testing.T, loaded *domain.ContextSnapshot, snapshot domain.ContextSnapshot, run domain.Run, err error) {
	t.Helper()
	if err != nil || loaded.SnapshotID != snapshot.SnapshotID || loaded.InputProjection != run.InputProjection {
		t.Fatalf("context snapshot = %#v, err=%v", loaded, err)
	}
}

func assertRunStartCheckpoints(t *testing.T, checkpoints []domain.Checkpoint, snapshot domain.ContextSnapshot, err error) {
	t.Helper()
	if err != nil || len(checkpoints) != 1 || checkpoints[0].ContextSnapshotID != snapshot.SnapshotID {
		t.Fatalf("checkpoints = %#v, err=%v", checkpoints, err)
	}
}

func TestCreateRunStartBundleRollsBackEveryRowOnArtifactConflict(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	repo := New(db, StaticSessions(db))
	run, step, snapshot, artifacts, checkpoint, events := runtimeStartBundleFixture("run_rollback")
	artifacts = append(artifacts, artifacts[0])

	if _, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, artifacts, &checkpoint, events); err == nil {
		t.Fatal("expected duplicate artifact to abort the bundle")
	}
	for table, model := range map[string]interface{}{
		"runs":        &models.RunRecord{},
		"steps":       &models.RunStep{},
		"contexts":    &models.ContextRecord{},
		"checkpoints": &models.RunCheckpoint{},
		"events":      &models.EventRecord{},
	} {
		var count int64
		if err := db.Model(model).Where("run_id = ?", run.RunID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s rollback count=%d err=%v", table, count, err)
		}
	}
}

func TestCreateRunStartBundleAllowsMultipleSnapshotsWithoutArtifacts(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	repo := New(db, StaticSessions(db))
	for _, runID := range []string{"run_snapshot_one", "run_snapshot_two"} {
		run, step, snapshot, _, checkpoint, events := runtimeStartBundleFixture(runID)
		if _, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, nil, &checkpoint, events); err != nil {
			t.Fatalf("create %s: %v", runID, err)
		}
	}
	var count int64
	if err := db.Model(&models.ContextRecord{}).Where("record_type = ?", "snapshot").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("snapshot count = %d, want 2", count)
	}
}

func TestCreatePlanningBundleAtomicallyMovesRunToWaitingInput(t *testing.T) {
	repo := newTestRepository(t)
	run, step, snapshot, artifacts, checkpoint, events := runtimeStartBundleFixture("run_planning")
	if _, err := repo.CreateRunStartBundle(context.Background(), &run, &step, &snapshot, artifacts, &checkpoint, events); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	plan := domain.Plan{PlanID: "plan_1", RunID: run.RunID, Revision: 1, Status: "pending", Goal: run.Goal, PayloadJSON: `{"steps":[]}`}
	interaction := domain.Interaction{InteractionID: "interaction_1", RunID: run.RunID, Type: "approve_plan", Status: domain.InteractionPending, RequestPayloadJSON: `{}`, RequestedAt: now}
	waiting := domain.Checkpoint{CheckpointID: "checkpoint_waiting", RunID: run.RunID, StepID: step.StepID, Kind: "waiting_input", Status: domain.CheckpointReady, ResumeStateJSON: `{}`}
	event := domain.Event{EventID: "event_waiting", RunID: run.RunID, Actor: run.Actor, Thread: run.Thread, EventType: "run.waiting_input", Visibility: visibilityUser, Status: domain.RunStatusWaitingInput, StartedAt: now, PayloadJSON: `{}`}

	saved, err := repo.CreatePlanningBundle(context.Background(), run.RunID, domain.RunStatusPreparing, &plan, nil, &interaction, &waiting, []domain.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.GetRun(context.Background(), run.Actor, run.RunID)
	if err != nil || loaded.Status != domain.RunStatusWaitingInput || loaded.CurrentPlanID != plan.PlanID || loaded.PendingInteractionID != interaction.InteractionID || len(saved) != 1 || saved[0].Seq != 2 {
		t.Fatalf("planning result run=%#v events=%#v err=%v", loaded, saved, err)
	}
}

func runtimeStartBundleFixture(runID string) (domain.Run, domain.Step, domain.ContextSnapshot, []domain.ContextArtifact, domain.Checkpoint, []domain.Event) {
	actor := domain.ActorRef{TenantID: "tenant_atomic", ActorID: "actor_1"}
	thread := domain.ThreadRef{Kind: "conversation", ID: "thread_1"}
	input := domain.ProjectionRef{Kind: projectionKindMessage, ID: "message_input"}
	now := time.Now()
	run := domain.Run{RunID: runID, RequestID: "request_1", Actor: actor, Thread: thread, InputProjection: input, OutputProjection: domain.ProjectionRef{Kind: projectionKindMessage, ID: "message_output"}, Environment: domain.ResourceRef{Kind: "environment", ID: "environment_1", Revision: "7"}, Goal: "inspect", RunConfigSnapshotJSON: `{}`, Status: domain.RunStatusPreparing, StartedAt: now}
	step := domain.Step{StepID: "step_root_" + runID, RunID: runID, Kind: "orchestration", Title: "Inspect", Status: domain.RunStatusPreparing, StartedAt: now}
	snapshot := domain.ContextSnapshot{SnapshotID: "snapshot_" + runID, RunID: runID, Actor: actor, Thread: thread, InputProjection: input, SchemaVersion: 1, ThreadPathHash: "path_hash", ContentJSON: `{}`, ContentHash: "content_hash"}
	artifacts := []domain.ContextArtifact{{ArtifactID: "artifact_" + runID, RunID: runID, Kind: domain.ContextArtifactSummary, Resource: domain.ResourceRef{Kind: string(domain.ContextArtifactSummary), ID: "summary_1"}, SourceType: "test", SourceID: "summary_1", Content: "durable evidence", ContentHash: "artifact_hash"}}
	checkpoint := domain.Checkpoint{CheckpointID: "checkpoint_" + runID, RunID: runID, StepID: step.StepID, Kind: "initial_context", Status: domain.CheckpointReady, ResumeStateJSON: `{}`}
	events := []domain.Event{{EventID: "event_" + runID, RunID: runID, Actor: actor, Thread: thread, Projection: input, EventType: "run.preparing", Visibility: visibilityUser, Status: domain.RunStatusPreparing, StartedAt: now, PayloadJSON: `{}`}}
	return run, step, snapshot, artifacts, checkpoint, events
}
