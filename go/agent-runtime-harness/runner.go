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
	capabilityStartFailedCode   = "harness.capability_start_failed"
)

// Clock supplies Harness orchestration time.
type Clock interface {
	Now() time.Time
}

func invocationLifecycleItemID(invocation Invocation, status InvocationStatus, revision uint64) string {
	return stableID("hivitem", invocation.ID, string(status), uintString(revision))
}

func invocationItemStatus(status InvocationStatus) ItemStatus {
	switch status {
	case InvocationWaitingInput:
		return ItemWaiting
	case InvocationCompleted:
		return ItemCompleted
	case InvocationFailed:
		return ItemFailed
	case InvocationCancelled:
		return ItemCancelled
	default:
		return ItemStarted
	}
}

func (runner *Runner) recordInvocationItem(ctx context.Context, invocation Invocation) error {
	if runner == nil || runner.store == nil || strings.TrimSpace(invocation.ID) == "" {
		return ErrInvalidRequest
	}
	now := runner.clock.Now().UTC()
	parentItemID := strings.TrimSpace(invocation.ParentItemID)
	if invocation.Revision > 1 {
		parentItemID = invocationLifecycleItemID(invocation, InvocationAccepted, 1)
	}
	payload, err := invocationItemPayload(invocation)
	if err != nil {
		return err
	}
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID:     invocationLifecycleItemID(invocation, invocation.Status, invocation.Revision),
		TurnID: invocation.TurnID, Kind: ItemInvocation, Status: invocationItemStatus(invocation.Status),
		RunID: invocation.ExecutionRefID, InvocationID: invocation.ID, ParentItemID: parentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func (runner *Runner) resumeDirectAgentStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	request StartRequest,
	config ConfigSnapshot,
) (Snapshot, error) {
	if snapshot, replayed, err := runner.replayDirectAgentStart(ctx, turn, invocation); replayed || err != nil {
		return snapshot, err
	}
	turn, runCtx, err := runner.resumeDirectAgentContext(ctx, turn, invocation, request.Context, config)
	if err != nil {
		return Snapshot{}, err
	}
	runtimeSnapshot, startErr := runner.agent.StartRun(runCtx, agent.StartRequest{
		ID: invocation.ExecutionRefID, Actor: request.Actor, Thread: request.Thread,
		RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), turn.ID), Goal: request.Goal,
		Model: config.Model, ModelOptions: append(json.RawMessage(nil), config.ModelOptions...),
		ToolKeys: append([]string(nil), config.ToolKeys...), RequiredToolKeys: append([]string(nil), request.RequiredToolKeys...),
		Limits: config.Limits,
	})
	if runtimeSnapshot.Run.ID == "" {
		failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
}

func (runner *Runner) replayDirectAgentStart(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
) (Snapshot, bool, error) {
	if terminalTurnStatus(turn.Status) || terminalInvocationStatus(invocation.Status) {
		snapshot, err := runner.loadSnapshot(ctx, turn, nil)
		return snapshot, true, err
	}
	loaded, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err == nil {
		snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, loaded)
		return snapshot, true, syncErr
	}
	if errors.Is(err, kernel.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return Snapshot{}, false, nil
	}
	return Snapshot{}, false, err
}

func (runner *Runner) resumeDirectAgentContext(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	seed *ContextSeed,
	config ConfigSnapshot,
) (Turn, context.Context, error) {
	updated, runCtx, err := runner.restoreOrBuildContext(ctx, turn, invocation.ExecutionRefID, seed, config)
	if err == nil {
		return updated, runCtx, nil
	}
	_, failErr := runner.failTopLevelInvocationAndTurn(ctx, turn, invocation, err)
	return Turn{}, nil, errors.Join(err, failErr)
}

