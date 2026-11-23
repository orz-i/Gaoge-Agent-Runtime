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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const shadowResultContentType = "application/json"

var (
	ErrInvalidShadowRunner = errors.New("invalid A2A shadow runner")
	ErrRemoteIdentityLost  = errors.New("A2A remote task identity is unavailable")
	// An initial Send/response cannot prove no remote task was started.
	// Preserve the original local shadow and stable MessageID for operator
	// reconciliation; never mint a terminal failed/cancelled HMAC receipt.
	ErrRemoteOutcomeUnknown = errors.New("A2A remote execution outcome is unverified")
	ErrRemoteBindingChanged = errors.New("A2A remote binding changed")
	ErrRemoteTaskFailed     = errors.New("A2A remote task failed")
	ErrRemoteInputRequired  = errors.New("A2A remote task is not waiting for input")
	// A failed GET is not the adapter's own unresolved physical-send proof.
	// Keep these distinctions private and never expose upstream credentials.
	errRemoteReadUnavailable = errors.New("A2A remote task observation is unavailable")
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
	// Stable secret, at least 32 bytes; never persist or expose it in Kernel.
	AttestationKey []byte `json:"-"`
}

// ShadowRunner implements the existing Handoff ChildRunner shape without
// importing Handoff or leaking A2A wire types into parent orchestrators.
type ShadowRunner struct {
	runtime        *kernel.Runtime
	client         RemoteClient
	discovery      Discovery
	targetID       string
	revision       string
	attestationKey []byte
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
	// A committed send-intent is an exclusive physical admission. Until its
	// original callback returns and durably seals this bit, no observer or
	// canceller may certify a terminal state: the physical send may still
	// begin after the observer/canceller returns.
	PendingMessageReturned bool `json:"pendingMessageReturned,omitempty"`
	// Task cancellation uses the same exclusive CAS protocol as input Send.
	// An uncertain CancelTask must not be physically retried.
	PendingCancel         bool   `json:"pendingCancel,omitempty"`
	PendingCancelReturned bool   `json:"pendingCancelReturned,omitempty"`
	RemoteMessageID       string `json:"remoteMessageID,omitempty"`
	RemoteTaskID          string `json:"remoteTaskID,omitempty"`
	RemoteContextID       string `json:"remoteContextID,omitempty"`
	RemoteState           string `json:"remoteState,omitempty"`
	RemotePrompt          string `json:"remotePrompt,omitempty"`
	Proof                 string `json:"proof,omitempty"`
}

// decodeShadowIdentityState reads the persisted identity for the original
// remote ChildRunner. This is an execution-proof prerequisite; it does not
// authorize a network call or substitute for the frozen ChildRunner proof.
func decodeShadowIdentityState(raw json.RawMessage) (shadowState, error) {
	var state shadowState
	if json.Unmarshal(raw, &state) != nil || strings.TrimSpace(state.RemoteName) == "" ||
		strings.TrimSpace(state.RemoteURL) == "" || strings.TrimSpace(state.ProtocolVersion) == "" {
		return shadowState{}, ErrRemoteBindingChanged
	}
	return state, nil
}

// NewShadowRunner creates one A2A ChildRunner bound to one immutable discovery.
func NewShadowRunner(dependencies ShadowDependencies) (*ShadowRunner, error) {
	descriptor := dependencies.Discovery.Descriptor
	if dependencies.Runtime == nil || dependencies.Client == nil || len(dependencies.AttestationKey) < 32 || strings.TrimSpace(descriptor.Name) == "" ||
		strings.TrimSpace(descriptor.PreferredURL) == "" || descriptor.ProtocolVersion != ProtocolVersion {
		return nil, ErrInvalidShadowRunner
	}
	return &ShadowRunner{
		runtime: dependencies.Runtime, client: dependencies.Client, discovery: cloneDiscovery(dependencies.Discovery),
		targetID: strings.TrimSpace(dependencies.TargetID), revision: strings.TrimSpace(dependencies.TargetRevision),
		attestationKey: append([]byte(nil), dependencies.AttestationKey...),
	}, nil
}

