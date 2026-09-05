package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// Subtask is a product projection of a durable execution relation, folded by
// delegation identity. It is delivered with the Turn, not a separate Run feed.
type Subtask struct {
	ID            string             `json:"id"`
	RunID         string             `json:"-"`
	ParentRunID   string             `json:"-"`
	ParentID      string             `json:"parentID,omitempty"`
	Kind          string             `json:"kind"`
	RoleID        string             `json:"roleID,omitempty"`
	RoleRevision  uint64             `json:"roleRevision,omitempty"`
	RoleName      string             `json:"roleName,omitempty"`
	Goal          string             `json:"goal"`
	Model         string             `json:"model,omitempty"`
	Status        string             `json:"status"`
	StartedAt     *time.Time         `json:"startedAt,omitempty"`
	UpdatedAt     *time.Time         `json:"updatedAt,omitempty"`
	EndedAt       *time.Time         `json:"endedAt,omitempty"`
	Budget        *budget.LedgerView `json:"budget,omitempty"`
	UsageKnown    bool               `json:"usageKnown"`
	Result        json.RawMessage    `json:"result,omitempty"`
	ErrorCode     string             `json:"errorCode,omitempty"`
	ErrorDetail   string             `json:"errorDetail,omitempty"`
	Approval      *SubtaskApproval   `json:"approval,omitempty"`
	CancelStatus  string             `json:"cancelStatus,omitempty"`
	CancelError   string             `json:"cancelError,omitempty"`
	CancelAttempt uint64             `json:"cancelAttempt,omitempty"`
}

type SubtaskApproval struct {
	CheckpointID string          `json:"checkpointID"`
	ToolCallID   string          `json:"toolCallID"`
	ToolKey      string          `json:"toolKey"`
	ToolName     string          `json:"toolName"`
	Arguments    json.RawMessage `json:"arguments"`
}

func (runner *Runner) loadSubtasks(ctx context.Context, turn Turn, items []Item, ledger *budget.Ledger) ([]Subtask, error) {
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return nil, err
	}
	byRun := map[string]Subtask{}
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return nil, err
		}
		parentRunID := payload.ParentRunID
		for _, invocation := range invocations {
			if invocation.ID == item.InvocationID {
				parentRunID = invocation.ExecutionRefID
			}
		}
		value := Subtask{ID: payload.DelegationID, RunID: payload.ChildRunID, ParentRunID: parentRunID, Kind: "delegation",
			RoleID: payload.RoleID, RoleRevision: payload.RoleRevision, RoleName: firstNonEmpty(payload.RoleName, payload.MemberID),
			Goal: payload.Goal, Status: string(payload.Status), Result: append(json.RawMessage(nil), payload.Result...)}
		if existing, ok := byRun[value.RunID]; ok {
			value.RoleID, value.RoleRevision, value.RoleName = existing.RoleID, existing.RoleRevision, existing.RoleName
		}
		if payload.Execution != nil {
			value.Model = payload.Execution.Model
		}
		byRun[value.RunID] = value
	}
	if err = runner.addRelatedSubtasks(ctx, invocations, byRun); err != nil {
		return nil, err
	}
	result := make([]Subtask, 0, len(byRun))
	for _, value := range byRun {
		if parent, exists := byRun[value.ParentRunID]; exists {
			value.ParentID = parent.ID
		}
		projected, projectErr := runner.materializeSubtask(ctx, value, ledger)
		if projectErr != nil {
			return nil, projectErr
		}
		projected = mergeSubtaskCancellation(projected, items)
		result = append(result, projected)
	}
	slices.SortFunc(result, func(left, right Subtask) int {
		if left.StartedAt != nil && right.StartedAt != nil {
			if compared := left.StartedAt.Compare(*right.StartedAt); compared != 0 {
				return compared
			}
		}
		return strings.Compare(left.ID, right.ID)
	})
	return result, nil
}

