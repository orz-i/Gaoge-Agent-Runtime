package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

const (
	defaultItemListLimit        = 500
	maxRuntimeSyncRetryAttempts = 8
)

// Clock supplies Harness orchestration time.
type Clock interface {
	Now() time.Time
}

// ResolveApproval resolves the active Tool approval using the durable Harness Turn identity.
func (runner *Runner) ResolveApproval(
	ctx context.Context,
	turnID string,
	request ResolveApprovalRequest,
) (Snapshot, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	if turn.Status != TurnWaitingInput || strings.TrimSpace(turn.RootRunID) == "" {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, turn.RootRunID)
	if err != nil {
		return Snapshot{}, err
	}
	pending, ok, err := approvalRequestFromSnapshot(runtimeSnapshot)
	if err != nil || !ok {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	decision, err := pluginApprovalDecision(request.Decision)
	if err != nil {
		return Snapshot{}, err
	}
	resolved, resolveErr := runner.agent.ResolveApproval(
		ctx, runtimeSnapshot.Run.ID, runtimeSnapshot.Run.Revision,
		plugin.ApprovalResponse{Decision: decision, Comment: strings.TrimSpace(request.Comment)},
	)
	if resolved.Run.ID == "" {
		return Snapshot{}, resolveErr
	}
	if err = runner.recordApprovalDecisionItem(ctx, turn, pending, request); err != nil {
		return Snapshot{}, errors.Join(resolveErr, err)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshot(ctx, turn, resolved)
	return snapshot, errors.Join(resolveErr, syncErr)
}

func (runner *Runner) buildContext(
	ctx context.Context,
	runID string,
	config ConfigSnapshot,
	seed *ContextSeed,
) (runtimecontext.Snapshot, error) {
	if runner.context == nil {
		return runtimecontext.Snapshot{}, ErrInvalidRequest
	}
	normalized, err := normalizeContextSeed(seed)
	if err != nil || normalized == nil {
		return runtimecontext.Snapshot{}, ErrInvalidRequest
	}
	definitions, err := buildContextTools(runner.catalog, config.ToolKeys)
	if err != nil {
		return runtimecontext.Snapshot{}, err
	}
	result, err := runner.context.Build(ctx, runtimecontext.BuildRequest{
		RunID: runID, Revision: 1, ThreadPathHash: normalized.ThreadPathHash,
		CurrentTurnID: normalized.CurrentTurnID,
		Prompt: runtimecontext.Prompt{
			Instructions: strings.TrimSpace(strings.Join([]string{config.Instructions, normalized.Instructions}, "\n\n")),
			Items:        append([]runtimecontext.Item(nil), normalized.Items...),
			Tools:        definitions, Options: defaultJSON(config.ModelOptions),
		},
		Budget: config.ContextBudget,
	})
	if err != nil {
		return runtimecontext.Snapshot{}, err
	}
	if result.Snapshot.RunID != runID || result.Snapshot.Revision != 1 ||
		result.Snapshot.ThreadPathHash != normalized.ThreadPathHash {
		return runtimecontext.Snapshot{}, ErrConflict
	}
	return result.Snapshot, nil
}

// AgentStarter is the narrow direct Agent capability required by Harness.
type AgentStarter interface {
	StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error)
	ResolveApproval(context.Context, string, uint64, plugin.ApprovalResponse) (kernel.Snapshot, error)
}

// Dependencies statically compose the minimal Harness core.
type Dependencies struct {
	Runtime   *kernel.Runtime
	Agent     AgentStarter
	Store     Store
	Clock     Clock
	TurnFeed  *TurnFeed
	Context   *runtimecontext.Builder
	Catalog   tools.Catalog
	Handoffs  HandoffStarter
	Relations runrelation.Recorder
}

// Runner owns durable Harness Session/Turn/Item lifecycle around one direct Agent root Run.
type Runner struct {
	runtime   *kernel.Runtime
	agent     AgentStarter
	store     Store
	clock     Clock
	turnFeed  *TurnFeed
	context   *runtimecontext.Builder
	catalog   tools.Catalog
	handoffs  HandoffStarter
	relations runrelation.Recorder
}

// StartRequest starts or idempotently reloads one Harness Turn.
type StartRequest struct {
	HostThread       HostRef
	HostTurn         HostRef
	InputMessage     *HostRef
	OutputMessage    *HostRef
	Actor            kernel.ActorRef
	Thread           kernel.ThreadRef
	RequestID        string
	RootRunID        string
	Goal             string
	RequiredToolKeys []string
	Config           ConfigSnapshot
	Context          *ContextSeed
}

// NewRunner constructs a minimal first-party Harness composition layer.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Agent == nil || dependencies.Store == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Runner{
		runtime: dependencies.Runtime, agent: dependencies.Agent, store: dependencies.Store, clock: dependencies.Clock,
		turnFeed: dependencies.TurnFeed,
		context:  dependencies.Context, catalog: dependencies.Catalog,
		handoffs: dependencies.Handoffs, relations: dependencies.Relations,
	}, nil
}

