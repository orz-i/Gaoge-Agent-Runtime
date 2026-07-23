package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	conversation "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	domain "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueQueueNext7B72D515 = "queue_next"
	valueText4E0E40B0      = "text"
)

func TestRunQueueClaimsOnlyOneItemPerConversation(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	requirePostgresTestNoError(t, db.AutoMigrate(&models.RunQueueItemRecord{}))
	actor, thread := queueTestRefs("actor_11", "queue_conversation")
	repo := New(db, StaticSessions(db))
	ctx := context.Background()
	for index, queueID := range []string{"queue_one", "queue_two"} {
		_, reused, err := repo.CreateRunQueueItem(ctx, &domain.QueueItem{QueueID: queueID, ClientQueueID: queueID, RequestFingerprint: queueID, Actor: actor, Thread: thread, Status: domain.QueueQueued, RequestJSON: `{"semanticVersion":1,"input":{"content":"test"}}`, Position: index + 1})
		requirePostgresTest(t, err == nil && !reused, "create %s: reused=%v err=%v", queueID, reused, err)
	}
	first, err := repo.ClaimNextRunQueueItem(ctx, time.Now())
	requirePostgresTest(t, err == nil && first.Status == domain.QueueDispatching, "first claim = %#v, err=%v", first, err)
	_, err = repo.ClaimNextRunQueueItem(ctx, time.Now())
	requirePostgresTestErrorIs(t, err, conversation.ErrNotFound)
	requirePostgresTestNoError(t, repo.RequeueRunQueueItem(ctx, first.QueueID, "permanent", "invalid resource", nil))
	second, err := repo.ClaimNextRunQueueItem(ctx, time.Now())
	requirePostgresTest(t, err == nil && second.QueueID == "queue_two", "next claim = %#v, err=%v", second, err)
}

func TestRunQueueEditRequeuesFailedItemAndCancelUsesCAS(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	requirePostgresTestNoError(t, db.AutoMigrate(&models.RunQueueItemRecord{}))
	actor, thread := queueTestRefs("actor_12", "queue_edit")
	repo := New(db, StaticSessions(db))
	ctx := context.Background()
	created, _, err := repo.CreateRunQueueItem(ctx, &domain.QueueItem{QueueID: "queue_edit_one", ClientQueueID: "client_edit_one", RequestFingerprint: "old", Actor: actor, Thread: thread, Status: domain.QueueQueued, RequestJSON: `{"semanticVersion":1,"input":{"content":"old"}}`})
	requirePostgresTestNoError(t, err)
	claimed, err := repo.ClaimNextRunQueueItem(ctx, time.Now())
	requirePostgresTestNoError(t, err)
	_, err = repo.CancelRunQueueItem(ctx, actor, thread, claimed.QueueID)
	requirePostgresTestErrorIs(t, err, conversation.ErrRunQueueConflict)
	requirePostgresTestNoError(t, repo.RequeueRunQueueItem(ctx, claimed.QueueID, "profile_unavailable", "profile disabled", nil))
	failed, err := repo.GetRunQueueItem(ctx, actor, thread, created.QueueID)
	requirePostgresTest(t, err == nil && failed.Status == domain.QueueFailed, "failed item = %#v err=%v", failed, err)
	failed.RequestJSON, failed.RequestFingerprint = `{"semanticVersion":1,"input":{"content":"new"}}`, "new"
	requirePostgresTestNoError(t, repo.UpdateRunQueueItem(ctx, failed, failed.Revision))
	updated, err := repo.GetRunQueueItem(ctx, actor, thread, failed.QueueID)
	requirePostgresTest(t, err == nil && updated.Status == domain.QueueQueued && updated.ErrorCode == "", "updated item = %#v err=%v", updated, err)
}