// ObservesFrozenRemoteShadow is the first-party, read-only ownership
// attestation used by Harness receipt and cancellation checks. It makes no
// network call and never applies a Kernel mutation. Frozen origin/Invocation
// authority remains independently checked by the Harness.
func (runner *ShadowRunner) ObservesFrozenRemoteShadow(
	runtime *kernel.Runtime, delegation handoff.Delegation, child kernel.Snapshot,
) bool {
	if runner == nil || runtime == nil || runner.runtime != runtime || !handoff.RemoteGoalOnly(delegation) ||
		child.Run.Kind != RunKind || child.Run.ID != delegation.ChildRunID ||
		child.Run.Goal != strings.TrimSpace(delegation.Goal) ||
		delegation.MemberID != TargetPrefix+runner.targetID ||
		delegation.MemberRevision == "" || delegation.MemberRevision != runner.revision {
		return false
	}
	state, err := runner.decodeState(child)
	return err == nil && state.TargetID == runner.targetID &&
		state.TargetRevision == runner.revision &&
		((state.RemoteTaskID != "" && state.RemoteContextID != "") ||
			(state.RemoteTaskID == "" && state.RemoteMessageID != ""))
}

// StartRun creates the durable local shadow before sending the remote A2A message.
func (runner *ShadowRunner) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	runID := strings.TrimSpace(request.ID)
	goal := strings.TrimSpace(request.Goal)
	// A2A has no negotiated per-model-call ceiling. Never acknowledge a
	// local execution limit that the remote Agent cannot enforce.
	if runner == nil || runner.runtime == nil || runID == "" || goal == "" || !agent.SupportsRemoteGoalOnly(request) {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	state := runner.initialState(runID)
	// Seal the initial committed send intent before any network effect. A
	// response-lost or malformed reply leaves this original Run unresolved,
	// but its source must still be authenticated on every later read.
	initial := kernel.Snapshot{Run: kernel.Run{
		ID: runID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: strings.TrimSpace(request.RequestID), Goal: goal,
		Revision: 1, Status: kernel.RunStatusRunning,
	}}
	proof, err := runner.proofMAC(initial, state, initial.Run.Revision,
		initial.Run.Status, nil, nil, "", "")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Proof = fmt.Sprintf("%x", proof)
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
	if _, err = runner.decodeState(snapshot); err != nil {
		return snapshot, err // No external call on a substituted Create result.
	}
	interaction, sendErr := runner.client.SendMessage(ctx, runner.discovery, SendRequest{
		MessageID: state.MessageID, Text: goal,
	})
	if sendErr != nil {
		// The provider may have accepted the message before the HTTP reply
		// was lost. Signed terminal failure would strand a live remote task
		// and incorrectly settle the parent. Do not persist upstream errors
		// (which may contain credentials), nor issue another physical send.
		return snapshot, ErrRemoteOutcomeUnknown
	}
	return runner.applyInteraction(ctx, snapshot, state, interaction)
}