func (runner *Runner) addRelatedSubtasks(ctx context.Context, invocations []Invocation, tasks map[string]Subtask) error {
	if runner.relationReader == nil {
		return nil
	}
	queue := []string{}
	for _, invocation := range invocations {
		if invocation.ExecutionRefID != "" {
			queue = append(queue, invocation.ExecutionRefID)
		}
	}
	seen := map[string]bool{}
	for len(queue) != 0 {
		runID := queue[0]
		queue = queue[1:]
		if seen[runID] {
			continue
		}
		seen[runID] = true
		relations, err := runner.relationReader.ListChildren(ctx, runID)
		if err != nil {
			return err
		}
		for _, relation := range relations {
			if _, exists := tasks[relation.ChildRunID]; !exists {
				tasks[relation.ChildRunID] = Subtask{ID: stableID("hsub", relation.ChildRunID), RunID: relation.ChildRunID,
					ParentRunID: relation.ParentRunID, Kind: string(relation.Kind), Status: "queued", StartedAt: &relation.CreatedAt}
			}
			queue = append(queue, relation.ChildRunID)
		}
	}
	return nil
}

func (runner *Runner) materializeSubtask(ctx context.Context, value Subtask, ledger *budget.Ledger) (Subtask, error) {
	snapshot, err := runner.runtime.Load(ctx, value.RunID)
	if errors.Is(err, kernel.ErrNotFound) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	value.Status = string(snapshot.Run.Status)
	value.Goal = snapshot.Run.Goal
	value.StartedAt, value.UpdatedAt = &snapshot.Run.CreatedAt, &snapshot.Run.UpdatedAt
	value.ErrorCode, value.ErrorDetail = snapshot.Run.ErrorCode, snapshot.Run.ErrorDetail
	if terminalRuntimeStatus(snapshot.Run.Status) {
		value.EndedAt = &snapshot.Run.UpdatedAt
	}
	if snapshot.Result != nil {
		value.Result = append(json.RawMessage(nil), snapshot.Result.Content...)
	}
	if view, viewErr := agent.ViewState(snapshot); viewErr == nil {
		value.Model = view.Model
		value.UsageKnown = ledger != nil
	}
	if ledger != nil {
		if _, exists := ledger.Runs[value.RunID]; exists {
			view := ledger.View(value.RunID)
			value.Budget = &view
			if view.WaitingRuns > 0 && snapshot.Run.Status == kernel.RunStatusRunning {
				value.Status = "waiting_budget"
			}
		}
	}
	approval, waiting, err := approvalRequestFromSnapshot(snapshot)
	if err != nil {
		return value, err
	}
	if waiting {
		value.Approval = &SubtaskApproval{CheckpointID: approval.CheckpointID, ToolCallID: approval.ToolCallID,
			ToolKey: approval.ToolKey, ToolName: approval.ToolName, Arguments: append(json.RawMessage(nil), approval.Arguments...)}
	}
	return value, nil
}

func mergeSubtaskCancellation(value Subtask, items []Item) Subtask {
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].Kind != ItemSubtask {
			continue
		}
		var previous Subtask
		if json.Unmarshal(items[index].Payload, &previous) != nil || previous.ID != value.ID {
			continue
		}
		value.CancelStatus, value.CancelError = previous.CancelStatus, previous.CancelError
		value.CancelAttempt = previous.CancelAttempt
		if value.Status == "queued" && previous.Status == "cancelled" && previous.CancelStatus == "confirmed" {
			value.Status = previous.Status
		}
		break
	}
	if value.Status == string(kernel.RunStatusCancelled) || value.CancelStatus != "" && (value.Status == "completed" || value.Status == "failed") {
		value.CancelStatus, value.CancelError = "confirmed", ""
	}
	return value
}

func (runner *Runner) recordSubtaskItem(ctx context.Context, turnID string, task Subtask) error {
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	status := ItemStarted
	switch task.Status {
	case "completed":
		status = ItemCompleted
	case "failed":
		status = ItemFailed
	case "cancelled":
		status = ItemCancelled
	case "waiting_budget", "waiting_input", "queued":
		status = ItemWaiting
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{ID: stableID("hsubitem", turnID, task.ID, hashInvocationBytes(raw)),
		TurnID: turnID, Kind: ItemSubtask, Status: status, RunID: task.RunID, Payload: raw, CreatedAt: now, UpdatedAt: now})
	return err
}

