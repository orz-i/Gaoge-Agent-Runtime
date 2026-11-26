package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// ErrCreationCancelled means this identity was irrevocably cancelled before
// a Run existed. There is no Run, event stream, lease or callback to recover.
var ErrCreationCancelled = errors.New("kernel Run creation cancelled")

// RunCreationCancellation is one immutable negative Run identity decision.
// Budget is zero for an unbudgeted root, otherwise the original selection.
// The first cancellation reason wins; an exact retry observes that fact.
type RunCreationCancellation struct {
	RunID  string
	Reason string
	Budget budget.RunEnrollment
}

func (c RunCreationCancellation) Valid() bool {
	return c.RunID != "" && strings.TrimSpace(c.RunID) == c.RunID && utf8.ValidString(c.RunID) &&
		strings.TrimSpace(c.Reason) == c.Reason && utf8.ValidString(c.Reason) &&
		(c.Budget == (budget.RunEnrollment{}) || (c.Budget.Valid(c.RunID) && c.Budget.Binding.ParentRunID == ""))
}

func (c RunCreationCancellation) MatchesSelection(q RunCreationCancellation) bool {
	return c.Valid() && q.Valid() && c.RunID == q.RunID && c.Budget == q.Budget
}

// SQL identity rows serialize both contenders through one permanent unique
// key. The empty payload means Create, never a cancellation or a Run snapshot.
func DecodeCreationCancellation(runID, payload string) (RunCreationCancellation, error) {
	var c RunCreationCancellation
	if payload == "" {
		return c, ErrAlreadyExists
	}
	if json.Unmarshal([]byte(payload), &c) != nil || !c.Valid() || c.RunID != runID {
		return RunCreationCancellation{}, ErrConflict
	}
	return c, nil
}

func (r *Runtime) CancelBeforeCreate(ctx context.Context, c RunCreationCancellation) (RunCreationCancellation, error) {
	if r == nil || ctx == nil || r.execution != nil || r.physicalGrant != nil || !c.Valid() {
		return RunCreationCancellation{}, ErrInvalidInput
	}
	return r.store.CancelBeforeCreate(ctx, c)
}