// LoadRun loads the local shadow and refreshes a non-terminal remote Task when present.
func (runner *ShadowRunner) LoadRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return snapshot, err
	}
	// Even a terminal Run is untrusted until its first-party remote receipt
	// attestation is checked. Previously this fast path accepted any local
	// Kernel twin with matching public identity, without a single A2A call.
	state, err := runner.decodeState(snapshot)
	if err != nil {
		return snapshot, err
	}
	if !shadowRunRefreshable(snapshot.Run.Status) {
		return snapshot, nil
	}
	// A committed physical intent that has not *returned* may still issue
	// network IO. No GET result, including terminal cancellation, can close
	// this window: otherwise a delayed sender could execute after an ACK.
	if (state.PendingMessageID != "" && !state.PendingMessageReturned) ||
		(state.PendingCancel && !state.PendingCancelReturned) {
		return snapshot, ErrRemoteOutcomeUnknown
	}
	if state.RemoteTaskID == "" {
		// This could be an unknown first Send outcome. Without the remote
		// Task ID a GET is impossible; never convert that uncertainty into
		// a locally certified failed/no-effect receipt.
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, ErrRemoteIdentityLost)
	}
	remote, err := runner.client.GetTask(ctx, runner.discovery, state.RemoteTaskID)
	if err != nil {
		// A provider transport failure cannot prove a terminal/no-effect
		// result. Do not expose remote error strings that may carry secrets,
		// or accidentally treat them as the pending-send marker below.
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, errRemoteReadUnavailable)
	}
	// Validate the original remote identity before the same-state shortcut.
	// State equality is not proof that GetTask returned the requested Task.
	if err := validateRemoteTaskBinding(state, remote); err != nil {
		return snapshot, err
	}
	// A pending resume is an already-issued physical SendMessage intent.
	// A GET that still reports a nonterminal Task cannot prove that the
	// input did not reach the remote peer. Never turn this observation into
	// a new input grant (even if the peer still says INPUT_REQUIRED).
	if (state.PendingMessageID != "" || state.PendingCancel) && !remoteTaskTerminalState(remote.State) {
		return snapshot, ErrRemoteOutcomeUnknown
	}
	if remote.State == state.RemoteState && !remote.Terminal {
		return snapshot, nil
	}
	return runner.applyTask(ctx, snapshot, state, remote)
}

// CancelRun uses the current revision as an implicit one-shot control source.
// The public Plugin additionally pins the caller's original revision.
func (runner *ShadowRunner) CancelRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	original, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return original, err
	}
	return runner.cancelRunAtRevision(ctx, runID, original.Run.Revision)
}

// cancelRunAtRevision gives one physical CancelTask the same durable CAS
// admission as Resume input. An existing input/cancel callback which has not
// returned can never race a successful cancellation ACK. No locks, TTLs or
// process-local state grant authority; the sealed Store revision does.
func (runner *ShadowRunner) cancelRunAtRevision(
	ctx context.Context, runID string, expectedRevision uint64,
) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || expectedRevision == 0 {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil || snapshot.Run.Revision != expectedRevision {
		if err != nil {
			return snapshot, err
		}
		return snapshot, kernel.ErrConflict
	}
	state, err := runner.decodeState(snapshot)
	if err != nil || !shadowRunRefreshable(snapshot.Run.Status) {
		return snapshot, err
	}
	if state.PendingCancel || (state.PendingMessageID != "" && !state.PendingMessageReturned) {
		return snapshot, ErrRemoteOutcomeUnknown
	}
	if state.RemoteTaskID == "" || state.RemoteContextID == "" {
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, ErrRemoteIdentityLost)
	}
	// A verified read before physical control rejects foreign GetTask replies.
	// Any intervening mutation is fenced by the *original* expectedRevision.
	observed, readErr := runner.LoadRun(ctx, runID)
	// Only an already-returned, original Resume callback may carry a
	// read-only outcome-unknown result into cancellation admission. A remote
	// GET failure or an unfinished physical send is never this authority.
	verifiedPendingResume := errors.Is(readErr, ErrRemoteOutcomeUnknown) &&
		!errors.Is(readErr, errRemoteReadUnavailable) &&
		state.PendingMessageID != "" && state.PendingMessageReturned
	if readErr != nil && !verifiedPendingResume {
		return observed, readErr
	}
	if observed.Run.Revision != expectedRevision {
		return observed, kernel.ErrConflict
	}
	if !shadowRunRefreshable(observed.Run.Status) {
		return observed, nil
	}
	// No second physical control may run once this record exists. The CAS
	// excludes an in-flight Resume admission and a concurrent Cancel.
	state.PendingCancel = true
	admitted, err := runner.applyAttested(ctx, observed, state, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "a2a.remote_cancel_requested", Message: "A2A cancellation admitted"}},
	})
	if err != nil {
		return admitted, err
	}
	remote, cancelErr := runner.client.CancelTask(ctx, runner.discovery, state.RemoteTaskID)
	// Physical callback has now returned, including transport failures. Seal
	// the release BEFORE a GET may declare a terminal result. If this Store
	// write is lost, retain a conservative unresolved physical intent.
	state.PendingCancelReturned = true
	returned, returnErr := runner.applyAttested(ctx, admitted, state, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "a2a.remote_cancel_returned", Message: "A2A cancellation callback returned"}},
	})
	if returnErr != nil {
		return admitted, errors.Join(ErrRemoteOutcomeUnknown, returnErr)
	}
	if cancelErr != nil {
		return returned, ErrRemoteOutcomeUnknown // Never expose remote error text.
	}
	if !remoteTaskTerminalState(remote.State) {
		if err = validateRemoteTaskBinding(state, remote); err != nil {
			return returned, err
		}
		return returned, ErrRemoteCancellationPending
	}
	return runner.applyTask(ctx, returned, state, remote)
}