// Start starts one direct Agent root Run after durable Session/Turn/Config creation.
func (runner *Runner) Start(ctx context.Context, request StartRequest) (Snapshot, error) {
	request, sessionID, turnID, err := normalizeStartRequest(request)
	if err != nil {
		return Snapshot{}, err
	}
	now := runner.clock.Now().UTC()
	config, err := SealConfigSnapshot(turnID, request.Config, now)
	if err != nil {
		return Snapshot{}, err
	}
	createdTurn, created, err := runner.persistStartEnvelope(ctx, request, sessionID, turnID, config, now)
	if err != nil {
		return Snapshot{}, err
	}
	if !created {
		return runner.Load(ctx, createdTurn.ID)
	}
	rootRunID := startRootRunID(request.RootRunID, turnID)
	if err = runner.recordHostMessageItems(ctx, createdTurn, rootRunID, request.InputMessage, request.OutputMessage); err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, createdTurn, EventTurnStarted, false)
	runCtx := ctx
	if request.Context != nil {
		contextSnapshot, buildErr := runner.buildContext(ctx, rootRunID, config, request.Context)
		if buildErr != nil {
			failed, failErr := runner.failTurn(ctx, createdTurn, buildErr)
			return failed, errors.Join(buildErr, failErr)
		}
		createdTurn, runCtx, err = runner.attachContextSnapshot(ctx, createdTurn, contextSnapshot)
		if err != nil {
			return Snapshot{}, err
		}
	}
	createdTurn.RootRunID = rootRunID
	createdTurn.UpdatedAt = runner.clock.Now().UTC()
	createdTurn, err = runner.store.UpdateTurn(ctx, createdTurn, createdTurn.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	runtimeSnapshot, startErr := runner.agent.StartRun(runCtx, agent.StartRequest{
		ID: rootRunID, Actor: request.Actor, Thread: request.Thread,
		RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), turnID), Goal: request.Goal,
		Model: config.Model, ModelOptions: append(json.RawMessage(nil), config.ModelOptions...),
		ToolKeys:         append([]string(nil), config.ToolKeys...),
		RequiredToolKeys: append([]string(nil), request.RequiredToolKeys...), Limits: config.Limits,
	})
	if runtimeSnapshot.Run.ID == "" {
		if errors.Is(startErr, kernel.ErrConflict) {
			recovered, recoverErr := runner.Refresh(ctx, createdTurn.ID)
			if recoverErr == nil && recovered.Turn.Status == TurnCancelled {
				return recovered, nil
			}
		}
		failed, failErr := runner.failTurn(ctx, createdTurn, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, createdTurn, runtimeSnapshot)
	if snapshot.Turn.Status == TurnCancelled && errors.Is(startErr, kernel.ErrConflict) {
		startErr = nil
	}
	return snapshot, errors.Join(startErr, syncErr)
}

