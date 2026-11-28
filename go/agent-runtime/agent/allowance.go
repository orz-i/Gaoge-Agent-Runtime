package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// CallAllowance is the cumulative, renewable allowance for logical model and
// local Tool calls in one Agent Run. Zero omits segmentation for that
// dimension; its frozen absolute budget.Limits still applies.
type CallAllowance struct {
	LLMCalls  int                  `json:"llmCalls,omitempty"`
	ToolCalls int                  `json:"toolCalls,omitempty"`
	Renewal   *AutoAllowancePolicy `json:"renewal,omitempty"`
}

var (
	ErrInvalidCallAllowance = errors.New("invalid agent call allowance")
	ErrRunNotBudgetPaused   = errors.New("agent run is not paused for execution allowance")
)

const (
	modelAllowanceErrorCode = "agent.model_allowance_exhausted"
	toolAllowanceErrorCode  = "agent.tool_allowance_exhausted"
)

// Clone freezes the sole segment configuration at the Host/Run boundary.
func (a *CallAllowance) Clone() *CallAllowance {
	if a == nil {
		return nil
	}
	v := *a
	if a.Renewal != nil {
		policy := *a.Renewal
		v.Renewal = &policy
	}
	return &v
}

type callAllowanceState struct {
	CallAllowance
	AutoAllowanceProgress
	ModelFingerprint string `json:"modelFingerprint,omitempty"`
	ToolFingerprint  string `json:"toolFingerprint,omitempty"`
}

func resolvedCallAllowance(request *CallAllowance, hard Limits) (*callAllowanceState, error) {
	if request == nil {
		return nil, nil
	}
	s := &callAllowanceState{CallAllowance: *request.Clone()}
	if !validCallAllowance(s, hard) {
		return nil, ErrInvalidCallAllowance
	}
	return s, nil
}

func validCallAllowance(s *callAllowanceState, hard Limits) bool {
	if s == nil {
		return true
	}
	if s.LLMCalls < 0 || s.ToolCalls < 0 || s.LLMCalls == 0 && s.ToolCalls == 0 || s.LLMCalls > hard.MaxLLMCalls || s.ToolCalls > hard.MaxToolCalls {
		return false
	}
	return validRenewalState(s, hard)
}

// Both automatic and elected manual grants advance the same cumulative
// ceiling. Neither may change the frozen renewal policy or hard Run limit.
func (s *runState) extendCallAllowance(increment CallAllowance) error {
	if s.CallAllowance == nil || increment.Renewal != nil || increment.LLMCalls < 0 || increment.ToolCalls < 0 ||
		(increment.LLMCalls == 0) == (increment.ToolCalls == 0) {
		return ErrInvalidCallAllowance
	}
	next := *s.CallAllowance
	if increment.LLMCalls > s.Budget.Limits.MaxLLMCalls-next.LLMCalls || increment.ToolCalls > s.Budget.Limits.MaxToolCalls-next.ToolCalls {
		return ErrInvalidCallAllowance
	}
	next.LLMCalls += increment.LLMCalls
	next.ToolCalls += increment.ToolCalls
	s.CallAllowance = &next
	return nil
}

func allowanceExhausted(state runState, dimension string) bool {
	if state.CallAllowance == nil {
		return false
	}
	switch dimension {
	case "model":
		return state.CallAllowance.LLMCalls > 0 &&
			state.Budget.Usage.LLMCalls >= state.CallAllowance.LLMCalls
	case "tool":
		return state.CallAllowance.ToolCalls > 0 &&
			state.Budget.Usage.ToolCalls >= state.CallAllowance.ToolCalls
	}
	return false
}