// ResumeRun continues a remote input-required or auth-required task with one
// durable, uniquely identified user message. The MessageID is correlation,
// NOT proof that a remote peer deduplicates sends. Only the pending input hash
// is persisted locally; message content stays in the caller request.
func (runner *ShadowRunner) ResumeRun(ctx context.Context, runID, text string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || strings.TrimSpace(runID) == "" || strings.TrimSpace(text) == "" {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	snapshot, err := runner.runtime.Load(ctx, strings.TrimSpace(runID))
	if err != nil {
		return snapshot, err
	}
	return runner.resumeRunAtRevision(ctx, runID, snapshot.Run.Revision, text)
}

// resumeRunAtRevision binds the caller's original wait revision all the way
// through the Store CAS granting the physical SendMessage. A resolver or GET
// may not transparently select a newer prompt or cancellation state.
func (runner *ShadowRunner) resumeRunAtRevision(
	ctx context.Context, runID string, expectedRevision uint64, text string,
) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || expectedRevision == 0 || strings.TrimSpace(runID) == "" || strings.TrimSpace(text) == "" {
		return kernel.Snapshot{}, ErrInvalidShadowRunner
	}
	snapshot, err := runner.runtime.Load(ctx, strings.TrimSpace(runID))
	if err != nil || snapshot.Run.Revision != expectedRevision {
		if err != nil {
			return snapshot, err
		}
		return snapshot, kernel.ErrConflict
	}
	state, err := runner.decodeState(snapshot)
	if err != nil {
		return snapshot, err
	}
	// The original physical input Send may have succeeded even if its reply
	// was lost or the process died after the Store CAS. A stable MessageID
	// is a correlation key, NOT a remote deduplication guarantee. Neither
	// another caller nor another process may send it again.
	if state.PendingMessageID != "" || state.PendingCancel {
		return snapshot, ErrRemoteOutcomeUnknown
	}
	if snapshot.Run.Status != kernel.RunStatusWaitingInput {
		return snapshot, ErrRemoteInputRequired
	}
	if state.RemoteTaskID == "" || state.RemoteContextID == "" {
		return snapshot, ErrRemoteIdentityLost
	}
	inputHash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	state.MessageSequence++
	state.PendingMessageID = runID + ":message:" + strconv.Itoa(state.MessageSequence)
	state.PendingInputHash = inputHash
	// The atomic Running transition is a one-way physical admission: a
	// crash or a concurrent request cannot reopen the original send.
	snapshot, err = runner.markResumePending(ctx, snapshot, state)
	if err != nil {
		return snapshot, err
	}
	interaction, sendErr := runner.client.SendMessage(ctx, runner.discovery, SendRequest{
		MessageID: state.PendingMessageID, ContextID: state.RemoteContextID,
		TaskID: state.RemoteTaskID, Text: text,
	})
	// This original physical Send has now *returned*. Persist that fact
	// independently of response validation or transport errors; until it is
	// committed, an observer must NOT acknowledge a remote terminal result.
	state.PendingMessageReturned = true
	returned, returnErr := runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "a2a.remote_resume_returned", Message: "A2A input callback returned"}},
	})
	if returnErr != nil {
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, returnErr)
	}
	if sendErr != nil {
		// The provider may have consumed the input before the response was
		// lost. Never expose its upstream transport error text.
		return returned, ErrRemoteOutcomeUnknown
	}
	state.MessageID = state.PendingMessageID
	state.PendingMessageID = ""
	state.PendingInputHash = ""
	state.PendingMessageReturned = false
	return runner.applyInteraction(ctx, returned, state, interaction)
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
		state.TargetRevision != runner.revision ||
		(state.RemoteTaskID != "" && state.RemoteContextID == "") ||
		(state.PendingMessageID == "" && (state.PendingInputHash != "" || state.PendingMessageReturned)) ||
		(state.PendingMessageID != "" && (state.PendingInputHash == "" || snapshot.Run.Status != kernel.RunStatusRunning)) ||
		(state.PendingCancelReturned && !state.PendingCancel) ||
		(state.PendingCancel && snapshot.Run.Status != kernel.RunStatusRunning) ||
		!runner.verifiedProof(snapshot, state) {
		return shadowState{}, ErrRemoteBindingChanged
	}
	return state, nil
}

