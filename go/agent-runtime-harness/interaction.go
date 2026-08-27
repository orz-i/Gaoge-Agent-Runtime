package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
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
	ID             string            `json:"id"`
	TurnID         string            `json:"turnID"`
	InvocationID   string            `json:"invocationID"`
	ParentItemID   string            `json:"parentItemID,omitempty"`
	ApplicationRef *HostRef          `json:"applicationRef,omitempty"`
	ArtifactRefs   []HostRef         `json:"artifactRefs"`
	Key            string            `json:"key"`
	Kind           InteractionKind   `json:"kind"`
	Schema         json.RawMessage   `json:"schema"`
	Presentation   json.RawMessage   `json:"presentation,omitempty"`
	Status         InteractionStatus `json:"status"`
	Response       json.RawMessage   `json:"response,omitempty"`
	Revision       uint64            `json:"revision"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

func (runner *Runner) interactionResponseContext(
	ctx context.Context,
	interaction Interaction,
	response json.RawMessage,
) (InteractionResponseContext, error) {
	turn, invocation, err := runner.loadResolvedInteractionOwners(ctx, interaction)
	if err != nil {
		return InteractionResponseContext{}, err
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return InteractionResponseContext{}, err
	}
	candidate := cloneInteraction(interaction)
	candidate.Response = append(json.RawMessage(nil), response...)
	return InteractionResponseContext{Interaction: candidate, Invocation: cloneInvocation(invocation), Session: session}, nil
}

type RequestInteraction struct {
	InvocationID   string
	ParentItemID   string
	ApplicationRef *HostRef
	ArtifactRefs   []HostRef
	Key            string
	Kind           InteractionKind
	Schema         json.RawMessage
	Presentation   json.RawMessage
}

type ResolveInteractionRequest struct {
	Response json.RawMessage
}

// InteractionResponseContext contains the durable application references and
// authenticated Harness Session needed to persist the meaning of one generic
// response without teaching Harness any application-specific schema.
type InteractionResponseContext struct {
	Interaction Interaction
	Invocation  Invocation
	Session     Session
}

type InteractionResponseResult struct {
	OutputRefs []HostRef
}

// InteractionResponseHandler persists the application-owned meaning of one
// resolved generic response. Implementations must be idempotent by Interaction
// identity and response because crash recovery can invoke the handler again.
type InteractionResponseHandler interface {
	HandleInteractionResponse(context.Context, InteractionResponseContext) (InteractionResponseResult, error)
}

// InteractionResponseValidator optionally rejects an application response
// before the durable Interaction transitions to resolved. This prevents an
// invalid business choice from permanently consuming the only waiting input.
type InteractionResponseValidator interface {
	ValidateInteractionResponse(context.Context, InteractionResponseContext) error
}

// WorkflowWaitInteractionContext contains the immutable Workflow wait and its
// durable Harness owners. Applications project the wait into business UI
// semantics without teaching Harness about product-specific artifacts.
type WorkflowWaitInteractionContext struct {
	Wait       workflow.WaitRequest
	Invocation Invocation
	Session    Session
}

// WorkflowWaitInteractionProjection is the host-renderable business contract
// for one explicit Workflow wait. Harness supplies the durable owner identity.
type WorkflowWaitInteractionProjection struct {
	ApplicationRef *HostRef
	ArtifactRefs   []HostRef
	Key            string
	Kind           InteractionKind
	Schema         json.RawMessage
	Presentation   json.RawMessage
}

// WorkflowWaitInteractionProjector optionally maps a generic Workflow wait to
// an application-owned interaction. Implementations must be deterministic for
// the same immutable wait because recovery can project it repeatedly.
type WorkflowWaitInteractionProjector interface {
	ProjectWorkflowWaitInteraction(context.Context, WorkflowWaitInteractionContext) (WorkflowWaitInteractionProjection, error)
}

// ErrInteractionResponseHandlerUnavailable reports missing static application
// composition for a response that has already been durably accepted.
var ErrInteractionResponseHandlerUnavailable = errors.New("harness interaction response handler unavailable")

// InteractionResolution is the atomically persisted response plus the still
// waiting owners that may only resume after application handling succeeds.
type InteractionResolution struct {
	Interaction Interaction
	Invocation  Invocation
	Turn        Turn
}

func interactionID(turnID, invocationID, key string) string {
	return stableID("hinteraction", strings.TrimSpace(turnID), strings.TrimSpace(invocationID), strings.TrimSpace(key))
}

func validInteraction(value Interaction) bool {
	if !validInteractionIdentity(value) || !validInteractionLifecycle(value) {
		return false
	}
	return validInteractionJSON(value.Schema, true) && validInteractionJSON(value.Presentation, false) &&
		validInteractionJSON(value.Response, false) && validInteractionReferences(value.ApplicationRef, value.ArtifactRefs)
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
		sameOptionalHostRef(left.ApplicationRef, right.ApplicationRef) && sameHostRefs(left.ArtifactRefs, right.ArtifactRefs) &&
		bytes.Equal(left.Schema, right.Schema) && bytes.Equal(left.Presentation, right.Presentation)
}

func cloneInteraction(value Interaction) Interaction {
	if value.ApplicationRef != nil {
		ref := *value.ApplicationRef
		value.ApplicationRef = &ref
	}
	value.ArtifactRefs = append([]HostRef(nil), value.ArtifactRefs...)
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
	applicationRef, artifactRefs, err := normalizeInteractionReferences(request.ApplicationRef, request.ArtifactRefs)
	if err != nil {
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
		ApplicationRef: applicationRef, ArtifactRefs: artifactRefs,
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

// ResolveInteraction durably records generic input, delegates its business
// meaning to the statically composed application handler, then resumes the
// exact owning execution before projecting it back to running.
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
		return runner.replayResolvedInteraction(ctx, interaction)
	}
	if runner.interactions == nil {
		return Snapshot{}, ErrInteractionResponseHandlerUnavailable
	}
	validationContext, err := runner.interactionResponseContext(ctx, interaction, request.Response)
	if err != nil {
		return Snapshot{}, err
	}
	if validator, ok := runner.interactions.(InteractionResponseValidator); ok {
		if err = validator.ValidateInteractionResponse(ctx, validationContext); err != nil {
			return Snapshot{}, err
		}
	}
	interaction.Response = append(json.RawMessage(nil), request.Response...)
	interaction.Status = InteractionResolved
	interaction.UpdatedAt = runner.clock.Now().UTC()
	resolution, err := runner.store.ResolveInteraction(ctx, interaction, interaction.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.continueResolvedInteraction(ctx, resolution.Interaction)
}

func (runner *Runner) replayResolvedInteraction(ctx context.Context, interaction Interaction) (Snapshot, error) {
	return runner.continueResolvedInteraction(ctx, interaction)
}

func (runner *Runner) continueResolvedInteraction(ctx context.Context, interaction Interaction) (Snapshot, error) {
	if err := runner.recordInteractionItem(ctx, interaction); err != nil {
		return Snapshot{}, err
	}
	turn, invocation, snapshot, handled, err := runner.prepareResolvedInteractionContinuation(ctx, interaction)
	if err != nil || handled {
		return snapshot, err
	}
	if runner.interactions == nil {
		return Snapshot{}, ErrInteractionResponseHandlerUnavailable
	}
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return Snapshot{}, err
	}
	result, err := runner.interactions.HandleInteractionResponse(ctx, InteractionResponseContext{
		Interaction: cloneInteraction(interaction), Invocation: cloneInvocation(invocation), Session: session,
	})
	if err != nil {
		return Snapshot{}, err
	}
	if invocation.ExecutionClass == ExecutionApplication {
		return runner.finishResolvedApplicationInteraction(ctx, turn, invocation, result)
	}
	turn, invocation, snapshot, handled, err = runner.prepareResolvedInteractionContinuation(ctx, interaction)
	if err != nil || handled {
		return snapshot, err
	}
	return runner.resumeResolvedInteractionOwner(ctx, turn, invocation, interaction.Response)
}

func (runner *Runner) prepareResolvedInteractionContinuation(
	ctx context.Context,
	interaction Interaction,
) (Turn, Invocation, Snapshot, bool, error) {
	turn, invocation, err := runner.loadResolvedInteractionOwners(ctx, interaction)
	if err != nil {
		return Turn{}, Invocation{}, Snapshot{}, false, err
	}
	if terminalTurnStatus(turn.Status) {
		snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
		return turn, invocation, snapshot, true, loadErr
	}
	waiting, err := runner.hasOtherWaitingInteraction(ctx, interaction)
	if err != nil {
		return Turn{}, Invocation{}, Snapshot{}, false, err
	}
	if waiting || interactionOwnersRunning(turn, invocation) {
		snapshot, loadErr := runner.loadSnapshot(ctx, turn, nil)
		return turn, invocation, snapshot, true, loadErr
	}
	if terminalInvocationStatus(invocation.Status) {
		snapshot, finishErr := runner.finishResolvedInteractionOwner(ctx, turn, invocation, kernel.Snapshot{})
		return turn, invocation, snapshot, true, finishErr
	}
	if !interactionOwnerCanResume(turn.Status, invocation.Status) {
		return Turn{}, Invocation{}, Snapshot{}, false, ErrConflict
	}
	return turn, invocation, Snapshot{}, false, nil
}

func (runner *Runner) loadResolvedInteractionOwners(
	ctx context.Context,
	interaction Interaction,
) (Turn, Invocation, error) {
	turn, err := runner.store.GetTurn(ctx, interaction.TurnID)
	if err != nil {
		return Turn{}, Invocation{}, err
	}
	invocation, err := runner.store.GetInvocation(ctx, interaction.InvocationID)
	if err != nil || invocation.TurnID != turn.ID {
		return Turn{}, Invocation{}, errors.Join(ErrConflict, err)
	}
	return turn, invocation, nil
}

func (runner *Runner) hasOtherWaitingInteraction(ctx context.Context, interaction Interaction) (bool, error) {
	interactions, err := runner.store.ListInteractions(ctx, interaction.TurnID)
	if err != nil {
		return false, err
	}
	for _, candidate := range interactions {
		if candidate.ID != interaction.ID && candidate.Status == InteractionWaiting {
			return true, nil
		}
	}
	return false, nil
}

func interactionOwnerCanResume(turnStatus TurnStatus, invocationStatus InvocationStatus) bool {
	turnResumable := turnStatus == TurnRunning || turnStatus == TurnWaitingInput
	invocationResumable := invocationStatus == InvocationRunning || invocationStatus == InvocationWaitingInput
	return turnResumable && invocationResumable
}

func interactionOwnersRunning(turn Turn, invocation Invocation) bool {
	return turn.Status == TurnRunning && invocation.Status == InvocationRunning
}

func (runner *Runner) resumeResolvedInteractionOwner(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	response json.RawMessage,
) (Snapshot, error) {
	if invocation.ExecutionClass == ExecutionApplication {
		return runner.resumeApplicationInteractionOwner(ctx, turn, invocation)
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.resumeRuntimeInteractionOwner(ctx, turn, invocation, runtimeSnapshot, response)
}

func (runner *Runner) resumeApplicationInteractionOwner(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, error) {
	if err := runner.reconcileInvocationStatus(ctx, invocation.ID, InvocationRunning); err != nil {
		return Snapshot{}, err
	}
	updated, changed, err := runner.reconcileTurnStatus(ctx, turn.ID, TurnRunning)
	if err != nil {
		return Snapshot{}, err
	}
	if changed {
		runner.publishTurnStatus(ctx, updated, EventTurnStarted, false)
	}
	return runner.loadSnapshot(ctx, updated, nil)
}

func (runner *Runner) resumeRuntimeInteractionOwner(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	response json.RawMessage,
) (Snapshot, error) {
	if runtimeSnapshot.Run.Status == kernel.RunStatusWaitingInput {
		if invocation.ExecutionClass != ExecutionWorkflow || runner.workflows == nil || len(response) == 0 {
			return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
		}
		resolved, resolveErr := runner.workflows.ResolveWait(
			ctx, invocation.ExecutionRefID, runtimeSnapshot.Run.Revision, append(json.RawMessage(nil), response...),
		)
		if resolved.Run.ID == "" {
			return Snapshot{}, resolveErr
		}
		snapshot, syncErr := runner.finishResolvedInteractionOwner(ctx, turn, invocation, resolved)
		return snapshot, normalizedFeatureStartError(invocation.ExecutionClass, resolved, resolveErr, syncErr)
	}
	if terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
		return runner.finishResolvedInteractionOwner(ctx, turn, invocation, runtimeSnapshot)
	}
	if runtimeSnapshot.Run.Status != kernel.RunStatusRunning {
		return Snapshot{}, ErrConflict
	}
	runCtx, err := runner.restoreInvocationContext(ctx, turn, invocation)
	if err != nil {
		return Snapshot{}, err
	}
	resumed, resumeErr := runner.resumeInvocationExecution(runCtx, invocation, runtimeSnapshot.Run.Revision)
	if resumed.Run.ID == "" {
		return Snapshot{}, resumeErr
	}
	snapshot, syncErr := runner.finishResolvedInteractionOwner(ctx, turn, invocation, resumed)
	if invocation.ExecutionClass == ExecutionAgent {
		return snapshot, errors.Join(resumeErr, syncErr)
	}
	return snapshot, normalizedFeatureStartError(invocation.ExecutionClass, resumed, resumeErr, syncErr)
}

func (runner *Runner) finishResolvedInteractionOwner(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) (Snapshot, error) {
	if strings.TrimSpace(invocation.ParentItemID) == "" {
		if runtimeSnapshot.Run.ID == "" {
			loaded, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
			if err != nil {
				return Snapshot{}, err
			}
			runtimeSnapshot = loaded
		}
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
	}
	if runtimeSnapshot.Run.ID != "" {
		if _, err := runner.syncChildInvocationSnapshot(ctx, turn, invocation, runtimeSnapshot); err != nil {
			return Snapshot{}, err
		}
	}
	updated, changed, err := runner.reconcileTurnStatus(ctx, turn.ID, TurnRunning)
	if errors.Is(err, ErrConflict) {
		return runner.loadInteractionSnapshot(ctx, Interaction{TurnID: turn.ID})
	}
	if err != nil {
		return Snapshot{}, err
	}
	if changed {
		runner.publishTurnStatus(ctx, updated, EventTurnStarted, false)
	}
	return runner.loadSnapshot(ctx, updated, nil)
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
		!validInteractionJSON(request.Presentation, false) || !validInteractionReferences(request.ApplicationRef, request.ArtifactRefs) {
		return ErrInvalidRequest
	}
	return nil
}

func normalizeInteractionReferences(applicationRef *HostRef, artifactRefs []HostRef) (*HostRef, []HostRef, error) {
	var application *HostRef
	if applicationRef != nil {
		normalized, err := normalizeHostRef(*applicationRef)
		if err != nil {
			return nil, nil, err
		}
		application = &normalized
	}
	artifacts := make([]HostRef, len(artifactRefs))
	for index, value := range artifactRefs {
		normalized, err := normalizeHostRef(value)
		if err != nil {
			return nil, nil, err
		}
		artifacts[index] = normalized
	}
	return application, artifacts, nil
}

func validInteractionReferences(applicationRef *HostRef, artifactRefs []HostRef) bool {
	application, artifacts, err := normalizeInteractionReferences(applicationRef, artifactRefs)
	if err != nil || !sameOptionalHostRef(applicationRef, application) || !sameHostRefs(artifactRefs, artifacts) {
		return false
	}
	return true
}

func sameOptionalHostRef(left, right *HostRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameHostRefs(left, right []HostRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
		InteractionID  string            `json:"interactionID"`
		ApplicationRef *HostRef          `json:"applicationRef,omitempty"`
		ArtifactRefs   []HostRef         `json:"artifactRefs"`
		Key            string            `json:"key"`
		Kind           InteractionKind   `json:"kind"`
		Schema         json.RawMessage   `json:"schema"`
		Presentation   json.RawMessage   `json:"presentation,omitempty"`
		Status         InteractionStatus `json:"status"`
		Response       json.RawMessage   `json:"response,omitempty"`
	}{
		interaction.ID, interaction.ApplicationRef, append([]HostRef(nil), interaction.ArtifactRefs...),
		interaction.Key, interaction.Kind, interaction.Schema,
		interaction.Presentation, interaction.Status, interaction.Response,
	})
}
