package a2a

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const shadowResultContentType = "application/json"

var (
	ErrInvalidShadowRunner  = errors.New("invalid A2A shadow runner")
	ErrRemoteIdentityLost   = errors.New("A2A remote task identity is unavailable")
	ErrRemoteBindingChanged = errors.New("A2A remote binding changed")
	ErrRemoteSendFailed     = errors.New("A2A remote message send failed")
	ErrRemoteTaskFailed     = errors.New("A2A remote task failed")
	ErrRemoteInputRequired  = errors.New("A2A remote task is not waiting for input")
)

// RemoteClient is the minimum A2A client capability required by one local shadow Run.
type RemoteClient interface {
	SendMessage(context.Context, Discovery, SendRequest) (Interaction, error)
	GetTask(context.Context, Discovery, string) (TaskSnapshot, error)
	CancelTask(context.Context, Discovery, string) (TaskSnapshot, error)
}

// ShadowDependencies bind one runner to one explicitly discovered remote Agent.
type ShadowDependencies struct {
	Runtime        *kernel.Runtime
	Client         RemoteClient
	Discovery      Discovery
	TargetID       string
	TargetRevision string
}

// ShadowRunner implements the existing Handoff ChildRunner shape without
// importing Handoff or leaking A2A wire types into parent orchestrators.
type ShadowRunner struct {
	runtime   *kernel.Runtime
	client    RemoteClient
	discovery Discovery
	targetID  string
	revision  string
}

type shadowState struct {
	RemoteName       string `json:"remoteName"`
	TargetID         string `json:"targetID,omitempty"`
	TargetRevision   string `json:"targetRevision,omitempty"`
	RemoteURL        string `json:"remoteURL"`
	ProtocolVersion  string `json:"protocolVersion"`
	MessageID        string `json:"messageID"`
	MessageSequence  int    `json:"messageSequence,omitempty"`
	PendingMessageID string `json:"pendingMessageID,omitempty"`
	PendingInputHash string `json:"pendingInputHash,omitempty"`
	RemoteMessageID  string `json:"remoteMessageID,omitempty"`
	RemoteTaskID     string `json:"remoteTaskID,omitempty"`
	RemoteContextID  string `json:"remoteContextID,omitempty"`
	RemoteState      string `json:"remoteState,omitempty"`
}

// NewShadowRunner creates one A2A ChildRunner bound to one immutable discovery.
func NewShadowRunner(dependencies ShadowDependencies) (*ShadowRunner, error) {
	descriptor := dependencies.Discovery.Descriptor
	if dependencies.Runtime == nil || dependencies.Client == nil || strings.TrimSpace(descriptor.Name) == "" ||
		strings.TrimSpace(descriptor.PreferredURL) == "" || descriptor.ProtocolVersion != ProtocolVersion {
		return nil, ErrInvalidShadowRunner
	}
	return &ShadowRunner{
		runtime: dependencies.Runtime, client: dependencies.Client, discovery: cloneDiscovery(dependencies.Discovery),
		targetID: strings.TrimSpace(dependencies.TargetID), revision: strings.TrimSpace(dependencies.TargetRevision),
	}, nil
}

// StartRun creates the durable local shadow before sending the remote A2A message.
func (runner *ShadowRunner) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	runID := strings.TrimSpace(request.ID)
	goal := strings.TrimSpace(request.Goal)
	if runner == nil || runner.runtime == nil || runID == "" || goal == "" {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	state := runner.initialState(runID)
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := runner.runtime.Create(ctx, kernel.CreateRequest{
		ID: runID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "a2a.remote_started", Message: "A2A remote child started"}},
	})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	interaction, sendErr := runner.client.SendMessage(ctx, runner.discovery, SendRequest{
		MessageID: state.MessageID, Text: goal,
	})
	if sendErr != nil {
		failed, applyErr := runner.fail(ctx, snapshot, state, "a2a.send_failed", ErrRemoteSendFailed)
		return failed, errors.Join(sendErr, applyErr)
	}
	return runner.applyInteraction(ctx, snapshot, state, interaction)
}

// LoadRun loads the local shadow and refreshes a non-terminal remote Task when present.
func (runner *ShadowRunner) LoadRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil || !shadowRunRefreshable(snapshot.Run.Status) {
		return snapshot, err
	}
	state, err := runner.decodeState(snapshot)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if state.RemoteTaskID == "" {
		failed, applyErr := runner.fail(ctx, snapshot, state, "a2a.remote_identity_lost", ErrRemoteIdentityLost)
		return failed, errors.Join(ErrRemoteIdentityLost, applyErr)
	}
	remote, err := runner.client.GetTask(ctx, runner.discovery, state.RemoteTaskID)
	if err != nil {
		return snapshot, err
	}
	if remote.State == state.RemoteState && !remote.Terminal {
		return snapshot, nil
	}
	return runner.applyTask(ctx, snapshot, state, remote)
}