// The first SendMessage establishes the remote Task/Context pair. After that,
// GetTask, CancelTask and all subsequent SendMessage responses must name the
// *same* immutable pair. A missing identity cannot be substituted by a
// coincidentally matching state or by a new remote Context.
func validateRemoteTaskBinding(state shadowState, remote TaskSnapshot) error {
	if state.RemoteTaskID != "" || state.RemoteContextID != "" {
		if state.RemoteTaskID == "" || state.RemoteContextID == "" {
			return ErrRemoteIdentityLost
		}
		if remote.ID != state.RemoteTaskID || remote.ContextID != state.RemoteContextID {
			return ErrRemoteBindingChanged
		}
	}
	// The Task envelope alone is insufficient if its status, history or
	// artifacts explicitly claim another task or context. This check also
	// runs on the first Task response before binding it to the Shadow Run.
	if remote.StatusMessage != nil && !remoteMessageBelongsToTask(*remote.StatusMessage, remote) {
		return ErrRemoteBindingChanged
	}
	for _, message := range remote.History {
		if !remoteMessageBelongsToTask(message, remote) {
			return ErrRemoteBindingChanged
		}
	}
	for _, artifact := range remote.Artifacts {
		if artifact.TaskID != remote.ID || artifact.ContextID != remote.ContextID {
			return ErrRemoteBindingChanged
		}
	}
	return nil
}

func remoteMessageBelongsToTask(message MessageSnapshot, task TaskSnapshot) bool {
	// A2A Message taskId/contextId may be omitted inside a Task envelope;
	// when supplied they must never name a different task or context.
	return (message.TaskID == "" || message.TaskID == task.ID) &&
		(message.ContextID == "" || message.ContextID == task.ContextID)
}

func validateRemoteMessageBinding(state shadowState, remote MessageSnapshot) error {
	if state.RemoteTaskID == "" && state.RemoteContextID == "" {
		return nil // A direct initial Message has no previously bound Task.
	}
	if state.RemoteTaskID == "" || state.RemoteContextID == "" {
		return ErrRemoteIdentityLost
	}
	// Both identifiers are required for a Message that claims to answer
	// a pre-existing Task. Omitting taskId cannot prove task ownership when
	// multiple Tasks may share the same Context.
	if remote.TaskID != state.RemoteTaskID || remote.ContextID != state.RemoteContextID {
		return ErrRemoteBindingChanged
	}
	return nil
}

