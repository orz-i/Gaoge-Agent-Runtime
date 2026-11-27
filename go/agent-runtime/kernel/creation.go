package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// A rejected creation identity is permanent. It does not manufacture a Run,
// event, Lease or authority to replay an unknown effect.
var (
	ErrCreationCancelled = errors.New("kernel Run creation cancelled")
	ErrCreationFailed    = errors.New("kernel Run creation failed before admission")
)

// RunCreationDecision records the single negative decision under the same
// native identity key as Create. Status describes the decision, not a Run.
type RunCreationDecision struct {
	RunID  string
	Status RunStatus
	Reason string
	Budget budget.RunEnrollment
}

func (c RunCreationDecision) Valid() bool {
	return c.RunID != "" && strings.TrimSpace(c.RunID) == c.RunID && utf8.ValidString(c.RunID) &&
		(c.Status == RunStatusCancelled || c.Status == RunStatusFailed && c.Reason != "") &&
		strings.TrimSpace(c.Reason) == c.Reason && utf8.ValidString(c.Reason) &&
		(c.Budget == (budget.RunEnrollment{}) || (c.Budget.Valid(c.RunID) && c.Budget.Binding.ParentRunID == ""))
}

func (c RunCreationDecision) MatchesSelection(q RunCreationDecision) bool {
	return c.Valid() && q.Valid() && c.RunID == q.RunID && c.Budget == q.Budget
}

func (c RunCreationDecision) Rejection() error {
	if c.Status == RunStatusCancelled {
		return ErrCreationCancelled
	}
	return ErrCreationFailed
}

// An empty payload denotes the positive Create identity; competing negative
// decisions return the original outcome, never replace its status or reason.
func DecodeCreationDecision(runID, payload string) (RunCreationDecision, error) {
	var c RunCreationDecision
	if payload == "" {
		return c, ErrAlreadyExists
	}
	if json.Unmarshal([]byte(payload), &c) != nil || !c.Valid() || c.RunID != runID {
		return RunCreationDecision{}, ErrConflict
	}
	return c, nil
}

func (r *Runtime) DecideBeforeCreate(ctx context.Context, c RunCreationDecision) (RunCreationDecision, error) {
	if r == nil || ctx == nil || r.execution != nil || r.physicalGrant != nil || !c.Valid() {
		return RunCreationDecision{}, ErrInvalidInput
	}
	return r.store.DecideBeforeCreate(ctx, c)
}

func (r *Runtime) LoadCreationDecision(ctx context.Context, id string) (RunCreationDecision, error) {
	if r == nil || ctx == nil || strings.TrimSpace(id) != id || id == "" {
		return RunCreationDecision{}, ErrInvalidInput
	}
	return r.store.LoadCreationDecision(ctx, id)
}

// ValidateCreationRetry must run within the native creation transaction.
// Ledger membership alone is never evidence for a predecessor without a Run.
func ValidateCreationRetry(ctx context.Context, e *budget.RunEnrollment,
	load func(context.Context, string) (Snapshot, error), decision func(context.Context, string) (RunCreationDecision, error),
) error {
	if e == nil || e.Retry == (budget.RootRetryGrant{}) {
		return nil
	}
	prior := e.Retry.OriginalRunID
	s, err := load(ctx, prior)
	if err == nil {
		if s.Run.ID != prior || s.Run.BudgetScopeID != e.ScopeID || s.Run.BudgetParentRunID != "" ||
			s.Run.ParentRunID != "" || !terminalStatus(s.Run.Status) || s.Run.Status == RunStatusCompleted {
			return budget.ErrLedgerConflict
		}
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	c, err := decision(ctx, prior)
	if err != nil {
		return errors.Join(budget.ErrLedgerConflict, err)
	}
	p := c.Budget
	if !c.Valid() || c.RunID != prior || p.ScopeID != e.ScopeID || p.Limits != e.Limits || p.Binding != e.Binding ||
		(e.Retry.Attempt == 2 && p.Retry != (budget.RootRetryGrant{})) ||
		(e.Retry.Attempt > 2 && (p.Retry.Attempt != e.Retry.Attempt-1 || p.Retry.InvocationID != e.Retry.InvocationID || !p.Retry.Active)) {
		return budget.ErrLedgerConflict
	}
	return nil
}