func (runner *Runner) persistStartEnvelope(
	ctx context.Context,
	request StartRequest,
	sessionID string,
	turnID string,
	config ConfigSnapshot,
	now time.Time,
) (Turn, bool, error) {
	session := Session{
		ID: sessionID, HostThread: request.HostThread, Actor: request.Actor,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := runner.store.CreateSession(ctx, session); err != nil {
		return Turn{}, false, err
	}
	if _, _, err := runner.store.PutConfigSnapshot(ctx, config); err != nil {
		return Turn{}, false, err
	}
	turn := Turn{
		ID: turnID, SessionID: sessionID, HostTurn: request.HostTurn,
		ConfigSnapshotID: config.ID, Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	return runner.store.CreateTurn(ctx, turn)
}

func startRootRunID(requested string, turnID string) string {
	if rootRunID := strings.TrimSpace(requested); rootRunID != "" {
		return rootRunID
	}
	return RootRunID(turnID)
}

func (runner *Runner) attachContextSnapshot(
	ctx context.Context,
	turn Turn,
	snapshot runtimecontext.Snapshot,
) (Turn, context.Context, error) {
	turn.ContextSnapshotID = snapshot.ID
	turn.ContextRef = contextRef(snapshot)
	turn.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateTurn(ctx, turn, turn.Revision)
	if err != nil {
		return Turn{}, nil, err
	}
	if err = runner.recordContextItem(ctx, updated); err != nil {
		return Turn{}, nil, err
	}
	return updated, withContextSnapshot(ctx, snapshot), nil
}

// Load returns one durable Harness Turn and current root output without changing execution state.
func (runner *Runner) Load(ctx context.Context, turnID string) (Snapshot, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, nil)
}

// Refresh synchronizes one active Harness Turn from the authoritative root Runtime snapshot.
func (runner *Runner) Refresh(ctx context.Context, turnID string) (Snapshot, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(turn.RootRunID) == "" || terminalTurnStatus(turn.Status) {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, turn.RootRunID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.syncRuntimeSnapshot(ctx, turn, runtimeSnapshot)
}

// Cancel cancels the direct Agent root Run and persists the resulting Harness Turn state.
func (runner *Runner) Cancel(ctx context.Context, turnID string, reason string) (Snapshot, error) {
	turnID = strings.TrimSpace(turnID)
	for range maxRuntimeSyncRetryAttempts {
		turn, err := runner.store.GetTurn(ctx, turnID)
		if err != nil {
			return Snapshot{}, err
		}
		if strings.TrimSpace(turn.RootRunID) == "" || terminalTurnStatus(turn.Status) {
			return runner.loadSnapshot(ctx, turn, nil)
		}
		runtimeSnapshot, err := runner.runtime.Load(ctx, turn.RootRunID)
		if err != nil {
			return Snapshot{}, err
		}
		if terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
			return runner.syncRuntimeSnapshotWithRetry(ctx, turn, runtimeSnapshot)
		}
		cancelled, err := runner.runtime.Cancel(
			ctx,
			runtimeSnapshot.Run.ID,
			runtimeSnapshot.Run.Revision,
			strings.TrimSpace(reason),
		)
		if errors.Is(err, kernel.ErrConflict) {
			continue
		}
		if err != nil {
			return Snapshot{}, err
		}
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, cancelled)
	}
	return Snapshot{}, ErrConflict
}

func (runner *Runner) syncRuntimeSnapshotWithRetry(
	ctx context.Context,
	turn Turn,
	runtimeSnapshot kernel.Snapshot,
) (Snapshot, error) {
	for range maxRuntimeSyncRetryAttempts {
		snapshot, err := runner.syncRuntimeSnapshot(ctx, turn, runtimeSnapshot)
		if !errors.Is(err, ErrConflict) {
			return snapshot, err
		}
		turn, err = runner.store.GetTurn(ctx, turn.ID)
		if err != nil {
			return Snapshot{}, err
		}
		if terminalTurnStatus(turn.Status) {
			return runner.loadSnapshot(ctx, turn, nil)
		}
		runtimeSnapshot, err = runner.runtime.Load(ctx, turn.RootRunID)
		if err != nil {
			return Snapshot{}, err
		}
	}
	return Snapshot{}, ErrConflict
}

