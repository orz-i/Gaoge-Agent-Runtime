package acp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const RunKind = handoff.RemoteAgentRunKind
const proofDomain = "acp.shadow.lifecycle.v1"
const maxStreamBytes = 1 << 20

// ACP has Session and Prompt, not A2A Task/Context/Message. Its original
// physical intents are committed before sending session/new, session/prompt,
// and session/cancel. Neither Prompt nor Cancel is replay-safe on lost replies.
type remoteState struct {
	TargetID       string `json:"targetID"`
	Revision       string `json:"revision"`
	Cwd            string `json:"cwd"`
	Version        string `json:"version"`
	SessionID      string `json:"sessionID,omitempty"`
	OpenPending    bool   `json:"openPending"`
	OpenReturned   bool   `json:"openReturned,omitempty"`
	PromptPending  bool   `json:"promptPending,omitempty"`
	PromptReturned bool   `json:"promptReturned,omitempty"`
	CancelPending  bool   `json:"cancelPending,omitempty"`
	CancelReturned bool   `json:"cancelReturned,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	// PromptOutput is held ONLY while an earlier admitted Cancel callback
	// is still in flight. It is never a second physical execution receipt.
	PromptOutput  string `json:"promptOutput,omitempty"`
	PromptUnknown bool   `json:"promptUnknown,omitempty"`
	Proof         string `json:"proof"`
}

func decodeRemoteState(raw json.RawMessage, state *remoteState) error {
	if state == nil || json.Unmarshal(raw, state) != nil || state.TargetID == "" ||
		state.Revision == "" || state.Cwd == "" || state.Version != ProtocolVersion ||
		!state.OpenPending || state.PromptPending && state.SessionID == "" ||
		state.PromptReturned && !state.PromptPending ||
		state.CancelReturned && !state.CancelPending ||
		state.PromptUnknown && !state.PromptReturned ||
		state.PromptOutput != "" && (!state.PromptReturned || state.StopReason != "end_turn") {
		return ErrBindingChanged
	}
	return nil
}

type ShadowDependencies struct {
	Runtime        *kernel.Runtime
	Binding        Binding
	AttestationKey []byte
}
type ShadowRunner struct {
	runtime *kernel.Runtime
	binding Binding
	key     []byte
}

func NewShadowRunner(deps ShadowDependencies) (*ShadowRunner, error) {
	if deps.Runtime == nil || len(deps.AttestationKey) < 32 ||
		!validBinding(deps.Binding, deps.Binding.TargetID, deps.Binding.Revision) {
		return nil, ErrInvalidRunner
	}
	return &ShadowRunner{runtime: deps.Runtime, binding: deps.Binding, key: append([]byte(nil), deps.AttestationKey...)}, nil
}
func (r *ShadowRunner) signature(snapshot kernel.Snapshot, state remoteState, revision uint64,
	status kernel.RunStatus, result *kernel.Result, cp *kernel.Checkpoint, code, detail string) ([]byte, error) {
	state.Proof = ""
	return handoff.RemoteProofMAC(r.key, proofDomain, snapshot, state, revision, status, result, cp, code, detail)
}
func (r *ShadowRunner) decodeState(snap kernel.Snapshot) (remoteState, error) {
	var state remoteState
	if r == nil || snap.Run.Kind != RunKind || decodeRemoteState(snap.State, &state) != nil ||
		state.TargetID != r.binding.TargetID || state.Revision != r.binding.Revision ||
		state.Cwd != r.binding.Cwd {
		return remoteState{}, ErrBindingChanged
	}
	proof := state.Proof
	state.Proof = ""
	if !handoff.VerifyRemoteProof(r.key, proofDomain, snap, state, proof) {
		return remoteState{}, ErrBindingChanged
	}
	state.Proof = proof
	if snap.Run.Status == kernel.RunStatusCompleted && (!state.PromptReturned || state.StopReason != "end_turn") ||
		snap.Run.Status == kernel.RunStatusCancelled && (!state.PromptReturned || state.StopReason != "cancelled") ||
		snap.Run.Status == kernel.RunStatusFailed && !state.PromptReturned ||
		(snap.Run.Status != kernel.RunStatusRunning && (state.PromptUnknown ||
			(state.CancelPending && !state.CancelReturned))) {
		return remoteState{}, ErrBindingChanged
	}
	return state, nil
}
func (r *ShadowRunner) apply(ctx context.Context, snap kernel.Snapshot, state remoteState,
	mutation kernel.Mutation) (kernel.Snapshot, error) {
	mutation.ErrorCode = strings.TrimSpace(mutation.ErrorCode)
	mutation.ErrorDetail = strings.TrimSpace(mutation.ErrorDetail)
	proof, err := r.signature(snap, state, snap.Run.Revision+1, mutation.Status,
		mutation.Result, mutation.Checkpoint, mutation.ErrorCode, mutation.ErrorDetail)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Proof = hex.EncodeToString(proof)
	mutation.State, err = json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return r.runtime.Apply(ctx, snap.Run.ID, snap.Run.Revision, mutation)
}

// ObservesFrozenRemoteShadow provides the same read-only proof used by A2A.
// It never accepts a same-ID native Run, a forged ACP Session or stale binding.
func (r *ShadowRunner) ObservesFrozenRemoteShadow(rt *kernel.Runtime, d handoff.Delegation, child kernel.Snapshot) bool {
	if r == nil || rt == nil || rt != r.runtime || !handoff.RemoteGoalOnly(d) ||
		child.Run.Kind != RunKind || child.Run.ID != d.ChildRunID ||
		child.Run.Goal != strings.TrimSpace(d.Goal) ||
		d.MemberID != TargetPrefix+r.binding.TargetID || d.MemberRevision != r.binding.Revision {
		return false
	}
	_, err := r.decodeState(child)
	return err == nil
}

func (r *ShadowRunner) StartRun(ctx context.Context, req agent.StartRequest) (kernel.Snapshot, error) {
	id, goal := strings.TrimSpace(req.ID), strings.TrimSpace(req.Goal)
	if r == nil || r.runtime == nil || id == "" || goal == "" || !agent.SupportsRemoteGoalOnly(req) {
		return kernel.Snapshot{}, ErrInvalidRunner
	}
	state := remoteState{
		TargetID: r.binding.TargetID, Revision: r.binding.Revision, Cwd: r.binding.Cwd,
		Version: ProtocolVersion, OpenPending: true,
	}
	initial := kernel.Snapshot{Run: kernel.Run{ID: id, Kind: RunKind, Actor: req.Actor,
		Thread: req.Thread, RequestID: strings.TrimSpace(req.RequestID), Goal: goal,
		Revision: 1, Status: kernel.RunStatusRunning}}
	proof, err := r.signature(initial, state, 1, kernel.RunStatusRunning, nil, nil, "", "")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	state.Proof = hex.EncodeToString(proof)
	encoded, err := json.Marshal(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snap, err := r.runtime.Create(ctx, kernel.CreateRequest{ID: id, Kind: RunKind,
		Actor: req.Actor, Thread: req.Thread, RequestID: req.RequestID, Goal: goal, State: encoded,
		Events: []kernel.EventDraft{{Type: "acp.remote_started", Message: "ACP remote intent admitted"}}})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if _, err = r.decodeState(snap); err != nil {
		return snap, err
	}
	// session/new is not a deduplicated RPC. A lost response is unknown.
	sessionID, openErr := r.binding.Client.OpenSession(ctx, id, r.binding.Cwd)
	if openErr != nil || strings.TrimSpace(sessionID) == "" {
		return snap, ErrOutcomeUnknown
	}
	state.OpenReturned = true
	state.SessionID = sessionID
	snap, err = r.apply(ctx, snap, state, kernel.Mutation{Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "acp.remote_session", Message: "ACP session identity frozen"}}})
	if err != nil {
		return snap, errors.Join(ErrOutcomeUnknown, err)
	}
	// The prompt is the second physical effect. Its admission must be durable
	// and exclusive; restarting a process cannot repeat an unknown Prompt.
	state.PromptPending = true
	snap, err = r.apply(ctx, snap, state, kernel.Mutation{Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "acp.remote_prompt_intent", Message: "ACP prompt effect admitted"}}})
	if err != nil {
		return snap, errors.Join(ErrOutcomeUnknown, err)
	}
	var chunks strings.Builder
	var streamErr error
	var mu sync.Mutex
	stop, promptErr := r.binding.Client.Prompt(ctx, id, sessionID, goal, func(chunk string) error {
		mu.Lock()
		defer mu.Unlock()
		if streamErr != nil {
			return streamErr
		}
		if chunks.Len()+len(chunk) > maxStreamBytes {
			streamErr = ErrOutcomeUnknown
			return streamErr
		}
		_, streamErr = chunks.WriteString(chunk)
		return streamErr
	})
	mu.Lock()
	answer, overflow := chunks.String(), streamErr
	mu.Unlock()
	if overflow != nil && promptErr == nil {
		promptErr = overflow
	}
	return r.finishPrompt(ctx, id, sessionID, stop, answer, promptErr)
}

// settledPromptMutation projects only an already returned original ACP Prompt,
// after any concurrently admitted cancel callback has ALSO returned. A
// session/cancel transport return alone is never a terminal remote receipt.
func settledPromptMutation(state *remoteState) (kernel.Mutation, bool) {
	mutation := kernel.Mutation{Status: kernel.RunStatusRunning}
	if !state.PromptReturned || state.PromptUnknown ||
		(state.CancelPending && !state.CancelReturned) {
		return mutation, false
	}
	switch state.StopReason {
	case "end_turn":
		result, err := json.Marshal(struct {
			Text       string `json:"text"`
			StopReason string `json:"stopReason"`
		}{Text: state.PromptOutput, StopReason: state.StopReason})
		if err != nil {
			return mutation, false
		}
		mutation.Status = kernel.RunStatusCompleted
		mutation.Result = &kernel.Result{ContentType: "application/json", Content: result}
	case "cancelled":
		mutation.Status = kernel.RunStatusCancelled
		mutation.ErrorCode = "acp.remote_cancelled"
	case "max_tokens", "max_turn_requests", "refusal":
		mutation.Status = kernel.RunStatusFailed
		mutation.ErrorCode = "acp.remote_incomplete"
	default:
		return mutation, false
	}
	// Do not duplicate potentially large answer text in the sealed state
	// once the Kernel terminal Result owns that exact content.
	state.PromptOutput = ""
	return mutation, true
}

func (r *ShadowRunner) finishPrompt(ctx context.Context, id, sessionID, stop, answer string, promptErr error) (kernel.Snapshot, error) {
	// A concurrent Cancel may advance the original Kernel CAS while Prompt
	// remains in flight. Only the original admitted Prompt may finish it.
	for attempt := 0; attempt < 16; attempt++ {
		snap, err := r.runtime.Load(ctx, id)
		if err != nil {
			return snap, errors.Join(ErrOutcomeUnknown, err)
		}
		state, err := r.decodeState(snap)
		if err != nil || state.SessionID != sessionID || !state.PromptPending || state.PromptReturned ||
			snap.Run.Status != kernel.RunStatusRunning {
			return snap, errors.Join(ErrOutcomeUnknown, err)
		}
		state.PromptReturned = true
		state.StopReason = stop
		state.PromptUnknown = promptErr != nil
		if !state.PromptUnknown && stop == "end_turn" {
			state.PromptOutput = answer
		}
		mutation, settled := settledPromptMutation(&state)
		mutation.Events = []kernel.EventDraft{{Type: "acp.remote_prompt_returned", Message: "ACP prompt callback returned"}}
		next, applyErr := r.apply(ctx, snap, state, mutation)
		if errors.Is(applyErr, kernel.ErrConflict) {
			continue
		}
		if applyErr != nil {
			return next, errors.Join(ErrOutcomeUnknown, applyErr)
		}
		if !settled {
			return next, ErrOutcomeUnknown
		}
		return next, nil
	}
	return kernel.Snapshot{}, ErrOutcomeUnknown
}

// ACP v1 has no authenticated read-only request for an individual completed
// Prompt. Session/load is NOT an original prompt receipt, so never replay
// session/new or session/prompt just because a local process restarted.
func (r *ShadowRunner) LoadRun(ctx context.Context, id string) (kernel.Snapshot, error) {
	if r == nil || r.runtime == nil {
		return kernel.Snapshot{}, ErrInvalidRunner
	}
	snap, err := r.runtime.Load(ctx, id)
	if err != nil {
		return snap, err
	}
	if _, err = r.decodeState(snap); err != nil {
		return snap, err
	}
	if snap.Run.Status == kernel.RunStatusRunning {
		return snap, ErrOutcomeUnknown
	}
	return snap, nil
}

func (r *ShadowRunner) CancelRun(ctx context.Context, id string, expectedRevision uint64) (kernel.Snapshot, error) {
	if r == nil || r.runtime == nil || expectedRevision == 0 {
		return kernel.Snapshot{}, ErrInvalidRunner
	}
	snap, err := r.runtime.Load(ctx, id)
	if err != nil {
		return snap, err
	}
	if snap.Run.Revision != expectedRevision {
		return snap, kernel.ErrConflict
	}
	state, err := r.decodeState(snap)
	if err != nil {
		return snap, err
	}
	if snap.Run.Status != kernel.RunStatusRunning {
		return snap, nil
	}
	// A committed Prompt may be in flight. ACP session/cancel is a best-effort
	// notification, not a no-effect or terminal receipt. Both physical
	// callbacks must durably return before a terminal Prompt can be signed.
	// Earlier cancelled session admission fences a later Prompt CAS.
	if state.SessionID == "" || state.CancelPending {
		return snap, ErrOutcomeUnknown
	}
	state.CancelPending = true
	snap, err = r.apply(ctx, snap, state, kernel.Mutation{Status: kernel.RunStatusRunning,
		Events: []kernel.EventDraft{{Type: "acp.remote_cancel_intent", Message: "ACP cancel admitted"}}})
	if err != nil {
		return snap, err
	}
	sendErr := r.binding.Client.CancelSession(ctx, id, state.SessionID)
	// The SDK cancel notification only acknowledges transport send, not
	// completion. Never convert it into a signed terminal cancellation.
	for attempt := 0; attempt < 16; attempt++ {
		current, loadErr := r.runtime.Load(ctx, id)
		if loadErr != nil {
			return snap, errors.Join(ErrOutcomeUnknown, loadErr)
		}
		frozen, verifyErr := r.decodeState(current)
		if verifyErr != nil {
			return current, verifyErr
		}
		if current.Run.Status != kernel.RunStatusRunning {
			return current, nil
		}
		if !frozen.CancelPending {
			return current, ErrBindingChanged
		}
		frozen.CancelReturned = true
		mutation, settled := settledPromptMutation(&frozen)
		mutation.Events = []kernel.EventDraft{{Type: "acp.remote_cancel_returned", Message: "ACP cancel callback returned"}}
		next, applyErr := r.apply(ctx, current, frozen, mutation)
		if errors.Is(applyErr, kernel.ErrConflict) {
			continue
		}
		if applyErr != nil {
			return next, errors.Join(ErrOutcomeUnknown, applyErr)
		}
		// Only the original returned Prompt can settle the Run. The cancel
		// transport return is not independently sufficient, even if nil.
		if sendErr != nil || !settled {
			return next, ErrOutcomeUnknown
		}
		return next, nil
	}
	return snap, ErrOutcomeUnknown
}

var _ handoff.ChildRunner = (*ShadowRunner)(nil)
var _ handoff.RemoteShadowObserver = (*ShadowRunner)(nil)
