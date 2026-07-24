package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/gorm"
)

const (
	columnEndedAt            = "ended_at"
	columnErrorCode          = "error_code"
	columnErrorMessage       = "error_message"
	columnPendingInteraction = "pending_interaction_id"
	columnResolvedAt         = "resolved_at"
	columnRevision           = "revision"
	columnStatus             = "status"
	columnUpdatedAt          = "updated_at"
	projectionKindMessage    = "conversation.message"
	visibilityUser           = "user"
)

type SessionProvider interface {
	DB(context.Context) *gorm.DB
}

type Repository struct {
	db       *gorm.DB
	sessions SessionProvider
}

var _ agentruntime.Store = (*Repository)(nil)

type transactionContextKey struct{}

type staticSessionProvider struct{ db *gorm.DB }

func (p staticSessionProvider) DB(ctx context.Context) *gorm.DB {
	if p.db == nil {
		return nil
	}
	return p.db.WithContext(ctx)
}

func StaticSessions(db *gorm.DB) SessionProvider { return staticSessionProvider{db: db} }

func New(db *gorm.DB, sessions SessionProvider) *Repository {
	return &Repository{db: db, sessions: sessions}
}

func (r *Repository) dbFor(ctx context.Context) *gorm.DB {
	if ctx != nil {
		if tx, ok := ctx.Value(transactionContextKey{}).(*gorm.DB); ok && tx != nil {
			return tx.WithContext(ctx)
		}
	}
	if r.sessions != nil {
		if db := r.sessions.DB(ctx); db != nil {
			return db
		}
	}
	if r.db == nil {
		return nil
	}
	return r.db.WithContext(ctx)
}

func (r *Repository) within(ctx context.Context, work func(context.Context) error) error {
	if work == nil {
		return errors.New("agent runtime postgres transaction work is required")
	}
	if ctx != nil {
		if tx, ok := ctx.Value(transactionContextKey{}).(*gorm.DB); ok && tx != nil {
			return work(ctx)
		}
	}
	db := r.dbFor(ctx)
	if db == nil {
		return ErrNilDatabase
	}
	return db.Transaction(func(tx *gorm.DB) error {
		return work(context.WithValue(ctx, transactionContextKey{}, tx))
	})
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agentruntime.ErrNotFound
	}
	if isUniqueConstraint(err) {
		return agentruntime.ErrDuplicate
	}
	var stateErr interface{ SQLState() string }
	if errors.As(err, &stateErr) {
		switch stateErr.SQLState() {
		case "40001":
			return fmt.Errorf("postgres serialization failure: %w", err)
		case "40P01":
			return fmt.Errorf("postgres deadlock: %w", err)
		}
	}
	return err
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var stateErr interface{ SQLState() string }
	if errors.As(err, &stateErr) && stateErr.SQLState() == "23505" {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate key") || strings.Contains(message, "unique constraint")
}

// EraseAccountData is retained for the account lifecycle boundary. The
// numeric identity is converted at the bootstrap edge; storage remains refs-only.
func (r *Repository) EraseAccountData(ctx context.Context, userID uint) error {
	actorID := strconv.FormatUint(uint64(userID), 10)
	db := r.dbFor(ctx).Unscoped()
	runIDs := db.Model(&models.RunRecord{}).Select("run_id").Where("tenant_id = ? AND actor_id = ?", "default", actorID)
	steps := []func() error{
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.RuntimeOutputRefRecord{}).Error },
		func() error {
			return db.Where("run_id IN (?)", runIDs).Delete(&models.RuntimePhaseProjectionRecord{}).Error
		},
		func() error {
			return db.Where("run_id IN (?)", runIDs).Delete(&models.RuntimeWorkbenchProjectionRecord{}).Error
		},
		func() error {
			return db.Where("tenant_id = ? AND actor_id = ?", "default", actorID).Delete(&models.EvidenceSelection{}).Error
		},
		func() error {
			return db.Where("tenant_id = ? AND actor_id = ?", "default", actorID).Delete(&models.RunQueueItemRecord{}).Error
		},
		func() error {
			return db.Where("tenant_id = ? AND actor_id = ?", "default", actorID).Delete(&models.ContinuationJobRecord{}).Error
		},
		func() error {
			return db.Where("tenant_id = ? AND actor_id = ?", "default", actorID).Delete(&models.RunHandoffRecord{}).Error
		},
		func() error {
			return db.Where("created_by_tenant_id = ? AND created_by_actor_id = ?", "default", actorID).Delete(&models.AgentManifestRevisionRecord{}).Error
		},
		func() error {
			return db.Where("tenant_id = ? AND actor_id = ?", "default", actorID).Delete(&models.RuntimeOutputIdentityRecord{}).Error
		},
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.RunCheckpoint{}).Error },
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.RunInteraction{}).Error },
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.RuntimePlanRecord{}).Error },
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.RunStep{}).Error },
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.ContextRecord{}).Error },
		func() error { return db.Where("run_id IN (?)", runIDs).Delete(&models.EventRecord{}).Error },
		func() error {
			return db.Where("tenant_id = ? AND actor_id = ?", "default", actorID).Delete(&models.RunRecord{}).Error
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return translateError(err)
		}
	}
	return nil
}

func (r *Repository) DeleteRunEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	result := r.dbFor(ctx).Where("created_at < ?", before).Delete(&models.EventRecord{})
	return result.RowsAffected, translateError(result.Error)
}

// DeleteEventLogsBefore satisfies the account retention boundary without
// exposing the old Conversation repository contract.
func (r *Repository) DeleteEventLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	return r.DeleteRunEventsBefore(ctx, before)
}

func recordNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }
