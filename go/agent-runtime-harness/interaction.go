package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// InteractionKind describes a host-renderable input shape without embedding
// application business semantics in Harness.
type InteractionKind string

const (
	InteractionChoice       InteractionKind = "choice"
	InteractionConfirmation InteractionKind = "confirmation"
	InteractionInput        InteractionKind = "input"
)

// InteractionStatus is the durable lifecycle of one generic input request.
type InteractionStatus string

const (
	InteractionWaiting  InteractionStatus = "waiting"
	InteractionResolved InteractionStatus = "resolved"
)

// Interaction is a generic durable input boundary. Schema/Presentation are UI
// contracts only. The application that owns the referenced artifact persists
// the business decision separately.
type Interaction struct {
	ID           string            `json:"id"`
	TurnID       string            `json:"turnID"`
	InvocationID string            `json:"invocationID"`
	ParentItemID string            `json:"parentItemID,omitempty"`
	Key          string            `json:"key"`
	Kind         InteractionKind   `json:"kind"`
	Schema       json.RawMessage   `json:"schema"`
	Presentation json.RawMessage   `json:"presentation,omitempty"`
	Status       InteractionStatus `json:"status"`
	Response     json.RawMessage   `json:"response,omitempty"`
	Revision     uint64            `json:"revision"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

type RequestInteraction struct {
	InvocationID string
	ParentItemID string
	Key          string
	Kind         InteractionKind
	Schema       json.RawMessage
	Presentation json.RawMessage
}

type ResolveInteractionRequest struct {
	Response json.RawMessage
}

func interactionID(turnID, invocationID, key string) string {
	return stableID("hinteraction", strings.TrimSpace(turnID), strings.TrimSpace(invocationID), strings.TrimSpace(key))
}

func validInteraction(value Interaction) bool {
	if !validInteractionIdentity(value) || !validInteractionLifecycle(value) {
		return false
	}
	return validInteractionJSON(value.Schema, true) && validInteractionJSON(value.Presentation, false) &&
		validInteractionJSON(value.Response, false)
}

func validInteractionIdentity(value Interaction) bool {
	return strings.TrimSpace(value.ID) != "" && strings.TrimSpace(value.TurnID) != "" &&
		strings.TrimSpace(value.InvocationID) != "" && strings.TrimSpace(value.Key) != ""
}

func validInteractionLifecycle(value Interaction) bool {
	return validInteractionKind(value.Kind) && validInteractionStatus(value.Status) && value.Revision > 0 &&
		!value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero()
}

func validInteractionKind(value InteractionKind) bool {
	return value == InteractionChoice || value == InteractionConfirmation || value == InteractionInput
}

func validInteractionStatus(value InteractionStatus) bool {
	return value == InteractionWaiting || value == InteractionResolved
}

func validInteractionJSON(value json.RawMessage, required bool) bool {
	if len(value) == 0 {
		return !required
	}
	return json.Valid(value)
}

func sameInteractionIdentity(left, right Interaction) bool {
	return left.ID == right.ID && left.TurnID == right.TurnID && left.InvocationID == right.InvocationID &&
		left.ParentItemID == right.ParentItemID && left.Key == right.Key && left.Kind == right.Kind &&
		bytes.Equal(left.Schema, right.Schema) && bytes.Equal(left.Presentation, right.Presentation)
}

func cloneInteraction(value Interaction) Interaction {
	value.Schema = append(json.RawMessage(nil), value.Schema...)
	value.Presentation = append(json.RawMessage(nil), value.Presentation...)
	value.Response = append(json.RawMessage(nil), value.Response...)
	return value
}

func cloneInteractions(values []Interaction) []Interaction {
	result := make([]Interaction, len(values))
	for index, value := range values {
		result[index] = cloneInteraction(value)
	}
	return result
}

// RequestInteraction durably suspends one Invocation and its owning Harness Turn.
// Replays reconcile every durable projection so a crash between Store writes
// cannot strand the Turn in a partially-applied waiting transition.
func (runner *Runner) RequestInteraction(
	ctx context.Context,
	turnID string,
	request RequestInteraction,
) (Snapshot, error) {
	if err := validateInteractionRequestContract(request); err != nil {
		return Snapshot{}, err
	}
	turn, invocation, err := runner.loadInteractionRequestOwners(ctx, turnID, request.InvocationID)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.validateParentItem(ctx, turn.ID, request.ParentItemID); err != nil {
		return Snapshot{}, err
	}
	now := runner.clock.Now().UTC()
	candidate := Interaction{
		ID: interactionID(turn.ID, invocation.ID, request.Key), TurnID: turn.ID, InvocationID: invocation.ID,
		ParentItemID: strings.TrimSpace(request.ParentItemID), Key: strings.TrimSpace(request.Key), Kind: request.Kind,
		Schema: append(json.RawMessage(nil), request.Schema...), Presentation: append(json.RawMessage(nil), request.Presentation...),
		Status: InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	interaction, _, err := runner.store.CreateInteraction(ctx, candidate, turn.Revision, invocation.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if !sameInteractionIdentity(interaction, candidate) {
		return Snapshot{}, ErrConflict
	}
	if interaction.Status == InteractionResolved {
		return runner.loadInteractionSnapshot(ctx, interaction)
	}
	return runner.reconcileInteractionState(ctx, interaction, InvocationWaitingInput, TurnWaitingInput, EventTurnWaitingInput)
}

// ResolveInteraction persists generic input only. Application business meaning
// remains outside Harness; this method only makes the generic lifecycle
// crash-recoverable and returns the Invocation to running state.
func (runner *Runner) ResolveInteraction(
	ctx context.Context,
	turnID string,
	interactionID string,
	request ResolveInteractionRequest,
) (Snapshot, error) {
	if len(request.Response) == 0 || !json.Valid(request.Response) {
		return Snapshot{}, ErrInvalidRequest
	}
	interaction, err := runner.store.GetInteraction(ctx, strings.TrimSpace(interactionID))
	if err != nil || interaction.TurnID != strings.TrimSpace(turnID) {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	if interaction.Status == InteractionResolved {
		if !bytes.Equal(interaction.Response, request.Response) {
			return Snapshot{}, ErrConflict
		}
		return runner.reconcileInteractionState(ctx, interaction, InvocationRunning, TurnRunning, EventTurnStarted)
	}
	interaction.Response = append(json.RawMessage(nil), request.Response...)
	interaction.Status = InteractionResolved
	interaction.UpdatedAt = runner.clock.Now().UTC()
	interaction, err = runner.store.UpdateInteraction(ctx, interaction, interaction.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.reconcileInteractionState(ctx, interaction, InvocationRunning, TurnRunning, EventTurnStarted)
}

func (runner *Runner) loadInteractionRequestOwners(
	ctx context.Context,
	turnID, invocationID string,
) (Turn, Invocation, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil || terminalTurnStatus(turn.Status) {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil || invocation.TurnID != turn.ID || terminalInvocationStatus(invocation.Status) {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	return turn, invocation, nil
}

func validateInteractionRequestContract(request RequestInteraction) error {
	if strings.TrimSpace(request.InvocationID) == "" || strings.TrimSpace(request.Key) == "" {
		return ErrInvalidRequest
	}
	if !validInteractionKind(request.Kind) || !validInteractionJSON(request.Schema, true) ||
		!validInteractionJSON(request.Presentation, false) {
		return ErrInvalidRequest
	}
	return nil
}

func (runner *Runner) reconcileInteractionState(
	ctx context.Context,
	interaction Interaction,
	invocationStatus InvocationStatus,
	turnStatus TurnStatus,
	eventType string,
) (Snapshot, error) {
	if err := runner.recordInteractionItem(ctx, interaction); err != nil {
		return Snapshot{}, err
	}
	if err := runner.reconcileInvocationStatus(ctx, interaction.InvocationID, invocationStatus); err != nil {
		return Snapshot{}, err
	}
	turn, changed, err := runner.reconcileTurnStatus(ctx, interaction.TurnID, turnStatus)
	if err != nil {
		return Snapshot{}, err
	}
	if changed {
		runner.publishTurnStatus(ctx, turn, eventType, false)
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) loadInteractionSnapshot(ctx context.Context, interaction Interaction) (Snapshot, error) {
	turn, err := runner.store.GetTurn(ctx, interaction.TurnID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) reconcileInvocationStatus(
	ctx context.Context,
	invocationID string,
	status InvocationStatus,
) error {
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(invocationID))
	if err != nil {
		return err
	}
	if terminalInvocationStatus(invocation.Status) {
		return ErrConflict
	}
	if invocation.Status != status {
		invocation.Status = status
		invocation.UpdatedAt = runner.clock.Now().UTC()
		invocation, err = runner.store.UpdateInvocation(ctx, invocation, invocation.Revision)
		if err != nil {
			return err
		}
	}
	return runner.recordInvocationItem(ctx, invocation)
}

func (runner *Runner) reconcileTurnStatus(
	ctx context.Context,
	turnID string,
	status TurnStatus,
) (Turn, bool, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Turn{}, false, err
	}
	if terminalTurnStatus(turn.Status) {
		return Turn{}, false, ErrConflict
	}
	if turn.Status == status {
		return turn, false, nil
	}
	turn.Status = status
	turn.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateTurn(ctx, turn, turn.Revision)
	return updated, err == nil, err
}

func (runner *Runner) recordInteractionItem(ctx context.Context, interaction Interaction) error {
	payload, err := interactionItemPayload(interaction)
	if err != nil {
		return err
	}
	status := ItemWaiting
	if interaction.Status == InteractionResolved {
		status = ItemCompleted
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID:     stableID("hinteractionitem", interaction.ID, string(interaction.Status)),
		TurnID: interaction.TurnID, Kind: ItemInteraction, Status: status,
		InvocationID: interaction.InvocationID, ParentItemID: interaction.ParentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func interactionItemPayload(interaction Interaction) (json.RawMessage, error) {
	return json.Marshal(struct {
		InteractionID string            `json:"interactionID"`
		Key           string            `json:"key"`
		Kind          InteractionKind   `json:"kind"`
		Schema        json.RawMessage   `json:"schema"`
		Presentation  json.RawMessage   `json:"presentation,omitempty"`
		Status        InteractionStatus `json:"status"`
		Response      json.RawMessage   `json:"response,omitempty"`
	}{
		interaction.ID, interaction.Key, interaction.Kind, interaction.Schema,
		interaction.Presentation, interaction.Status, interaction.Response,
	})
}
