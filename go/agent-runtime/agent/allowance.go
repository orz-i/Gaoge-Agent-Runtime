package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// CallAllowance is the cumulative, renewable allowance for logical model and
// local Tool calls in one direct Agent Run. Zero omits segmentation for that
// dimension; its frozen absolute budget.Limits still applies.
type CallAllowance struct {
	LLMCalls  int `json:"llmCalls,omitempty"`
	ToolCalls int `json:"toolCalls,omitempty"`
}

var (
	ErrInvalidCallAllowance = errors.New("invalid agent call allowance")
	ErrRunNotBudgetPaused   = errors.New("agent run is not paused for execution allowance")
)

const (
	modelAllowanceErrorCode = "agent.model_allowance_exhausted"
	toolAllowanceErrorCode  = "agent.tool_allowance_exhausted"
)

func resolvedCallAllowance(request *CallAllowance, hard Limits) (*CallAllowance, error) {
	if request == nil {
		return nil, nil
	}
	if request.LLMCalls < 0 || request.ToolCalls < 0 ||
		request.LLMCalls == 0 && request.ToolCalls == 0 ||
		request.LLMCalls > hard.MaxLLMCalls || request.ToolCalls > hard.MaxToolCalls {
		return nil, ErrInvalidCallAllowance
	}
	copied := *request
	return &copied, nil
}

func validCallAllowance(allowance *CallAllowance, hard Limits) bool {
	if allowance == nil {
		return true
	}
	_, err := resolvedCallAllowance(allowance, hard)
	return err == nil
}

func cloneCallAllowance(value *CallAllowance) *CallAllowance {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
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
	paused, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
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
		increment.LLMCalls < 0 || increment.ToolCalls < 0 ||
		increment.LLMCalls == 0 && increment.ToolCalls == 0 {
		return kernel.Snapshot{}, ErrInvalidCallAllowance
	}
	snapshot, state, err := runner.loadState(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Revision != expectedRevision {
		return kernel.Snapshot{}, kernel.ErrConflict
	}
	if snapshot.Run.Status != kernel.RunStatusPausedBudget || state.CallAllowance == nil {
		return snapshot, ErrRunNotBudgetPaused
	}
	if snapshot.Run.DeadlineAt != nil && !snapshot.Run.DeadlineAt.After(runner.clock.Now()) {
		return snapshot, kernel.ErrDeadline
	}
	// A grant may only resolve the blocked dimension. Other dimensions cannot
	// be silently expanded during an unrelated approval or allowance action.
	switch snapshot.Run.ErrorCode {
	case modelAllowanceErrorCode:
		if increment.LLMCalls == 0 || increment.ToolCalls != 0 {
			return snapshot, ErrInvalidCallAllowance
		}
	case toolAllowanceErrorCode:
		if increment.ToolCalls == 0 || increment.LLMCalls != 0 {
			return snapshot, ErrInvalidCallAllowance
		}
	default:
		return snapshot, ErrRunNotBudgetPaused
	}
	next := *state.CallAllowance
	if increment.LLMCalls > state.Budget.Limits.MaxLLMCalls-next.LLMCalls ||
		increment.ToolCalls > state.Budget.Limits.MaxToolCalls-next.ToolCalls {
		return snapshot, ErrInvalidCallAllowance
	}
	next.LLMCalls += increment.LLMCalls
	next.ToolCalls += increment.ToolCalls
	state.CallAllowance = &next
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	// Durable wakeup is committed with the CAS; stale worker deliveries are
	// filtered by ExpectedRevision before any additional model/Tool dispatch.
	running, err := runner.runtime.Apply(ctx, runID, expectedRevision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.allowance_granted", Message: snapshot.Run.ErrorCode, Wakeup: true}},
	})
	if err == nil {
		runner.publishRunEvent(ctx, running, EventRunResumed, false)
	}
	return running, err
}