func terminalRuntimeStatus(status kernel.RunStatus) bool {
	return status == kernel.RunStatusCompleted || status == kernel.RunStatusFailed ||
		status == kernel.RunStatusCancelled
}

func normalizeStartRequest(request StartRequest) (StartRequest, string, string, error) {
	var err error
	request.HostThread, err = normalizeHostRef(request.HostThread)
	if err != nil {
		return StartRequest{}, "", "", err
	}
	request.HostTurn, err = normalizeHostRef(request.HostTurn)
	if err != nil || !validActor(request.Actor) || strings.TrimSpace(request.Thread.Kind) == "" ||
		strings.TrimSpace(request.Thread.ID) == "" || strings.TrimSpace(request.Goal) == "" {
		return StartRequest{}, "", "", ErrInvalidRequest
	}
	request.Goal = strings.TrimSpace(request.Goal)
	if err = normalizeOptionalHostRef(&request.InputMessage); err != nil {
		return StartRequest{}, "", "", err
	}
	if err = normalizeOptionalHostRef(&request.OutputMessage); err != nil {
		return StartRequest{}, "", "", err
	}
	sessionID, err := SessionID(request.HostThread)
	if err != nil {
		return StartRequest{}, "", "", err
	}
	turnID, err := TurnID(sessionID, request.HostTurn)
	return request, sessionID, turnID, err
}