// CancelRun explicitly cancels a non-terminal remote Task and projects the result locally.
func (runner *ShadowRunner) CancelRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	snapshot, err := runner.LoadRun(ctx, runID)
	if err != nil || !shadowRunRefreshable(snapshot.Run.Status) {
		return snapshot, err
	}
	state, err := runner.decodeState(snapshot)
	if err != nil || state.RemoteTaskID == "" {
		return snapshot, errors.Join(err, ErrRemoteIdentityLost)
	}
	remote, err := runner.client.CancelTask(ctx, runner.discovery, state.RemoteTaskID)
	if err != nil {
		return snapshot, err
	}
	return runner.applyTask(ctx, snapshot, state, remote)
}

// ResumeRun continues a remote input-required or auth-required task with one
// durable, idempotently identified user message. Only a hash of the pending
// input is persisted locally; message content stays in the caller request.
func (runner *ShadowRunner) ResumeRun(ctx context.Context, runID, text string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(text) == "" {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	snapshot, err := runner.runtime.Load(ctx, strings.TrimSpace(runID))
	if err != nil {
		return snapshot, err
	}
	if snapshot.Run.Status != kernel.RunStatusWaitingInput {
		return snapshot, ErrRemoteInputRequired
	}
	state, err := runner.decodeState(snapshot)
	if err != nil || state.RemoteTaskID == "" || state.RemoteContextID == "" {
		return snapshot, errors.Join(err, ErrRemoteIdentityLost)
	}
	inputHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	if state.PendingMessageID == "" {
		state.MessageSequence++
		state.PendingMessageID = runID + ":message:" + strconv.Itoa(state.MessageSequence)
		state.PendingInputHash = inputHash
		snapshot, err = runner.markResumePending(ctx, snapshot, state)
		if err != nil {
			return snapshot, err
		}
	} else if state.PendingInputHash != inputHash {
		return snapshot, ErrRemoteInputRequired
	}
	interaction, sendErr := runner.client.SendMessage(ctx, runner.discovery, SendRequest{
		MessageID: state.PendingMessageID, ContextID: state.RemoteContextID,
		TaskID: state.RemoteTaskID, Text: text,
	})
	if sendErr != nil {
		waiting, applyErr := runner.resumeSendFailed(ctx, snapshot, state, sendErr)
		return waiting, errors.Join(sendErr, applyErr)
	}
	state.MessageID = state.PendingMessageID
	state.PendingMessageID = ""
	state.PendingInputHash = ""
	return runner.applyInteraction(ctx, snapshot, state, interaction)
}

func (runner *ShadowRunner) initialState(runID string) shadowState {
	descriptor := runner.discovery.Descriptor
	return shadowState{
		RemoteName: descriptor.Name, RemoteURL: descriptor.PreferredURL, ProtocolVersion: descriptor.ProtocolVersion,
		TargetID: runner.targetID, TargetRevision: runner.revision, MessageID: runID + ":message", MessageSequence: 1,
	}
}

func (runner *ShadowRunner) decodeState(snapshot kernel.Snapshot) (shadowState, error) {
	var state shadowState
	if json.Unmarshal(snapshot.State, &state) != nil || state.MessageID == "" || state.RemoteName == "" || state.RemoteURL == "" {
		return shadowState{}, ErrInvalidShadowRunner
	}
	descriptor := runner.discovery.Descriptor
	if state.RemoteName != descriptor.Name || state.RemoteURL != descriptor.PreferredURL ||
		state.ProtocolVersion != descriptor.ProtocolVersion || state.TargetID != runner.targetID ||
		state.TargetRevision != runner.revision {
		return shadowState{}, ErrRemoteBindingChanged
	}
	return state, nil
}

func (runner *ShadowRunner) applyInteraction(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	interaction Interaction,
) (kernel.Snapshot, error) {
	if interaction.Task != nil {
		return runner.applyTask(ctx, snapshot, state, *interaction.Task)
	}
	if interaction.Message == nil || len(interaction.Raw) == 0 {
		return runner.fail(ctx, snapshot, state, "a2a.invalid_result", ErrInvalidResult)
	}
	state.RemoteMessageID = interaction.Message.ID
	state.RemoteContextID = interaction.Message.ContextID
	return runner.complete(ctx, snapshot, state, interaction.Raw)
}

func (runner *ShadowRunner) applyTask(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	remote TaskSnapshot,
) (kernel.Snapshot, error) {
	if strings.TrimSpace(remote.ID) == "" || strings.TrimSpace(remote.ContextID) == "" || strings.TrimSpace(remote.State) == "" {
		return runner.fail(ctx, snapshot, state, "a2a.invalid_task", ErrInvalidTask)
	}
	state.RemoteTaskID = remote.ID
	state.RemoteContextID = remote.ContextID
	state.RemoteState = remote.State
	switch remote.State {
	case "TASK_STATE_COMPLETED":
		return runner.complete(ctx, snapshot, state, remote.Raw)
	case "TASK_STATE_FAILED", "TASK_STATE_REJECTED":
		return runner.fail(ctx, snapshot, state, "a2a.remote_failed", fmt.Errorf("%w: %s", ErrRemoteTaskFailed, remote.State))
	case "TASK_STATE_CANCELED":
		return runner.cancel(ctx, snapshot, state, remote.State)
	case "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED":
		return runner.waitForInput(ctx, snapshot, state)
	case "TASK_STATE_SUBMITTED", "TASK_STATE_WORKING":
		return runner.keepRunning(ctx, snapshot, state)
	default:
		return runner.fail(ctx, snapshot, state, "a2a.invalid_task_state", ErrInvalidTask)
	}
}

func shadowRunRefreshable(status kernel.RunStatus) bool {
	return status == kernel.RunStatusRunning || status == kernel.RunStatusWaitingInput
}

func (runner *ShadowRunner) waitForInput(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
) (kernel.Snapshot, error) {
	if snapshot.Run.Status == kernel.RunStatusWaitingInput {
		encoded, err := json.Marshal(state)
		if err != nil {
			return kernel.Snapshot{}, err
		}
		refreshed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded,
			Events: []kernel.EventDraft{{Type: "a2a.remote_wait_refreshed", Message: "A2A remote wait state changed"}},
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
		snapshot = refreshed
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	eventType := "a2a.remote_input_required"
	if state.RemoteState == "TASK_STATE_AUTH_REQUIRED" {
		eventType = "a2a.remote_auth_required"
	}
	checkpoint, err := runner.remoteCheckpoint(snapshot.Run.ID, state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded,
		Checkpoint: checkpoint,
		Events:     []kernel.EventDraft{{Type: eventType, Message: state.RemoteState}},
	})
}

func (runner *ShadowRunner) markResumePending(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
) (kernel.Snapshot, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "a2a.remote_resume_requested", Message: "A2A remote input submitted"}},
	})
}

