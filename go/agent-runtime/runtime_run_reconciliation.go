package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) startTextRunReconciliation(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := s.now()
		if err := s.ReconcileTextRunsOnce(ctx, now.Add(-60*time.Second)); err != nil && s.logger != nil {
			s.logger.Error("reconcile_text_runs_failed", Error(err))
		}
		if err := s.ExpireRunInteractionsOnce(ctx, now); err != nil && s.logger != nil {
			s.logger.Error("expire_run_interactions_failed", Error(err))
		}
		if err := s.ExpireRunHandoffJoinsOnce(ctx, now); err != nil && s.logger != nil {
			s.logger.Error("expire_run_handoff_joins_failed", Error(err))
		}
	}
}

// ReconcileTextRunsOnce runs the same fail-safe reconciliation pass used by
// the periodic worker. It is exported for operational probes and deterministic
// crash-recovery tests; it does not alter the public HTTP contract.
func (s *Engine) ReconcileTextRunsOnce(ctx context.Context, olderThan time.Time) error {
	return reconcileTextRunsOnce(ctx, olderThan, textRunReconciliationDependencies{
		list:       s.repo.ListNonterminalRuns,
		leaseState: s.TextRunLeaseState,
		warn: func(runID string, state GenerationLeaseState, leaseErr error) {
			evidence := "store_unknown"
			if state == GenerationLeaseActive {
				evidence = "local_active"
			}
			if s.logger != nil {
				s.logger.Warn("text_run_generation_lease_degraded", String("run_id", runID), String("lease_state", string(state)), String("evidence_source", evidence), Error(leaseErr))
			}
		},
		suspend: func(run model.Run, events []model.Event) (bool, error) {
			saved, applied, appendErr := s.repo.AppendRunEventsIfCurrent(ctx, run.RunID, run.Status, run.LastEventSeq, events)
			if appendErr == nil && applied {
				s.publishRunEvents(run.RunID, saved)
			}
			return applied, appendErr
		},
	})
}

// ExpireRunInteractionsOnce runs one deterministic interaction-expiry pass.
// It is exported for operational probes and crash-recovery contract tests.
func (s *Engine) ExpireRunInteractionsOnce(ctx context.Context, before time.Time) error {
	return expireRunInteractionsOnce(ctx, before, interactionExpiryDependencies{
		list:           s.repo.ListExpiredRunInteractions,
		expire:         s.repo.ExpireRunInteraction,
		expireWorkflow: s.expireWorkflowInteractionIfNeeded,
		publish:        s.publishRunEvents,
		finish:         s.FinishRunNotifications,
	})
}

func expireRunInteractionsOnce(ctx context.Context, before time.Time, deps interactionExpiryDependencies) error {
	items, err := deps.list(ctx, before, 100)
	if err != nil {
		return err
	}
	for _, item := range items {
		var saved []model.Event
		var applied, handled bool
		var expireErr error
		if deps.expireWorkflow != nil {
			saved, applied, handled, expireErr = deps.expireWorkflow(ctx, item)
		}
		if expireErr == nil && !handled {
			saved, applied, expireErr = deps.expire(ctx, item.InteractionID)
		}
		if expireErr != nil {
			return expireErr
		}
		if applied {
			deps.publish(item.RunID, saved)
			if !handled {
				deps.finish(item.RunID)
			}
		}
	}
	return nil
}

func reconcileTextRunsOnce(ctx context.Context, olderThan time.Time, deps textRunReconciliationDependencies) error {
	runs, err := deps.list(ctx, olderThan)
	if err != nil {
		return err
	}
	var reconcileErr error
	for i := range runs {
		if !runRequiresGenerationLease(runs[i].Status) {
			continue
		}
		state, leaseErr := deps.leaseState(ctx, runs[i].RunID)
		if leaseErr != nil && deps.warn != nil {
			deps.warn(runs[i].RunID, state, leaseErr)
		}
		if state != GenerationLeaseExpired {
			continue
		}
		reason := "runtime lease is no longer active"
		events := []model.Event{
			newRunEvent(runs[i], "step.suspended", runs[i].CurrentStepID, reason, map[string]interface{}{valueStatus6CF1EE63: model.RunStatusSuspended}, nil),
			newRunEvent(runs[i], "run.suspended", runs[i].CurrentStepID, reason, map[string]interface{}{valueStatus6CF1EE63: model.RunStatusSuspended}, nil),
		}
		if _, err := deps.suspend(runs[i], events); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("suspend stale text run %s: %w", runs[i].RunID, err))
		}
	}
	return reconcileErr
}

func runRequiresGenerationLease(status string) bool {
	switch status {
	case model.RunStatusQueued, model.RunStatusPreparing, model.RunStatusRunning:
		return true
	default:
		return false
	}
}
