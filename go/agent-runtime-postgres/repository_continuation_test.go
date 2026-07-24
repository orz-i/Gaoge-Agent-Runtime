package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

var errInjectedContinuationTransaction = errors.New("injected transaction failure")

func TestContinuationCheckpointAndJobRollbackTogether(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	repo := New(db, StaticSessions(db))
	err := repo.within(t.Context(), func(txCtx context.Context) error {
		checkpoint := models.RunCheckpoint{CheckpointID: "checkpoint-rollback", RunID: "run-rollback", Kind: "continuation", Status: "active", ResumeStateJSON: `{}`}
		if createErr := repo.dbFor(txCtx).Create(&checkpoint).Error; createErr != nil {
			return createErr
		}
		_, _, createErr := repo.CreateContinuationJob(txCtx, &domain.ContinuationJob{
			JobID: "continuation-rollback", SegmentKey: "segment-rollback", RunID: "run-rollback", CheckpointID: checkpoint.CheckpointID,
			Actor: domain.ActorRef{TenantID: "tenant-rollback", ActorID: "actor-rollback"}, Status: domain.ContinuationJobQueued, MaxAttempts: 3, AvailableAt: time.Now(),
		})
		if createErr != nil {
			return createErr
		}
		return errInjectedContinuationTransaction
	})
	if !errors.Is(err, errInjectedContinuationTransaction) {
		t.Fatalf("transaction error = %v, want %v", err, errInjectedContinuationTransaction)
	}
	var checkpointCount, jobCount int64
	if err = db.Model(&models.RunCheckpoint{}).Where("checkpoint_id = ?", "checkpoint-rollback").Count(&checkpointCount).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Model(&models.ContinuationJobRecord{}).Where("job_id = ?", "continuation-rollback").Count(&jobCount).Error; err != nil {
		t.Fatal(err)
	}
	if checkpointCount != 0 || jobCount != 0 {
		t.Fatalf("rolled back checkpoint/job counts = %d/%d", checkpointCount, jobCount)
	}
}