func (runner *ShadowRunner) applyInteraction(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	interaction Interaction,
) (kernel.Snapshot, error) {
	// A2A v1 SendMessage resolves to exactly one Task OR Message.
	// Do not prefer a plausible Task when a conflicting Message is present.
	if interaction.Task != nil && interaction.Message != nil {
		return snapshot, ErrInvalidResult
	}
	if interaction.Task != nil {
		return runner.applyTask(ctx, snapshot, state, *interaction.Task)
	}
	if interaction.Message == nil || len(interaction.Raw) == 0 {
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, ErrInvalidResult)
	}
	if err := validateRemoteMessageBinding(state, *interaction.Message); err != nil {
		return snapshot, err // Do not complete, rebind or erase a pending resume.
	}
	if strings.TrimSpace(interaction.Message.ID) == "" {
		return snapshot, ErrInvalidMessage
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
	if err := validateRemoteTaskBinding(state, remote); err != nil {
		return snapshot, err // Never persist an alien Task or Context identity.
	}
	if strings.TrimSpace(remote.ID) == "" || strings.TrimSpace(remote.ContextID) == "" || strings.TrimSpace(remote.State) == "" {
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, ErrInvalidTask)
	}
	// Only a terminal receipt, after the original physical callback has
	// durably returned, may clear the outstanding external control intent.
	if remoteTaskTerminalState(remote.State) {
		state.PendingCancel = false
		state.PendingCancelReturned = false
		state.PendingMessageID = ""
		state.PendingInputHash = ""
		state.PendingMessageReturned = false
	}
	state.RemoteTaskID = remote.ID
	state.RemoteContextID = remote.ContextID
	state.RemoteState = remote.State
	state.RemotePrompt = ""
	if remote.State == "TASK_STATE_INPUT_REQUIRED" || remote.State == "TASK_STATE_AUTH_REQUIRED" {
		state.RemotePrompt = remoteStatusPrompt(remote.StatusMessage)
	}
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
		return snapshot, errors.Join(ErrRemoteOutcomeUnknown, ErrInvalidTask)
	}
}

func remoteTaskTerminalState(status string) bool {
	switch status {
	case "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_REJECTED", "TASK_STATE_CANCELED":
		return true
	default:
		return false
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
		refreshed, err := runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
			Status: kernel.RunStatusRunning,
			Events: []kernel.EventDraft{{Type: "a2a.remote_wait_refreshed", Message: "A2A remote wait state changed"}},
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
		snapshot = refreshed
	}
	eventType := "a2a.remote_input_required"
	if state.RemoteState == "TASK_STATE_AUTH_REQUIRED" {
		eventType = "a2a.remote_auth_required"
	}
	checkpoint, err := runner.remoteCheckpoint(snapshot.Run.ID, state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status:     kernel.RunStatusWaitingInput,
		Checkpoint: checkpoint,
		Events:     []kernel.EventDraft{{Type: eventType, Message: state.RemoteState}},
	})
}

func (runner *ShadowRunner) markResumePending(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
) (kernel.Snapshot, error) {
	return runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "a2a.remote_resume_requested", Message: "A2A remote input submitted"}},
	})
}

func (runner *ShadowRunner) remoteCheckpoint(runID string, state shadowState) (*kernel.Checkpoint, error) {
	payload, err := json.Marshal(map[string]string{
		"remoteTaskID": state.RemoteTaskID, "remoteContextID": state.RemoteContextID,
		"remoteState": state.RemoteState, "prompt": state.RemotePrompt,
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

func remoteStatusPrompt(message *MessageSnapshot) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range message.Parts {
		if part.Kind != ContentPartText {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(text)
		if builder.Len() >= 4000 {
			break
		}
	}
	result := strings.TrimSpace(builder.String())
	if len(result) > 4000 {
		result = result[:4000]
	}
	return result
}

func (runner *ShadowRunner) keepRunning(ctx context.Context, snapshot kernel.Snapshot, state shadowState) (kernel.Snapshot, error) {
	return runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "a2a.remote_updated", Message: state.RemoteState}},
	})
}

func (runner *ShadowRunner) complete(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state shadowState,
	content json.RawMessage,
) (kernel.Snapshot, error) {
	if !json.Valid(content) {
		return kernel.Snapshot{}, ErrInvalidResult
	}
	return runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status: kernel.RunStatusCompleted,
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
	return runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status:    kernel.RunStatusFailed,
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
	return runner.applyAttested(ctx, snapshot, state, kernel.Mutation{
		Status:    kernel.RunStatusCancelled,
		ErrorCode: "a2a.remote_cancelled", ErrorDetail: strings.TrimSpace(reason),
		Events: []kernel.EventDraft{{Type: "a2a.remote_cancelled", Message: strings.TrimSpace(reason)}},
	})
}
