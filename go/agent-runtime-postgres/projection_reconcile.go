package postgres

import (
	"errors"
	"fmt"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	stateprojection "github.com/orz-i/Gaoge/sdk/go/agent-runtime/projection"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	currentStateProjectionVersion = 1
	currentHostProjectionVersion  = 1
)

var ErrRunProjectionReplay = errors.New("agent runtime state projection replay failed")

func reconcileHistoricalRunState(db *gorm.DB) error {
	var runIDs []string
	if err := db.Model(&models.RunRecord{}).
		Where("state_projection_version < ?", currentStateProjectionVersion).
		Order("id").
		Pluck("run_id", &runIDs).Error; err != nil {
		return err
	}
	for _, runID := range runIDs {
		if err := replayRunState(db, runID); err != nil {
			return fmt.Errorf("%w: run_id=%s: %w", ErrRunProjectionReplay, runID, err)
		}
	}
	return nil
}

func replayRunState(db *gorm.DB, runID string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var row models.RunRecord
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("run_id = ?", runID).Take(&row).Error; err != nil {
			return err
		}
		if row.StateProjectionVersion >= currentStateProjectionVersion {
			return nil
		}
		return replayLockedRunState(tx, row)
	})
}

func replayLockedRunState(tx *gorm.DB, row models.RunRecord) error {
	var eventRows []models.EventRecord
	if err := tx.Where("run_id = ? AND event_scope = ?", row.RunID, runEventScope).Order("seq,id").Find(&eventRows).Error; err != nil {
		return err
	}
	if len(eventRows) == 0 {
		return tx.Model(&row).Update("state_projection_version", currentStateProjectionVersion).Error
	}
	var stepRows []models.RunStep
	if err := tx.Where("run_id = ?", row.RunID).Order("step_index,id").Find(&stepRows).Error; err != nil {
		return err
	}
	run, steps, err := projectHistoricalRun(row, stepRows, eventRows)
	if err != nil {
		return err
	}
	return persistHistoricalRunProjection(tx, row, stepRows, run, steps)
}

func projectHistoricalRun(
	row models.RunRecord,
	stepRows []models.RunStep,
	eventRows []models.EventRecord,
) (domain.Run, map[string]*domain.Step, error) {
	run := resetRunProjection(row)
	steps := resetStepProjections(stepRows)
	for _, eventRow := range eventRows {
		event := toEventDomain(eventRow)
		if err := stateprojection.ApplyEvent(&run, steps[event.StepID], event); err != nil {
			return domain.Run{}, nil, err
		}
		run.LastEventSeq = event.Seq
		if isPresentationEvent(event) {
			run.LastPresentationEventSeq = event.Seq
		}
	}
	if isTerminalRunStatus(run.Status) {
		run.PendingInteractionID = ""
	}
	return run, steps, nil
}

func persistHistoricalRunProjection(
	tx *gorm.DB,
	row models.RunRecord,
	stepRows []models.RunStep,
	run domain.Run,
	steps map[string]*domain.Step,
) error {
	if err := tx.Model(&row).Updates(runProjectionUpdates(run)).Error; err != nil {
		return err
	}
	for index := range stepRows {
		step := steps[stepRows[index].StepID]
		if step != nil {
			if err := tx.Model(&stepRows[index]).Updates(stepProjectionUpdates(*step)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func resetRunProjection(row models.RunRecord) domain.Run {
	run := toRunDomain(row)
	run.Status = domain.RunStatusQueued
	run.StatusReason = ""
	run.CurrentStepID = ""
	run.LastEventSeq = 0
	run.LastPresentationEventSeq = 0
	run.InputTokens = 0
	run.OutputTokens = 0
	run.CacheReadTokens = 0
	run.CacheWriteTokens = 0
	run.ReasoningTokens = 0
	run.LLMCallsCount = 0
	run.ToolCallsCount = 0
	run.FirstTokenLatencyMS = 0
	run.TotalLatencyMS = 0
	run.EndedAt = nil
	return run
}

func resetStepProjections(rows []models.RunStep) map[string]*domain.Step {
	result := make(map[string]*domain.Step, len(rows))
	for _, row := range rows {
		step := toStepDomain(row)
		step.Status = domain.RunStatusQueued
		step.ResultSummary = ""
		step.ErrorJSON = ""
		step.StartedAt = domain.Step{}.StartedAt
		step.EndedAt = nil
		projected := step
		result[step.StepID] = &projected
	}
	return result
}
