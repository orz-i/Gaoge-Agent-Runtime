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
func (runner *Runner) RequestInteraction(
	ctx context.Context,
	turnID string,
	request RequestInteraction,
) (Snapshot, error) {
	turn, invocation, err := runner.validateInteractionRequest(ctx, turnID, request)
	if err != nil {
		return Snapshot{}, err
	}
	now := runner.clock.Now().UTC()
	interaction := Interaction{
		ID: interactionID(turn.ID, invocation.ID, request.Key), TurnID: turn.ID, InvocationID: invocation.ID,
		ParentItemID: strings.TrimSpace(request.ParentItemID), Key: strings.TrimSpace(request.Key), Kind: request.Kind,
		Schema: append(json.RawMessage(nil), request.Schema...), Presentation: append(json.RawMessage(nil), request.Presentation...),
		Status: InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	interaction, fresh, err := runner.store.CreateInteraction(ctx, interaction)
	if err != nil {
		return Snapshot{}, err
	}
	if !fresh {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	if err = runner.recordInteractionItem(ctx, interaction); err != nil {
		return Snapshot{}, err
	}
	if err = runner.setInvocationStatus(ctx, invocation, InvocationWaitingInput); err != nil {
		return Snapshot{}, err
	}
	turn, err = runner.setTurnStatus(ctx, turn, TurnWaitingInput)
	if err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnWaitingInput, false)
	return runner.loadSnapshot(ctx, turn, nil)
}

// ResolveInteraction persists generic input only. It deliberately does not
// persist an application AuthorDecision and does not resume a Runtime Feature.
func (runner *Runner) ResolveInteraction(
	ctx context.Context,
	turnID string,
	interactionID string,
	request ResolveInteractionRequest,
) (Snapshot, error) {
	interaction, replay, handled, err := runner.prepareInteractionResolution(ctx, turnID, interactionID, request)
	if err != nil || handled {
		return replay, err
	}
	return runner.resolvePendingInteraction(ctx, interaction, request.Response)
}

func (runner *Runner) prepareInteractionResolution(
	ctx context.Context,
	turnID, interactionID string,
	request ResolveInteractionRequest,
) (Interaction, Snapshot, bool, error) {
	if len(request.Response) == 0 || !json.Valid(request.Response) {
		return Interaction{}, Snapshot{}, true, ErrInvalidRequest
	}
	interaction, err := runner.store.GetInteraction(ctx, strings.TrimSpace(interactionID))
	if err != nil || interaction.TurnID != strings.TrimSpace(turnID) {
		return Interaction{}, Snapshot{}, true, errors.Join(ErrConflict, err)
	}
	if interaction.Status != InteractionResolved {
		return interaction, Snapshot{}, false, nil
	}
	if !bytes.Equal(interaction.Response, request.Response) {
		return Interaction{}, Snapshot{}, true, ErrConflict
	}
	snapshot, err := runner.loadInteractionSnapshot(ctx, interaction)
	return Interaction{}, snapshot, true, err
}

func (runner *Runner) resolvePendingInteraction(
	ctx context.Context,
	interaction Interaction,
	response json.RawMessage,
) (Snapshot, error) {
	turn, invocation, err := runner.loadInteractionOwners(ctx, interaction)
	if err != nil {
		return Snapshot{}, err
	}
	interaction.Response = append(json.RawMessage(nil), response...)
	interaction.Status = InteractionResolved
	interaction.UpdatedAt = runner.clock.Now().UTC()
	interaction, err = runner.store.UpdateInteraction(ctx, interaction, interaction.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordInteractionItem(ctx, interaction); err != nil {
		return Snapshot{}, err
	}
	if err = runner.setInvocationStatus(ctx, invocation, InvocationRunning); err != nil {
		return Snapshot{}, err
	}
	turn, err = runner.setTurnStatus(ctx, turn, TurnRunning)
	if err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, turn, EventTurnStarted, false)
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) validateInteractionRequest(
	ctx context.Context,
	turnID string,
	request RequestInteraction,
) (Turn, Invocation, error) {
	if err := validateInteractionRequestContract(request); err != nil {
		return Turn{}, Invocation{}, err
	}
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil || turn.Status != TurnRunning {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	invocation, err := runner.store.GetInvocation(ctx, strings.TrimSpace(request.InvocationID))
	if err != nil || invocation.TurnID != turn.ID || terminalInvocationStatus(invocation.Status) ||
		invocation.Status == InvocationWaitingInput {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	if err = runner.validateParentItem(ctx, turn.ID, request.ParentItemID); err != nil {
		return Turn{}, Invocation{}, err
	}
	if err = runner.ensureNoWaitingInteraction(ctx, turn.ID); err != nil {
		return Turn{}, Invocation{}, err
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

func (runner *Runner) ensureNoWaitingInteraction(ctx context.Context, turnID string) error {
	interactions, err := runner.store.ListInteractions(ctx, turnID)
	if err != nil {
		return err
	}
	for _, value := range interactions {
		if value.Status == InteractionWaiting {
			return ErrConflict
		}
	}
	return nil
}

func (runner *Runner) loadInteractionOwners(
	ctx context.Context,
	interaction Interaction,
) (Turn, Invocation, error) {
	turn, err := runner.store.GetTurn(ctx, interaction.TurnID)
	if err != nil || turn.Status != TurnWaitingInput {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	invocation, err := runner.store.GetInvocation(ctx, interaction.InvocationID)
	if err != nil || invocation.Status != InvocationWaitingInput {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	return turn, invocation, nil
}

func (runner *Runner) loadInteractionSnapshot(ctx context.Context, interaction Interaction) (Snapshot, error) {
	turn, err := runner.store.GetTurn(ctx, interaction.TurnID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

func (runner *Runner) setInvocationStatus(
	ctx context.Context,
	invocation Invocation,
	status InvocationStatus,
) error {
	invocation.Status = status
	invocation.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateInvocation(ctx, invocation, invocation.Revision)
	if err != nil {
		return err
	}
	if err = runner.recordInvocationItem(ctx, updated); err != nil {
		return err
	}
	return nil
}

func (runner *Runner) setTurnStatus(ctx context.Context, turn Turn, status TurnStatus) (Turn, error) {
	turn.Status = status
	turn.UpdatedAt = runner.clock.Now().UTC()
	return runner.store.UpdateTurn(ctx, turn, turn.Revision)
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
