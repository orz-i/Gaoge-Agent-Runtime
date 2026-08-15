package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

type HandoffStarter interface {
	StartOrLoad(context.Context, kernel.Snapshot, handoff.Delegation) (handoff.Delegation, error)
}

// DelegateRequest asks Harness to start one stable specialist Child Agent.
// Child Tool access is intentionally empty in Phase A; delegation cannot widen the root Turn's permissions.
type DelegateRequest struct {
	MemberID string `json:"memberID"`
	Goal     string `json:"goal"`
}

type DelegationResult struct {
	Delegation handoff.Delegation `json:"delegation"`
	Snapshot   Snapshot           `json:"snapshot"`
}

type delegationItemPayload struct {
	DelegationID string          `json:"delegationID"`
	MemberID     string          `json:"memberID"`
	ChildRunID   string          `json:"childRunID"`
	Goal         string          `json:"goal"`
	Status       handoff.Status  `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
}

func normalizeDelegateRequest(request DelegateRequest) (DelegateRequest, error) {
	request.MemberID = strings.TrimSpace(request.MemberID)
	request.Goal = strings.TrimSpace(request.Goal)
	if request.MemberID == "" || request.Goal == "" || len(request.MemberID) > 64 || len(request.Goal) > 200_000 {
		return DelegateRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func (runner *Runner) Delegate(ctx context.Context, turnID string, request DelegateRequest) (DelegationResult, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return DelegationResult{}, err
	}
	return runner.delegate(ctx, turn, request)
}

// DelegateByRootRunID is the Tool-facing form used by a root Agent without exposing Harness Turn IDs to the model.
func (runner *Runner) DelegateByRootRunID(ctx context.Context, rootRunID string, request DelegateRequest) (DelegationResult, error) {
	turn, err := runner.store.GetTurnByRootRunID(ctx, strings.TrimSpace(rootRunID))
	if err != nil {
		return DelegationResult{}, err
	}
	return runner.delegate(ctx, turn, request)
}

func (runner *Runner) delegate(ctx context.Context, turn Turn, request DelegateRequest) (DelegationResult, error) {
	request, err := normalizeDelegateRequest(request)
	if err != nil || !runner.canDelegate(turn) {
		return DelegationResult{}, errors.Join(ErrInvalidRequest, err)
	}
	delegation, parent, err := runner.prepareDelegation(ctx, turn, request)
	if err != nil {
		return DelegationResult{}, err
	}
	startedItemID, err := runner.recordDelegationItem(ctx, turn, delegation, ItemStarted, "")
	if err != nil {
		return DelegationResult{}, err
	}
	return runner.executeDelegation(ctx, turn, parent, delegation, startedItemID)
}

func (runner *Runner) canDelegate(turn Turn) bool {
	return runner != nil && runner.handoffs != nil && runner.relations != nil && strings.TrimSpace(turn.RootRunID) != ""
}

func (runner *Runner) prepareDelegation(
	ctx context.Context,
	turn Turn,
	request DelegateRequest,
) (handoff.Delegation, kernel.Snapshot, error) {
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return handoff.Delegation{}, kernel.Snapshot{}, err
	}
	parent, err := runner.runtime.Load(ctx, turn.RootRunID)
	if err != nil {
		return handoff.Delegation{}, kernel.Snapshot{}, err
	}
	delegationID := stableID("hd", turn.ID, request.MemberID, request.Goal)
	childRunID := stableID("hchild", turn.RootRunID, delegationID)
	return handoff.Delegation{
		ID: delegationID, MemberID: request.MemberID, ChildRunID: childRunID,
		Goal: request.Goal, Model: config.Model, ModelOptions: append(json.RawMessage(nil), config.ModelOptions...),
		Status: handoff.StatusQueued,
	}, parent, nil
}

func (runner *Runner) executeDelegation(
	ctx context.Context,
	turn Turn,
	parent kernel.Snapshot,
	delegation handoff.Delegation,
	startedItemID string,
) (DelegationResult, error) {
	delegation, delegateErr := runner.handoffs.StartOrLoad(ctx, parent, delegation)
	status := delegationItemStatus(delegation.Status)
	if _, itemErr := runner.recordDelegationItem(ctx, turn, delegation, status, startedItemID); itemErr != nil {
		return DelegationResult{}, errors.Join(delegateErr, itemErr)
	}
	snapshot, loadErr := runner.Load(ctx, turn.ID)
	if errors.Is(delegateErr, handoff.ErrChildPending) {
		delegateErr = nil
	}
	return DelegationResult{Delegation: delegation, Snapshot: snapshot}, errors.Join(delegateErr, loadErr)
}

func (runner *Runner) projectDelegationRelations(ctx context.Context, turn Turn) error {
	items, err := runner.store.ListItems(ctx, turn.ID, 0, defaultItemListLimit)
	if err != nil {
		return err
	}
	projected := make(map[string]struct{})
	for _, item := range items {
		if item.Kind != ItemDelegation {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return err
		}
		if _, exists := projected[payload.DelegationID]; exists {
			continue
		}
		if err = runner.ensureDelegationRelation(ctx, turn.RootRunID, handoff.Delegation{
			ID: payload.DelegationID, ChildRunID: payload.ChildRunID,
		}); err != nil {
			return err
		}
		projected[payload.DelegationID] = struct{}{}
	}
	return nil
}

func (runner *Runner) ensureDelegationRelation(ctx context.Context, parentRunID string, delegation handoff.Delegation) error {
	_, err := runner.relations.Ensure(ctx, runrelation.Draft{
		ParentRunID: parentRunID, ChildRunID: delegation.ChildRunID,
		Kind: runrelation.KindDelegation, OwnerNodeID: delegation.ID,
	})
	return err
}

func (runner *Runner) recordDelegationItem(
	ctx context.Context,
	turn Turn,
	delegation handoff.Delegation,
	status ItemStatus,
	parentItemID string,
) (string, error) {
	payload, err := json.Marshal(delegationItemPayload{
		DelegationID: delegation.ID, MemberID: delegation.MemberID, ChildRunID: delegation.ChildRunID,
		Goal: delegation.Goal, Status: delegation.Status, Result: append(json.RawMessage(nil), delegation.Result...),
	})
	if err != nil {
		return "", err
	}
	itemID := stableID("hid", turn.ID, delegation.ID, string(status))
	now := runner.clock.Now().UTC()
	_, _, err = runner.store.AppendItem(ctx, Item{
		ID: itemID, TurnID: turn.ID, Kind: ItemDelegation, Status: status,
		RunID: delegation.ChildRunID, ParentItemID: parentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return itemID, err
}

func delegationItemStatus(status handoff.Status) ItemStatus {
	switch status {
	case handoff.StatusCompleted:
		return ItemCompleted
	case handoff.StatusFailed:
		return ItemFailed
	case handoff.StatusCancelled:
		return ItemCancelled
	default:
		return ItemStarted
	}
}