func (runner *Runner) restoreOrBuildContext(
	ctx context.Context,
	turn Turn,
	executionRefID string,
	seed *ContextSeed,
	config ConfigSnapshot,
) (Turn, context.Context, error) {
	if snapshotID := strings.TrimSpace(turn.ContextSnapshotID); snapshotID != "" {
		snapshot, err := runner.store.GetContextSnapshot(ctx, snapshotID)
		if err != nil {
			return Turn{}, nil, err
		}
		ref := contextRef(snapshot)
		if strings.TrimSpace(snapshot.RunID) != strings.TrimSpace(executionRefID) || ref.ID != snapshotID || ref != turn.ContextRef {
			return Turn{}, nil, ErrConflict
		}
		if err = runner.recordContextItem(ctx, turn); err != nil {
			return Turn{}, nil, err
		}
		return turn, withContextSnapshot(ctx, snapshot), nil
	}
	if seed == nil {
		return turn, ctx, nil
	}
	snapshot, err := runner.buildContext(ctx, executionRefID, config, seed)
	if err != nil {
		return Turn{}, nil, err
	}
	return runner.attachContextSnapshot(ctx, turn, snapshot)
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
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil || turn.Status != TurnWaitingInput || invocation.ExecutionClass != ExecutionAgent ||
		strings.TrimSpace(invocation.ExecutionRefID) == "" {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
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
	if err = runner.recordApprovalDecisionItem(ctx, turn, invocation, pending, request); err != nil {
		return Snapshot{}, errors.Join(resolveErr, err)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshot(ctx, turn, invocation, resolved)
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
	Plans     PlanExecuteFeature
	Teams     TeamFeature
	Workflows WorkflowFeature
	Store     Store
	Clock     Clock
	TurnFeed  *TurnFeed
	Context   *runtimecontext.Builder
	Catalog   tools.Catalog
	Handoffs  HandoffStarter
	Relations runrelation.Recorder
}

// Runner owns durable Harness Session/Turn/Invocation/Item lifecycle. Runtime
// Feature execution is represented by durable Capability Invocations.
type Runner struct {
	runtime   *kernel.Runtime
	agent     AgentStarter
	plans     PlanExecuteFeature
	teams     TeamFeature
	workflows WorkflowFeature
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
		runtime: dependencies.Runtime, agent: dependencies.Agent,
		plans: dependencies.Plans, teams: dependencies.Teams, workflows: dependencies.Workflows,
		store: dependencies.Store, clock: dependencies.Clock,
		turnFeed: dependencies.TurnFeed,
		context:  dependencies.Context, catalog: dependencies.Catalog,
		handoffs: dependencies.Handoffs, relations: dependencies.Relations,
	}, nil
}

// Start starts the default direct-Agent capability as a durable top-level
// Invocation after Session/Turn/Config creation.
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
	invocation, err := newDirectAgentInvocation(
		turnID, firstNonEmpty(strings.TrimSpace(request.RequestID), turnID), request.Goal, now,
	)
	if err != nil {
		return Snapshot{}, err
	}
	invocation, invocationCreated, err := runner.store.CreateInvocation(ctx, invocation)
	if err != nil {
		return Snapshot{}, err
	}
	if created && !invocationCreated {
		return Snapshot{}, ErrConflict
	}
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return Snapshot{}, err
	}
	if !created {
		return runner.resumeDirectAgentStart(ctx, createdTurn, invocation, request, config)
	}
	if err = runner.recordHostMessageItems(ctx, createdTurn, request.InputMessage, request.OutputMessage); err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, createdTurn, EventTurnStarted, false)
	runCtx := ctx
	if request.Context != nil {
		contextSnapshot, buildErr := runner.buildContext(ctx, invocation.ExecutionRefID, config, request.Context)
		if buildErr != nil {
			failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, createdTurn, invocation, buildErr)
			return failed, errors.Join(buildErr, failErr)
		}
		createdTurn, runCtx, err = runner.attachContextSnapshot(ctx, createdTurn, contextSnapshot)
		if err != nil {
			return Snapshot{}, err
		}
	}
	runtimeSnapshot, startErr := runner.agent.StartRun(runCtx, agent.StartRequest{
		ID: invocation.ExecutionRefID, Actor: request.Actor, Thread: request.Thread,
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
		failed, failErr := runner.failTopLevelInvocationAndTurn(ctx, createdTurn, invocation, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshotWithRetry(ctx, createdTurn, invocation, runtimeSnapshot)
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

func (runner *Runner) attachContextSnapshot(
	ctx context.Context,
	turn Turn,
	snapshot runtimecontext.Snapshot,
) (Turn, context.Context, error) {
	if !validContextSnapshot(snapshot) {
		return Turn{}, nil, ErrInvalidRequest
	}
	if _, _, err := runner.store.PutContextSnapshot(ctx, snapshot); err != nil {
		return Turn{}, nil, err
	}
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

// Refresh synchronizes one active Harness Turn from its top-level Invocation.
func (runner *Runner) Refresh(ctx context.Context, turnID string) (Snapshot, error) {
	turn, err := runner.store.GetTurn(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Snapshot{}, err
	}
	if terminalTurnStatus(turn.Status) {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil || strings.TrimSpace(invocation.ExecutionRefID) == "" {
		return Snapshot{}, errors.Join(ErrConflict, err)
	}
	runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil {
		return Snapshot{}, err
	}
	return runner.syncRuntimeSnapshot(ctx, turn, invocation, runtimeSnapshot)
}

// Cancel cancels the active top-level Invocation and persists the resulting
// Harness Turn state. C2 extends dispatch to the other explicit Feature adapters.
func (runner *Runner) Cancel(ctx context.Context, turnID string, reason string) (Snapshot, error) {
	turnID = strings.TrimSpace(turnID)
	for range maxRuntimeSyncRetryAttempts {
		turn, err := runner.store.GetTurn(ctx, turnID)
		if err != nil {
			return Snapshot{}, err
		}
		if terminalTurnStatus(turn.Status) {
			return runner.loadSnapshot(ctx, turn, nil)
		}
		invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
		if err != nil || strings.TrimSpace(invocation.ExecutionRefID) == "" {
			return Snapshot{}, errors.Join(ErrConflict, err)
		}
		runtimeSnapshot, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
		if err != nil {
			return Snapshot{}, err
		}
		if terminalRuntimeStatus(runtimeSnapshot.Run.Status) {
			return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, runtimeSnapshot)
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
		return runner.syncRuntimeSnapshotWithRetry(ctx, turn, invocation, cancelled)
	}
	return Snapshot{}, ErrConflict
}

func (runner *Runner) syncRuntimeSnapshotWithRetry(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) (Snapshot, error) {
	for range maxRuntimeSyncRetryAttempts {
		snapshot, err := runner.syncRuntimeSnapshot(ctx, turn, invocation, runtimeSnapshot)
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
		invocation, err = runner.store.GetInvocation(ctx, invocation.ID)
		if err != nil {
			return Snapshot{}, err
		}
		runtimeSnapshot, err = runner.runtime.Load(ctx, invocation.ExecutionRefID)
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

func (runner *Runner) syncRuntimeSnapshot(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) (Snapshot, error) {
	status, err := turnStatusFromRuntime(runtimeSnapshot.Run.Status)
	if err != nil {
		return Snapshot{}, err
	}
	if runtimeSnapshotConflicts(invocation, runtimeSnapshot) {
		return Snapshot{}, ErrConflict
	}
	invocation, err = runner.syncInvocationProjection(ctx, invocation, runtimeSnapshot, status)
	if err != nil {
		return Snapshot{}, err
	}
	turn, turnChanged, err := runner.syncTurnProjection(ctx, turn, runtimeSnapshot, status)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordRuntimeSnapshotItems(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	deferTerminalProjection, err := runner.hostOutputProjectionPending(ctx, turn)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.finalizeTerminalRuntimeProjection(
		ctx, turn, runtimeSnapshot, turnChanged, deferTerminalProjection,
	); err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
}

func (runner *Runner) syncInvocationProjection(
	ctx context.Context,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
	status TurnStatus,
) (Invocation, error) {
	nextStatus := invocationStatusFromTurn(status)
	if nextStatus == invocation.Status && invocation.ErrorCode == runtimeSnapshot.Run.ErrorCode &&
		invocation.ErrorDetail == runtimeSnapshot.Run.ErrorDetail {
		if err := runner.recordInvocationItem(ctx, invocation); err != nil {
			return Invocation{}, err
		}
		return invocation, nil
	}
	invocation.Status = nextStatus
	invocation.ErrorCode = strings.TrimSpace(runtimeSnapshot.Run.ErrorCode)
	invocation.ErrorDetail = strings.TrimSpace(runtimeSnapshot.Run.ErrorDetail)
	invocation.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateInvocation(ctx, invocation, invocation.Revision)
	if err != nil {
		return Invocation{}, err
	}
	if err = runner.recordInvocationItem(ctx, updated); err != nil {
		return Invocation{}, err
	}
	return updated, nil
}

func (runner *Runner) syncTurnProjection(
	ctx context.Context,
	turn Turn,
	runtimeSnapshot kernel.Snapshot,
	status TurnStatus,
) (Turn, bool, error) {
	if !runtimeSnapshotChangesTurn(turn, runtimeSnapshot, status) {
		return turn, false, nil
	}
	turn.Status = status
	turn.ErrorCode = strings.TrimSpace(runtimeSnapshot.Run.ErrorCode)
	turn.ErrorDetail = strings.TrimSpace(runtimeSnapshot.Run.ErrorDetail)
	turn.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateTurn(ctx, turn, turn.Revision)
	if err != nil {
		return Turn{}, false, err
	}
	if !terminalTurnStatus(updated.Status) {
		runner.publishTurnStatus(ctx, updated, turnEventTypeForStatus(updated.Status), false)
	}
	return updated, true, nil
}

func (runner *Runner) recordRuntimeSnapshotItems(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runtimeSnapshot kernel.Snapshot,
) error {
	if invocation.ExecutionClass != ExecutionAgent {
		return runner.projectChildInvocationOutcome(ctx, invocation, runtimeSnapshot)
	}
	if err := runner.recordAgentRunItem(ctx, turn, invocation, runtimeSnapshot); err != nil {
		return err
	}
	return runner.recordApprovalRequestItem(ctx, turn, invocation, runtimeSnapshot)
}

func (runner *Runner) finalizeTerminalRuntimeProjection(
	ctx context.Context,
	turn Turn,
	runtimeSnapshot kernel.Snapshot,
	turnChanged bool,
	deferHostProjection bool,
) error {
	if !terminalTurnStatus(turn.Status) {
		return nil
	}
	if !deferHostProjection {
		if err := runner.recordTerminalAgentMessageItem(ctx, turn, runtimeSnapshot); err != nil {
			return err
		}
	}
	// Delegation relations are continuation signals as well as provenance. Project
	// them only after the synchronous root call stack has committed its terminal
	// state, otherwise a completed sibling can race the remaining Tool calls and
	// resume the same Agent revision concurrently.
	if err := runner.projectDelegationRelations(ctx, turn); err != nil {
		return err
	}
	if turnChanged && !deferHostProjection {
		runner.publishTurnStatus(ctx, turn, turnEventTypeForStatus(turn.Status), true)
	}
	return nil
}

func (runner *Runner) terminalRuntimeSnapshot(ctx context.Context, turn Turn) (kernel.Snapshot, error) {
	invocation, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if errors.Is(err, ErrNotFound) {
		return kernel.Snapshot{}, nil
	}
	if err != nil {
		return kernel.Snapshot{}, err
	}
	result := kernel.Snapshot{Run: kernel.Run{ID: invocation.ExecutionRefID}}
	executionRefID := strings.TrimSpace(invocation.ExecutionRefID)
	if executionRefID == "" {
		return result, nil
	}
	loaded, err := runner.runtime.Load(ctx, executionRefID)
	if err == nil {
		return loaded, nil
	}
	if turn.Status == TurnFailed {
		return result, nil
	}
	return kernel.Snapshot{}, err
}

// FinalizeHostOutput acknowledges that the host-owned output projection is
// durable. A Harness Turn with a bound host Assistant Message does not publish
// its terminal message/Turn events until this acknowledgement succeeds.
func (runner *Runner) FinalizeHostOutput(ctx context.Context, turnID string) (Snapshot, error) {
	turnID = strings.TrimSpace(turnID)
	if runner == nil || turnID == "" {
		return Snapshot{}, ErrInvalidRequest
	}
	turn, err := runner.store.GetTurn(ctx, turnID)
	if err != nil {
		return Snapshot{}, err
	}
	if !terminalTurnStatus(turn.Status) {
		return Snapshot{}, ErrConflict
	}
	pending, finalized, err := hostOutputProjectionState(ctx, runner.store, turn)
	if err != nil {
		return Snapshot{}, err
	}
	if finalized {
		return runner.loadSnapshot(ctx, turn, nil)
	}
	if !pending {
		return Snapshot{}, ErrConflict
	}
	runtimeSnapshot, err := runner.terminalRuntimeSnapshot(ctx, turn)
	if err != nil {
		return Snapshot{}, err
	}
	if err = runner.recordTerminalAgentMessageItem(ctx, turn, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	runner.publishTurnStatus(ctx, turn, turnEventTypeForStatus(turn.Status), true)
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
	invocation, _ := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	runID := firstNonEmpty(strings.TrimSpace(snapshot.Run.ID), strings.TrimSpace(invocation.ExecutionRefID))
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID:     stableID("him", turn.ID, runID, string(status), payload.ContentHash),
		TurnID: turn.ID, Kind: ItemAgentMessage, Status: status, RunID: runID, InvocationID: invocation.ID,
		HostRef: hostRef, ParentItemID: parentItemID, Payload: raw,
		CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func runtimeSnapshotConflicts(invocation Invocation, snapshot kernel.Snapshot) bool {
	executionRefID := strings.TrimSpace(invocation.ExecutionRefID)
	return executionRefID != "" && executionRefID != snapshot.Run.ID
}

func runtimeSnapshotChangesTurn(turn Turn, snapshot kernel.Snapshot, status TurnStatus) bool {
	return turn.Status != status || turn.ErrorCode != snapshot.Run.ErrorCode || turn.ErrorDetail != snapshot.Run.ErrorDetail
}

func (runner *Runner) recordApprovalRequestItem(ctx context.Context, turn Turn, invocation Invocation, snapshot kernel.Snapshot) error {
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
		Kind: ItemApproval, Status: ItemWaiting, RunID: snapshot.Run.ID, InvocationID: invocation.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func (runner *Runner) recordApprovalDecisionItem(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
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
		Kind: ItemApproval, Status: ItemCompleted, RunID: invocation.ExecutionRefID, InvocationID: invocation.ID,
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

func (runner *Runner) recordAgentRunItem(ctx context.Context, turn Turn, invocation Invocation, snapshot kernel.Snapshot) error {
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
		TurnID: turn.ID, Kind: ItemAgentRun, Status: status, RunID: snapshot.Run.ID, InvocationID: invocation.ID,
		ParentItemID: invocationLifecycleItemID(invocation, InvocationAccepted, 1),
		Payload:      payload, CreatedAt: now, UpdatedAt: now,
	}
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, item)
	return err
}

func (runner *Runner) failTopLevelInvocationAndTurn(ctx context.Context, turn Turn, invocation Invocation, cause error) (Snapshot, error) {
	invocation.Status = InvocationFailed
	invocation.ErrorCode = capabilityStartFailedCode
	invocation.ErrorDetail = "capability execution did not start"
	if cause == nil {
		invocation.ErrorDetail = "capability returned no execution identity"
	}
	invocation.UpdatedAt = runner.clock.Now().UTC()
	if updatedInvocation, err := runner.store.UpdateInvocation(ctx, invocation, invocation.Revision); err == nil {
		invocation = updatedInvocation
		if err = runner.recordInvocationItem(ctx, invocation); err != nil {
			return Snapshot{}, err
		}
	} else {
		return Snapshot{}, err
	}
	turn.Status = TurnFailed
	turn.ErrorCode = capabilityStartFailedCode
	turn.ErrorDetail = "top-level capability execution did not start"
	if cause == nil {
		turn.ErrorDetail = "top-level capability returned no execution identity"
	}
	turn.UpdatedAt = runner.clock.Now().UTC()
	updated, err := runner.store.UpdateTurn(ctx, turn, turn.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	pending, _, err := hostOutputProjectionState(ctx, runner.store, updated)
	if err != nil {
		return Snapshot{}, err
	}
	if !pending {
		if err = runner.recordTerminalAgentMessageItem(ctx, updated, kernel.Snapshot{Run: kernel.Run{ID: invocation.ExecutionRefID}}); err != nil {
			return Snapshot{}, err
		}
		runner.publishTurnStatus(ctx, updated, EventTurnFailed, true)
	}
	return runner.loadSnapshot(ctx, updated, nil)
}

func (runner *Runner) hostOutputProjectionPending(ctx context.Context, turn Turn) (bool, error) {
	if !terminalTurnStatus(turn.Status) {
		return false, nil
	}
	pending, _, err := hostOutputProjectionState(ctx, runner.store, turn)
	return pending, err
}

func hostOutputProjectionState(ctx context.Context, store Store, turn Turn) (bool, bool, error) {
	items, err := listAllItems(ctx, store, turn.ID)
	if err != nil {
		return false, false, err
	}
	startedID := hostOutputStartedMessageItemID(items)
	if startedID == "" {
		return false, false, nil
	}
	finalized := hostOutputLifecycleFinalized(items, startedID)
	return !finalized, finalized, nil
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
	input *HostRef,
	output *HostRef,
) error {
	now := runner.clock.Now().UTC()
	if input != nil {
		if _, err := appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hium", turn.ID, input.Kind, input.ID), TurnID: turn.ID,
			Kind: ItemUserMessage, Status: ItemCompleted, HostRef: input,
			Payload: json.RawMessage(`{"role":"user"}`), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
	}
	if output != nil {
		if _, err := appendItemFact(ctx, runner.store, runner.turnFeed, Item{
			ID: stableID("hiam", turn.ID, output.Kind, output.ID), TurnID: turn.ID,
			Kind: ItemAgentMessage, Status: ItemStarted, HostRef: output,
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
	items, err := listAllItems(ctx, runner.store, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	invocations, err := runner.store.ListInvocations(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	interactions, err := runner.store.ListInteractions(ctx, turn.ID)
	if err != nil {
		return Snapshot{}, err
	}
	result := Snapshot{
		Session: session, Turn: turn, Config: config, Invocations: invocations,
		Interactions: interactions, Items: items,
	}
	runtimeSnapshot := provided
	rootInvocation, hasRootInvocation := topLevelInvocation(invocations)
	if runtimeSnapshot == nil && hasRootInvocation && strings.TrimSpace(rootInvocation.ExecutionRefID) != "" {
		loaded, loadErr := runner.runtime.Load(ctx, rootInvocation.ExecutionRefID)
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
	value.Invocations = cloneInvocations(value.Invocations)
	value.Interactions = cloneInteractions(value.Interactions)
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