func (runner *Runner) syncRuntimeSnapshot(ctx context.Context, turn Turn, runtimeSnapshot kernel.Snapshot) (Snapshot, error) {
	status, err := turnStatusFromRuntime(runtimeSnapshot.Run.Status)
	if err != nil {
		return Snapshot{}, err
	}
	if runtimeSnapshotConflicts(turn, runtimeSnapshot) {
		return Snapshot{}, ErrConflict
	}
	turnChanged := runtimeSnapshotChangesTurn(turn, runtimeSnapshot, status)
	if turnChanged {
		turn.RootRunID = runtimeSnapshot.Run.ID
		turn.Status = status
		turn.ErrorCode = strings.TrimSpace(runtimeSnapshot.Run.ErrorCode)
		turn.ErrorDetail = strings.TrimSpace(runtimeSnapshot.Run.ErrorDetail)
		turn.UpdatedAt = runner.clock.Now().UTC()
		turn, err = runner.store.UpdateTurn(ctx, turn, turn.Revision)
		if err != nil {
			return Snapshot{}, err
		}
		if !terminalTurnStatus(turn.Status) {
			runner.publishTurnStatus(ctx, turn, turnEventTypeForStatus(turn.Status), false)
		}
	}
	if err = runner.recordAgentRunItem(ctx, turn, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordApprovalRequestItem(ctx, turn, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	if terminalTurnStatus(turn.Status) {
		if err = runner.recordTerminalAgentMessageItem(ctx, turn, runtimeSnapshot); err != nil {
			return Snapshot{}, err
		}
	}
	// Delegation relations are continuation signals as well as provenance. Project
	// them only after the synchronous root call stack has committed its terminal
	// state, otherwise a completed sibling can race the remaining Tool calls and
	// resume the same Agent revision concurrently.
	if terminalTurnStatus(turn.Status) {
		if err = runner.projectDelegationRelations(ctx, turn); err != nil {
			return Snapshot{}, err
		}
		if turnChanged {
			runner.publishTurnStatus(ctx, turn, turnEventTypeForStatus(turn.Status), true)
		}
	}
	return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
}

func (runner *Runner) recordTerminalAgentMessageItem(
	ctx context.Context,
	turn Turn,
	snapshot kernel.Snapshot,
) error {
	parentItemID, hostRef, err := activeAgentMessageBinding(ctx, runner.store, turn)
	if err != nil || parentItemID == "" {
		return err
	}
	status := ItemCompleted
	switch turn.Status {
	case TurnFailed:
		status = ItemFailed
	case TurnCancelled:
		status = ItemCancelled
	}
	payload := modelTimelinePayload{}
	if snapshot.Result != nil {
		payload.ContentHash = hashTimelineBytes(snapshot.Result.Content)
		payload.ContentBytes = len(snapshot.Result.Content)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID:     stableID("him", turn.ID, snapshot.Run.ID, string(status), payload.ContentHash),
		TurnID: turn.ID, Kind: ItemAgentMessage, Status: status, RunID: snapshot.Run.ID,
		HostRef: hostRef, ParentItemID: parentItemID, Payload: raw,
		CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func runtimeSnapshotConflicts(turn Turn, snapshot kernel.Snapshot) bool {
	rootRunID := strings.TrimSpace(turn.RootRunID)
	return rootRunID != "" && rootRunID != snapshot.Run.ID
}

func runtimeSnapshotChangesTurn(turn Turn, snapshot kernel.Snapshot, status TurnStatus) bool {
	return turn.RootRunID != snapshot.Run.ID || turn.Status != status ||
		turn.ErrorCode != snapshot.Run.ErrorCode || turn.ErrorDetail != snapshot.Run.ErrorDetail
}

func (runner *Runner) recordApprovalRequestItem(ctx context.Context, turn Turn, snapshot kernel.Snapshot) error {
	pending, ok, err := approvalRequestFromSnapshot(snapshot)
	if err != nil || !ok {
		return err
	}
	payload, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID: stableID("hia", turn.ID, pending.CheckpointID), TurnID: turn.ID,
		Kind: ItemApproval, Status: ItemWaiting, RunID: snapshot.Run.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func (runner *Runner) recordApprovalDecisionItem(
	ctx context.Context,
	turn Turn,
	pending approvalRequestItemPayload,
	request ResolveApprovalRequest,
) error {
	payload, err := json.Marshal(approvalDecisionItemPayload{
		CheckpointID: pending.CheckpointID, Decision: request.Decision, Comment: strings.TrimSpace(request.Comment),
	})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID: stableID("hia", turn.ID, pending.CheckpointID, "decision"), TurnID: turn.ID,
		Kind: ItemApproval, Status: ItemCompleted, RunID: turn.RootRunID,
		ParentItemID: stableID("hia", turn.ID, pending.CheckpointID),
		Payload:      payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func pluginApprovalDecision(value ApprovalDecision) (plugin.ApprovalDecision, error) {
	switch value {
	case ApprovalApprove:
		return plugin.ApprovalApprove, nil
	case ApprovalReject:
		return plugin.ApprovalReject, nil
	default:
		return "", ErrInvalidRequest
	}
}

func (runner *Runner) recordAgentRunItem(ctx context.Context, turn Turn, snapshot kernel.Snapshot) error {
	status := itemStatusFromTurn(turn.Status)
	payload, err := json.Marshal(struct {
		RuntimeRevision uint64 `json:"runtimeRevision"`
	}{RuntimeRevision: snapshot.Run.Revision})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	item := Item{
		ID:     stableID("hi", turn.ID, snapshot.Run.ID, string(status), uintString(snapshot.Run.Revision)),
		TurnID: turn.ID, Kind: ItemAgentRun, Status: status, RunID: snapshot.Run.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	}
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, item)
	return err
}

func (runner *Runner) failTurn(ctx context.Context, turn Turn, cause error) (Snapshot, error) {
	turn.Status = TurnFailed
	turn.ErrorCode = "harness.agent_start_failed"
	turn.ErrorDetail = "agent root run did not start"
	if cause == nil {
		turn.ErrorDetail = "agent root run returned no identity"
	}
	turn.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateTurn(ctx, turn, turn.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordTerminalAgentMessageItem(ctx, updated, kernel.Snapshot{Run: kernel.Run{ID: updated.RootRunID}}); err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, updated, EventTurnFailed, true)
	return runner.loadSnapshot(ctx, updated, nil)
}

func normalizeOptionalHostRef(value **HostRef) error {
	if value == nil || *value == nil {
		return nil
	}
	normalized, err := normalizeHostRef(**value)
	if err != nil {
		return err
	}
	*value = &normalized
	return nil
}

func (runner *Runner) recordHostMessageItems(
	ctx context.Context,
	turn Turn,
	runID string,
	input *HostRef,
	output *HostRef,
) error {
	now := runner.clock.Now().UTC()
	if input != nil {
		if _, err := appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hium", turn.ID, input.Kind, input.ID), TurnID: turn.ID,
			Kind: ItemUserMessage, Status: ItemCompleted, HostRef: input, RunID: runID,
			Payload: json.RawMessage(`{"role":"user"}`), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
	}
	if output != nil {
		if _, err := appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hiam", turn.ID, output.Kind, output.ID), TurnID: turn.ID,
			Kind: ItemAgentMessage, Status: ItemStarted, HostRef: output, RunID: runID,
			Payload: json.RawMessage(`{"role":"assistant"}`), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) publishTurnStatus(ctx context.Context, turn Turn, eventType string, terminal bool) {
	if runner == nil || runner.turnFeed == nil {
		return
	}
	_, _ = runner.turnFeed.Publish(ctx, turn.ID, TurnEventDraft{
		Type: eventType, Status: string(turn.Status), Message: turn.ErrorDetail, Terminal: terminal,
	})
}

func turnEventTypeForStatus(status TurnStatus) string {
	switch status {
	case TurnWaitingInput:
		return EventTurnWaitingInput
	case TurnCompleted:
		return EventTurnCompleted
	case TurnFailed:
		return EventTurnFailed
	case TurnCancelled:
		return EventTurnCancelled
	default:
		return EventTurnStarted
	}
}

// SubscribeTurnFeed returns retained semantic events followed by live events for
// one Harness Turn. Durable Items remain available through Load regardless of feed retention.
func (runner *Runner) SubscribeTurnFeed(ctx context.Context, turnID string, afterSeq int64) (*TurnSubscription, error) {
	turnID = strings.TrimSpace(turnID)
	if runner == nil || runner.turnFeed == nil || turnID == "" || afterSeq < 0 {
		return nil, ErrInvalidRequest
	}
	if _, err := runner.store.GetTurn(ctx, turnID); err != nil {
		return nil, err
	}
	return runner.turnFeed.Subscribe(ctx, turnID, afterSeq)
}

func (runner *Runner) loadSnapshot(ctx context.Context, turn Turn, provided *kernel.Snapshot) (Snapshot, error) {
	session, err := runner.store.GetSession(ctx, turn.SessionID)
	if err != nil {
		return Snapshot{}, err
	}
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	items, err := runner.store.ListItems(ctx, turn.ID, 0, defaultItemListLimit)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{Session: session, Turn: turn, Config: config, Items: items}
	runtimeSnapshot := provided
	if runtimeSnapshot == nil && strings.TrimSpace(turn.RootRunID) != "" {
		loaded, loadErr := runner.runtime.Load(ctx, turn.RootRunID)
		if loadErr != nil {
			return Snapshot{}, loadErr
		}
		runtimeSnapshot = &loaded
	}
	if runtimeSnapshot != nil && runtimeSnapshot.Result != nil {
		result.Output = &Output{
			ContentType: strings.TrimSpace(runtimeSnapshot.Result.ContentType),
			Content:     append(json.RawMessage(nil), runtimeSnapshot.Result.Content...),
		}
	}
	return cloneSnapshot(result), nil
}

func cloneSnapshot(value Snapshot) Snapshot {
	value.Session = cloneSession(value.Session)
	value.Config = cloneConfigSnapshot(value.Config)
	value.Items = cloneItems(value.Items)
	value.Output = cloneOutput(value.Output)
	return value
}

func itemStatusFromTurn(status TurnStatus) ItemStatus {
	switch status {
	case TurnCompleted:
		return ItemCompleted
	case TurnFailed:
		return ItemFailed
	case TurnCancelled:
		return ItemCancelled
	case TurnWaitingInput:
		return ItemWaiting
	default:
		return ItemStarted
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func uintString(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 20)
	for value > 0 {
		buffer = append(buffer, digits[value%10])
		value /= 10
	}
	for left, right := 0, len(buffer)-1; left < right; left, right = left+1, right-1 {
		buffer[left], buffer[right] = buffer[right], buffer[left]
	}
	return string(buffer)
}
