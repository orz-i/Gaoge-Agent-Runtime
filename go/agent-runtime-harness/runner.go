package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const defaultItemListLimit = 500

// Clock supplies Harness orchestration time.
type Clock interface {
	Now() time.Time
}

// AgentStarter is the narrow direct Agent capability required by Harness.
type AgentStarter interface {
	StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error)
}

// Dependencies statically compose the minimal Harness core.
type Dependencies struct {
	Runtime *kernel.Runtime
	Agent   AgentStarter
	Store   Store
	Clock   Clock
}

// Runner owns durable Harness Session/Turn/Item lifecycle around one direct Agent root Run.
type Runner struct {
	runtime *kernel.Runtime
	agent   AgentStarter
	store   Store
	clock   Clock
}

// StartRequest starts or idempotently reloads one Harness Turn.
type StartRequest struct {
	HostThread HostRef
	HostTurn   HostRef
	Actor      kernel.ActorRef
	Thread     kernel.ThreadRef
	RequestID  string
	RootRunID  string
	Goal       string
	Config     ConfigSnapshot
}

// NewRunner constructs a minimal first-party Harness composition layer.
func NewRunner(dependencies Dependencies) (*Runner, error) {
	if dependencies.Runtime == nil || dependencies.Agent == nil || dependencies.Store == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRequest
	}
	return &Runner{runtime: dependencies.Runtime, agent: dependencies.Agent, store: dependencies.Store, clock: dependencies.Clock}, nil
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
	session := Session{
		ID: sessionID, HostThread: request.HostThread, Actor: request.Actor,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err = runner.store.CreateSession(ctx, session); err != nil {
		return Snapshot{}, err
	}
	if _, _, err = runner.store.PutConfigSnapshot(ctx, config); err != nil {
		return Snapshot{}, err
	}
	turn := Turn{
		ID: turnID, SessionID: sessionID, HostTurn: request.HostTurn,
		ConfigSnapshotID: config.ID, Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	createdTurn, created, err := runner.store.CreateTurn(ctx, turn)
	if err != nil {
		return Snapshot{}, err
	}
	if !created {
		return runner.Load(ctx, createdTurn.ID)
	}
	rootRunID := strings.TrimSpace(request.RootRunID)
	if rootRunID == "" {
		rootRunID = RootRunID(turnID)
	}
	runtimeSnapshot, startErr := runner.agent.StartRun(ctx, agent.StartRequest{
		ID: rootRunID, Actor: request.Actor, Thread: request.Thread,
		RequestID: firstNonEmpty(strings.TrimSpace(request.RequestID), turnID), Goal: request.Goal,
		Model: config.Model, ModelOptions: append(json.RawMessage(nil), config.ModelOptions...),
		ToolKeys: append([]string(nil), config.ToolKeys...), Limits: config.Limits,
	})
	if runtimeSnapshot.Run.ID == "" {
		failed, failErr := runner.failTurn(ctx, createdTurn, startErr)
		return failed, errors.Join(startErr, failErr)
	}
	snapshot, syncErr := runner.syncRuntimeSnapshot(ctx, createdTurn, runtimeSnapshot)
	return snapshot, errors.Join(startErr, syncErr)
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
	cancelled, err := runner.runtime.Cancel(ctx, runtimeSnapshot.Run.ID, runtimeSnapshot.Run.Revision, strings.TrimSpace(reason))
	if err != nil {
		return Snapshot{}, err
	}
	return runner.syncRuntimeSnapshot(ctx, turn, cancelled)
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
	if strings.TrimSpace(turn.RootRunID) != "" && turn.RootRunID != runtimeSnapshot.Run.ID {
		return Snapshot{}, ErrConflict
	}
	changed := turn.RootRunID != runtimeSnapshot.Run.ID || turn.Status != status ||
		turn.ErrorCode != runtimeSnapshot.Run.ErrorCode || turn.ErrorDetail != runtimeSnapshot.Run.ErrorDetail
	if changed {
		turn.RootRunID = runtimeSnapshot.Run.ID
		turn.Status = status
		turn.ErrorCode = strings.TrimSpace(runtimeSnapshot.Run.ErrorCode)
		turn.ErrorDetail = strings.TrimSpace(runtimeSnapshot.Run.ErrorDetail)
		turn.UpdatedAt = runner.clock.Now().UTC()
		turn, err = runner.store.UpdateTurn(ctx, turn, turn.Revision)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if err = runner.recordAgentRunItem(ctx, turn, runtimeSnapshot); err != nil {
		return Snapshot{}, err
	}
	return runner.loadSnapshot(ctx, turn, &runtimeSnapshot)
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
	_, _, err = runner.store.AppendItem(ctx, item)
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
	return runner.loadSnapshot(ctx, updated, nil)
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