func TestRunQueueSuspendedStartedRunBlocksNextDispatch(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	requirePostgresTestNoError(t, db.AutoMigrate(&models.RunQueueItemRecord{}))
	actor, thread := queueTestRefs("actor_13", "queue_suspended")
	startedAt := time.Now().Add(-time.Minute)
	run := models.RunRecord{RunID: "run_suspended_queue", TenantID: actor.TenantID, ActorID: actor.ActorID, ThreadKind: thread.Kind, ThreadID: thread.ID, Status: domain.RunStatusSuspended, StartedAt: startedAt}
	requirePostgresTestNoError(t, db.Create(&run).Error)
	repo := New(db, StaticSessions(db))
	ctx := context.Background()
	started, _, err := repo.CreateRunQueueItem(ctx, &domain.QueueItem{QueueID: "queue_started", ClientQueueID: "queue_started", RequestFingerprint: "started", Actor: actor, Thread: thread, Status: domain.QueueQueued, RequestJSON: `{"semanticVersion":1,"input":{"content":"first"}}`})
	requirePostgresTestNoError(t, err)
	requirePostgresTestNoError(t, db.Model(&models.RunQueueItemRecord{}).Where("queue_id = ?", started.QueueID).Updates(map[string]interface{}{columnStatus: domain.QueueStarted, "started_run_id": run.RunID}).Error)
	_, _, err = repo.CreateRunQueueItem(ctx, &domain.QueueItem{QueueID: valueQueueNext7B72D515, ClientQueueID: valueQueueNext7B72D515, RequestFingerprint: "next", Actor: actor, Thread: thread, Status: domain.QueueQueued, RequestJSON: `{"semanticVersion":1,"input":{"content":"second"}}`})
	requirePostgresTestNoError(t, err)
	_, err = repo.ClaimNextRunQueueItem(ctx, time.Now())
	requirePostgresTestErrorIs(t, err, conversation.ErrNotFound)
	completedAt := time.Now()
	requirePostgresTestNoError(t, db.Model(&models.RunRecord{}).Where("run_id = ?", run.RunID).Updates(map[string]interface{}{columnStatus: domain.RunStatusCompleted, columnEndedAt: completedAt}).Error)
	claimed, err := repo.ClaimNextRunQueueItem(ctx, time.Now())
	requirePostgresTest(t, err == nil && claimed.QueueID == valueQueueNext7B72D515, "claim after prior run completed = %#v, err=%v", claimed, err)
}

func requirePostgresTestNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requirePostgresTestErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func requirePostgresTest(t *testing.T, condition bool, format string, args ...interface{}) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func TestRunQueueRecoversStaleDispatchLease(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	if err := db.AutoMigrate(&models.RunQueueItemRecord{}); err != nil {
		t.Fatal(err)
	}
	actor, thread := queueTestRefs("actor_14", "queue_recovery")
	repo := New(db, StaticSessions(db))
	ctx := context.Background()
	created, _, err := repo.CreateRunQueueItem(ctx, &domain.QueueItem{QueueID: "queue_recover", ClientQueueID: "queue_recover", RequestFingerprint: "recover", Actor: actor, Thread: thread, Status: domain.QueueQueued, RequestJSON: `{"semanticVersion":1,"input":{"content":"recover"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ClaimNextRunQueueItem(ctx, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = db.Exec("UPDATE agent_queue_items SET updated_at = ? WHERE queue_id = ?", time.Now().Add(-2*time.Minute), created.QueueID).Error; err != nil {
		t.Fatal(err)
	}
	recovered, err := repo.ClaimNextRunQueueItem(ctx, time.Now())
	if err != nil || recovered.QueueID != created.QueueID || recovered.Status != domain.QueueDispatching || recovered.AttemptCount != 2 {
		t.Fatalf("recovered claim = %#v, err=%v", recovered, err)
	}
}

func queueTestRefs(actorID, threadID string) (domain.ActorRef, domain.ThreadRef) {
	return domain.ActorRef{TenantID: "tenant_queue", ActorID: actorID}, domain.ThreadRef{Kind: "conversation", ID: threadID}
}