func (runner *Runner) syncSubtaskItems(ctx context.Context, turn Turn) error {
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return err
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil {
		return err
	}
	if ledger != nil {
		if err = runner.budgets.project(ctx, *ledger); err != nil {
			return err
		}
	}
	tasks, err := runner.loadSubtasks(ctx, turn, items, ledger)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err = runner.recordSubtaskItem(ctx, turn.ID, task); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) findSubtask(ctx context.Context, turnID, taskID string) (Turn, Subtask, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Turn{}, Subtask{}, err
	}
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return turn, Subtask{}, err
	}
	tasks, err := runner.loadSubtasks(ctx, turn, items, nil)
	if err != nil {
		return turn, Subtask{}, err
	}
	for _, task := range tasks {
		if task.ID == taskID {
			return turn, task, nil
		}
	}
	return turn, Subtask{}, ErrNotFound
}

// CancelSubtask keeps cancellation intent/failure visible while a remote or
// local canceller acknowledges cleanup. Retrying does not cancel siblings.
func (runner *Runner) CancelSubtask(ctx context.Context, turnID, taskID, reason string) (Snapshot, error) {
	turn, task, err := runner.findSubtask(ctx, turnID, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	if task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled" {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	task.CancelStatus, task.CancelError = "requested", ""
	task.CancelAttempt++
	if err = runner.recordSubtaskItem(ctx, turn.ID, task); err != nil {
		return Snapshot{}, err
	}
	fenced := false
	if ledger, loadErr := runner.loadTurnBudget(ctx, turn); loadErr != nil {
		return Snapshot{}, loadErr
	} else if ledger != nil {
		if _, err = runner.budgets.coordinator().CancelRun(ctx, turn.ID, task.RunID); err != nil {
			return Snapshot{}, err
		}
		fenced = true
	}
	_, found, cancelErr := runner.cancelRuntimeRun(ctx, task.RunID, reason)
	if cancelErr == nil && !found && fenced {
		task.Status, task.CancelStatus = "cancelled", "confirmed"
		if err = runner.recordSubtaskItem(ctx, turn.ID, task); err != nil {
			return Snapshot{}, err
		}
	}
	if cancelErr == nil {
		_, cancelErr = runner.cancelRelatedRuns(ctx, task.RunID, nil, reason)
	}
	if cancelErr != nil {
		task.CancelStatus, task.CancelError = "failed", cancelErr.Error()
		if err = runner.recordSubtaskItem(context.WithoutCancel(ctx), turn.ID, task); err != nil {
			return Snapshot{}, errors.Join(cancelErr, err)
		}
		return Snapshot{}, cancelErr
	}
	if err = runner.syncSubtaskItems(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

// ResolveSubtaskApproval requires the exact checkpoint shown to the user, so an
// old approval action cannot authorize a newer Tool call after resumption.
func (runner *Runner) ResolveSubtaskApproval(ctx context.Context, turnID, taskID, checkpointID string, request ResolveApprovalRequest) (Snapshot, error) {
	turn, task, err := runner.findSubtask(ctx, turnID, taskID)
	if err != nil {
		return Snapshot{}, err
	}
	if task.Approval == nil || task.Approval.CheckpointID != checkpointID || terminalTurnStatus(turn.Status) {
		return Snapshot{}, ErrConflict
	}
	decision, err := pluginApprovalDecision(request.Decision)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := runner.runtime.Load(ctx, task.RunID)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Checkpoint == nil || snapshot.Checkpoint.ID != checkpointID {
		return Snapshot{}, ErrConflict
	}
	resolved, resolveErr := runner.agent.ResolveApproval(ctx, task.RunID, snapshot.Run.Revision, plugin.ApprovalResponse{Decision: decision, Comment: request.Comment})
	if resolveErr != nil {
		return Snapshot{}, resolveErr
	}
	invocation, err := runner.store.GetInvocationByExecutionRefID(ctx, task.RunID)
	if errors.Is(err, ErrNotFound) {
		// Team, PlanExecute and Workflow retain their own child state machines;
		// not every child Agent is a separate Harness capability invocation.
		if err = runner.syncSubtaskItems(ctx, turn); err != nil {
			return Snapshot{}, err
		}
		return runner.loadSnapshot(ctx, turn, nil)
	}
	if err != nil {
		return Snapshot{}, err
	}
	pending, _, err := approvalRequestFromSnapshot(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordApprovalDecisionItem(ctx, turn, invocation, pending, request); err != nil {
		return Snapshot{}, err
	}
	if err = runner.syncSubtaskItems(ctx, turn); err != nil {
		return Snapshot{}, err
	}
	return runner.syncChildInvocationSnapshot(ctx, turn, invocation, resolved)
}