// pauseCallAllowance stores the exact pending transcript/Tool IDs and usage
// before denying the next external dispatch. A pause has no automatic wakeup.
func (runner *Runner) pauseCallAllowance(
	ctx context.Context, snapshot kernel.Snapshot, state runState, dimension string,
) (kernel.Snapshot, error) {
	code := modelAllowanceErrorCode
	if dimension == "tool" {
		code = toolAllowanceErrorCode
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	paused, err := runner.commitNativeTransition(ctx, snapshot, kernel.Mutation{
		Status: kernel.RunStatusPausedBudget, State: encoded, Checkpoint: snapshot.Checkpoint,
		ErrorCode: code, ErrorDetail: fmt.Sprintf("%s call allowance exhausted", dimension),
		Events: []kernel.EventDraft{{Type: "agent.budget_paused", Message: dimension}},
	})
	if err == nil {
		runner.publishRunEvent(ctx, paused, EventRunPausedBudget, false)
	}
	return paused, err
}

// GrantCallAllowance is a privileged, revision-guarded execution command. The
// calling Harness/HTTP owner must authorize this Run first; the SDK Runner
// never interprets tenant credentials. It does not dispatch external work.
// Existing completed model/tool receipts remain exactly as stored.
func (runner *Runner) GrantCallAllowance(
	ctx context.Context, runID string, expectedRevision uint64, increment CallAllowance,
) (kernel.Snapshot, error) {
	if runner == nil || strings.TrimSpace(runID) == "" || expectedRevision == 0 ||
		increment.Renewal != nil || increment.LLMCalls < 0 || increment.ToolCalls < 0 ||
		increment.LLMCalls == 0 && increment.ToolCalls == 0 {
		return kernel.Snapshot{}, agentControlNoEffect(ErrInvalidCallAllowance)
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, agentControlNoEffect(err)
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, agentControlNoEffect(kernel.ErrConflict)
	}
	if snapshot.Run.Status != kernel.RunStatusPausedBudget || state.CallAllowance == nil {
		return snapshot, agentControlNoEffect(ErrRunNotBudgetPaused)
	}
	if snapshot.Run.DeadlineAt != nil && !snapshot.Run.DeadlineAt.After(runner.clock.Now()) {
		return snapshot, agentControlNoEffect(kernel.ErrDeadline)
	}
	// A grant may only resolve the blocked dimension. Other dimensions cannot
	// be silently expanded during an unrelated approval or allowance action.
	switch snapshot.Run.ErrorCode {
	case modelAllowanceErrorCode:
		if increment.LLMCalls == 0 || increment.ToolCalls != 0 {
			return snapshot, agentControlNoEffect(ErrInvalidCallAllowance)
		}
	case toolAllowanceErrorCode:
		if increment.ToolCalls == 0 || increment.LLMCalls != 0 {
			return snapshot, agentControlNoEffect(ErrInvalidCallAllowance)
		}
	default:
		return snapshot, agentControlNoEffect(ErrRunNotBudgetPaused)
	}
	if err = state.extendCallAllowance(increment); err != nil {
		return snapshot, agentControlNoEffect(err)
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, agentControlNoEffect(err)
	}
	// Durable wakeup is committed with the CAS; stale worker deliveries are
	// filtered by ExpectedRevision before any additional model/Tool dispatch.
	proof := featurecontrol.Seal(runID, expectedRevision, string(kernel.ControlAllowance),
		snapshot.Run.ErrorCode, encoded, snapshot.Checkpoint)
	running, _, err := runner.runtime.ApplyControl(ctx, runID, expectedRevision,
		kernel.ControlGuard{Kind: kernel.ControlAllowance, BlockedCode: snapshot.Run.ErrorCode}, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
			Events: []kernel.EventDraft{{Type: "agent.allowance_granted", Message: snapshot.Run.ErrorCode, Wakeup: true}},
		}, proof)
	if err != nil && kernel.IsControlNoEffect(err) {
		// Kernel certifies this attempt failed before entering its
		// write-capable Store boundary. Preserve the original error for
		// diagnostics, and let Harness retire only this native control's
		// pending dispatch grant.
		return running, agentControlNoEffect(err)
	}
	if err == nil {
		runner.publishRunEvent(ctx, running, EventRunResumed, false)
	}
	// Any Store.ApplyControl/Apply error is outcome-unknown here, including
	// ErrConflict and an ACK lost after committing a control CAS.
	return running, err
}