func (runner *ShadowRunner) resumeSendFailed(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	_ error,
) (kernel.Snapshot, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	checkpoint, err := runner.remoteCheckpoint(snapshot.Run.ID, state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: encoded,
		Checkpoint: checkpoint,
		Events:     []kernel.EventDraft{{Type: "a2a.remote_resume_deferred", Message: "A2A remote resume deferred"}},
	})
}

func (runner *ShadowRunner) remoteCheckpoint(runID string, state shadowState) (*kernel.Checkpoint, error) {
	payload, err := json.Marshal(map[string]string{
		"remoteTaskID": state.RemoteTaskID, "remoteContextID": state.RemoteContextID,
		"remoteState": state.RemoteState,
	})
	if err != nil {
		return nil, err
	}
	kind := "a2a.input"
	if state.RemoteState == "TASK_STATE_AUTH_REQUIRED" {
		kind = "a2a.auth"
	}
	return &kernel.Checkpoint{
		ID: runID + ":remote-input", Kind: kind, Status: kernel.CheckpointPending,
		Payload: payload, CreatedAt: runner.runtime.Now(),
	}, nil
}

func (runner *ShadowRunner) keepRunning(ctx context.Context, snapshot kernel.Snapshot, state shadowState) (kernel.Snapshot, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded,
		Events: []kernel.EventDraft{{Type: "a2a.remote_updated", Message: state.RemoteState}},
	})
}

func (runner *ShadowRunner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	content json.RawMessage,
) (kernel.Snapshot, error) {
	encoded, err := json.Marshal(state)
	if err != nil || !json.Valid(content) {
		return kernel.Snapshot{}, errors.Join(err, ErrInvalidResult)
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: encoded,
		Result: &kernel.Result{ContentType: shadowResultContentType, Content: append(json.RawMessage(nil), content...)},
		Events: []kernel.EventDraft{{Type: "a2a.remote_completed", Message: "A2A remote child completed"}},
	})
}

func (runner *ShadowRunner) fail(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	code string,
	cause error,
) (kernel.Snapshot, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: encoded,
		ErrorCode: code, ErrorDetail: cause.Error(),
		Events: []kernel.EventDraft{{Type: "a2a.remote_failed", Message: cause.Error()}},
	})
}

func (runner *ShadowRunner) cancel(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	reason string,
) (kernel.Snapshot, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCancelled, State: encoded,
		ErrorCode: "a2a.remote_cancelled", ErrorDetail: strings.TrimSpace(reason),
		Events: []kernel.EventDraft{{Type: "a2a.remote_cancelled", Message: strings.TrimSpace(reason)}},
	})
}
