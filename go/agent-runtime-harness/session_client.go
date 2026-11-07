package harness

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// TranscriptSource reads the host-owned conversation ancestry for one prompt.
// SourcePath and Entries must include the current user message; previously
// completed assistant responses remain owned by the host, not by this facade.
// No additional Session transcript or cursor is persisted here.
type TranscriptSource interface {
	Seed(context.Context, HostRef, HostRef, string) (*ContextSeed, error)
}

// TranscriptSourceFunc adapts a host transcript lookup to TranscriptSource.
type TranscriptSourceFunc func(context.Context, HostRef, HostRef, string) (*ContextSeed, error)

func (fn TranscriptSourceFunc) Seed(ctx context.Context, thread, turn HostRef, prompt string) (*ContextSeed, error) {
	if fn == nil {
		return nil, ErrInvalidRequest
	}
	return fn(ctx, thread, turn, prompt)
}

// SessionOptions binds immutable execution settings and a host transcript to a
// Harness session. The host must namespace HostThread across tenants; a
// different Actor cannot reopen an existing persisted Session identity.
type SessionOptions struct {
	HostThread HostRef
	Actor      kernel.ActorRef
	Thread     kernel.ThreadRef
	Config     ConfigSnapshot
	Transcript TranscriptSource
}

// SessionClient is a stateless convenience handle over Harness Session/Turn.
// A new handle with the same identity and persistent stores can resume after
// process restart; the Harness Store remains the only Session authority.
type SessionClient struct {
	runner     *Runner
	hostThread HostRef
	actor      kernel.ActorRef
	thread     kernel.ThreadRef
	config     ConfigSnapshot
	transcript TranscriptSource
}

// OpenSession prepares a short Prompt/Load/Refresh/Resume API for text conversations.
// Transcript may be omitted when reopening a Session only to read or resume
// existing Turns. Prompt requires it; recovery uses committed Context instead.
// Advanced orchestration uses explicitly chosen Runner.StartAgentTurn/StartTeamTurn/StartWorkflowTurn instead.
func (runner *Runner) OpenSession(options SessionOptions) (*SessionClient, error) {
	if runner == nil || runner.store == nil || runner.context == nil ||
		!validActor(options.Actor) || strings.TrimSpace(options.Thread.Kind) == "" ||
		strings.TrimSpace(options.Thread.ID) == "" {
		return nil, ErrInvalidRequest
	}
	thread, err := normalizeHostRef(options.HostThread)
	if err != nil {
		return nil, err
	}
	return &SessionClient{
		runner: runner, hostThread: thread, actor: options.Actor,
		thread: options.Thread, config: cloneConfigSnapshot(options.Config),
		transcript: options.Transcript,
	}, nil
}

// Prompt starts or reloads one idempotent Harness Turn within this Session.
// turnID is a host-stable identity, not a generated sequence number: retrying
// the same turn must reuse the same ID, while a new prompt uses a new ID.
func (session *SessionClient) Prompt(ctx context.Context, turnID, prompt string) (Snapshot, error) {
	if session == nil || session.runner == nil || session.transcript == nil || !utf8.ValidString(turnID) ||
		model.ValidateMessage(model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)}) != nil {
		return Snapshot{}, ErrInvalidRequest
	}
	hostTurn, err := session.hostTurn(turnID)
	if err != nil {
		return Snapshot{}, err
	}
	// Enforce an existing Session's Actor boundary before asking a Host
	// source to materialize potentially private conversation history.
	if err = session.checkExistingIdentity(ctx); err != nil {
		return Snapshot{}, err
	}
	seed, err := session.transcript.Seed(ctx, session.hostThread, hostTurn, prompt)
	if err != nil {
		return Snapshot{}, err
	}
	// This text-only facade rejects a missing or stale current input rather
	// than accidentally replaying only the preceding conversation into a model.
	normalized, err := normalizeContextSeed(seed)
	if err != nil || normalized == nil || len(normalized.Entries) == 0 {
		return Snapshot{}, ErrInvalidRequest
	}
	// A custom TranscriptSource may also contain richer roles and media. Apply
	// the shared model contract to every entry before admitting an immutable
	// Turn; otherwise malformed UTF-8 can be lossy-encoded in the admission
	// hash and fail only after the Model has already performed an effect.
	for _, entry := range normalized.Entries {
		if !utf8.ValidString(entry.ID) || !utf8.ValidString(entry.SourceID) ||
			!utf8.ValidString(entry.TurnID) || entry.ID == "" || entry.ID != strings.TrimSpace(entry.ID) ||
			entry.SourceID == "" || entry.SourceID != strings.TrimSpace(entry.SourceID) ||
			entry.TurnID != strings.TrimSpace(entry.TurnID) || model.ValidateMessage(entry.Message) != nil {
			return Snapshot{}, ErrInvalidRequest
		}
	}
	last := normalized.Entries[len(normalized.Entries)-1].Message
	if last.Role != model.RoleUser || strings.TrimSpace(model.TextValue(last.Content)) != strings.TrimSpace(prompt) {
		return Snapshot{}, ErrInvalidRequest
	}
	return session.runner.StartAgentTurn(ctx, StartRequest{
		HostThread: session.hostThread, HostTurn: hostTurn, Actor: session.actor,
		Thread: session.thread, RequestID: strings.TrimSpace(turnID), Goal: prompt,
		Config: cloneConfigSnapshot(session.config), Context: normalized,
	})
}

// Load obtains an existing durable Turn without generating another model call.
func (session *SessionClient) Load(ctx context.Context, turnID string) (Snapshot, error) {
	id, err := session.authorizedTurnID(ctx, turnID)
	if err != nil {
		return Snapshot{}, err
	}
	value, loadErr := session.runner.Load(ctx, id)
	return session.verifySnapshot(id, value, loadErr)
}

// LoadState reads bounded status/config facts without scanning Timeline history.
// Like Load, it does not advance the underlying Agent Run.
func (session *SessionClient) LoadState(ctx context.Context, turnID string) (StateSnapshot, error) {
	id, err := session.authorizedTurnID(ctx, turnID)
	if err != nil {
		return StateSnapshot{}, err
	}
	state, err := session.runner.LoadState(ctx, id)
	if err != nil {
		return StateSnapshot{}, err
	}
	if state.Session.Actor != session.actor || state.Session.HostThread != session.hostThread ||
		state.Turn.ID != id || state.Turn.SessionID != state.Session.ID ||
		state.Config.TurnID != id {
		return StateSnapshot{}, ErrConflict
	}
	return state, nil
}

// Timeline reads one page of the committed Turn Item history.
func (session *SessionClient) Timeline(ctx context.Context, turnID string, afterSeq uint64, limit int) ([]Item, error) {
	id, err := session.authorizedTurnID(ctx, turnID)
	if err != nil {
		return nil, err
	}
	items, err := session.runner.ListTimeline(ctx, id, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	// The page has only Turn IDs, not Actor metadata. Recheck its frozen
	// owner after the Store read, not merely the initial Session projection.
	if err := session.requireFrozenTurnOwner(ctx, id); err != nil {
		return nil, err
	}
	return items, nil
}

// Refresh synchronizes one existing Turn from its underlying Runtime facts.
func (session *SessionClient) Refresh(ctx context.Context, turnID string) (Snapshot, error) {
	id, err := session.authorizedTurnID(ctx, turnID)
	if err != nil {
		return Snapshot{}, err
	}
	value, refreshErr := session.runner.refreshTurnForSession(ctx, id, session.actor, session.thread)
	return session.verifySnapshot(id, value, refreshErr)
}

// Resume continues one admitted Agent Turn from its committed Context.
// It neither reads TranscriptSource nor creates a new execution attempt. A
// waiting or terminal Turn is returned without resolving approval or budget.
func (session *SessionClient) Resume(ctx context.Context, turnID string) (Snapshot, error) {
	id, err := session.authorizedTurnID(ctx, turnID)
	if err != nil {
		return Snapshot{}, err
	}
	value, resumeErr := session.runner.resumeTurnForSession(ctx, id, session.actor, session.thread)
	return session.verifySnapshot(id, value, resumeErr)
}

// authorizedTurnID is the single read/recovery trust boundary for all
// SessionClient operations. The identity proof must precede any Turn load,
// projection or native Run continuation; Prompt independently checks ownership
// before asking the Host for a potentially private transcript.
func (session *SessionClient) authorizedTurnID(ctx context.Context, turnID string) (string, error) {
	id, err := session.turnID(turnID)
	if err != nil {
		return "", err
	}
	if err = session.requireIdentity(ctx); err != nil {
		return "", err
	}
	// The Session record alone is not independent ownership authority: an
	// inconsistent Store can return a forged Actor on this first read. The
	// already-frozen root Invocation must agree with this exact caller.
	if err = session.requireFrozenTurnOwner(ctx, id); err != nil {
		return "", err
	}
	return id, nil
}

// requireFrozenTurnOwner cross-checks the Host Session handle with the
// original root Agent Invocation from the trusted Store (ADR 0017). An
// inconsistent or stale Session receipt must not authorize another Actor's
// history or continuation. Both values come from the same trusted Store;
// they are not independent proofs against a persistently malicious Store.
// No Kernel read is needed for bounded LoadState/Timeline.
func (session *SessionClient) requireFrozenTurnOwner(ctx context.Context, id string) error {
	if session == nil || session.runner == nil || session.runner.store == nil {
		return ErrInvalidRequest
	}
	turn, err := loadVerifiedTurn(ctx, session.runner.store, id)
	if err != nil {
		return err
	}
	sessionID, err := SessionID(session.hostThread)
	if err != nil || turn.SessionID != sessionID {
		return ErrConflict
	}
	// Prompt freezes its root Invocation ID from the stable Host Turn ID.
	// Use this exact indexed record rather than scanning child Invocations
	// on each bounded status/history read or trusting a returned list order.
	rootID, err := AgentInvocationID(id, turn.HostTurn.ID)
	if err != nil {
		return ErrConflict
	}
	invocation, err := session.runner.store.GetInvocation(ctx, rootID)
	if err != nil {
		// A Turn without an original frozen execution owner cannot grant
		// SessionClient read or resume authority from a Session alone.
		return errors.Join(ErrConflict, err)
	}
	return session.checkFrozenRootOwner(turn, invocation)
}

// sessionBoundOwner is a caller-supplied requirement, not a new persistent
// execution identity or a replacement for the original root Invocation.
type sessionBoundOwner struct {
	actor  kernel.ActorRef
	thread kernel.ThreadRef
}

// requireSessionRootOwner is shared by pre-read proof, returned snapshots,
// pre-Refresh repair and pre-Resume physical continuation. Inconsistent
// Store reads must not choose different rules at different boundaries.
func requireSessionRootOwner(turn Turn, invocation Invocation, owner sessionBoundOwner) error {
	rootID, err := AgentInvocationID(turn.ID, turn.HostTurn.ID)
	if err != nil || invocation.ID != rootID || invocation.TurnID != turn.ID ||
		invocation.ParentItemID != "" || invocation.ExecutionClass != ExecutionAgent ||
		invocation.CapabilityKey != CapabilityAgent || !validInvocation(invocation) {
		return ErrConflict
	}
	actor, thread, err := frozenTopLevelInvocationIdentity(invocation)
	if err != nil || actor != owner.actor || thread != owner.thread {
		return ErrConflict
	}
	return nil
}

func (session *SessionClient) checkFrozenRootOwner(turn Turn, invocation Invocation) error {
	return requireSessionRootOwner(turn, invocation, sessionBoundOwner{actor: session.actor, thread: session.thread})
}

// verifySnapshot applies the caller's owner proof to the actual returned
// projection (including non-nil error snapshots). A second Store read cannot
// substitute another Session, root Invocation, or physical result for display.
func (session *SessionClient) verifySnapshot(id string, value Snapshot, runErr error) (Snapshot, error) {
	if value.Turn.ID == "" && runErr != nil {
		return Snapshot{}, runErr
	}
	sessionID, err := SessionID(session.hostThread)
	if err != nil || value.Session.ID != sessionID || value.Session.HostThread != session.hostThread ||
		value.Session.Actor != session.actor || value.Turn.ID != id ||
		value.Turn.SessionID != sessionID || value.Config.TurnID != id {
		return Snapshot{}, ErrConflict
	}
	root, ok := topLevelInvocation(value.Invocations)
	if !ok || session.checkFrozenRootOwner(value.Turn, root) != nil {
		return Snapshot{}, ErrConflict
	}
	return value, runErr
}

func (session *SessionClient) hostTurn(turnID string) (HostRef, error) {
	if session == nil || session.runner == nil {
		return HostRef{}, ErrInvalidRequest
	}
	return normalizeHostRef(HostRef{Kind: "conversation_turn", ID: turnID})
}

func (session *SessionClient) turnID(turnID string) (string, error) {
	hostTurn, err := session.hostTurn(turnID)
	if err != nil {
		return "", err
	}
	sessionID, err := SessionID(session.hostThread)
	if err != nil {
		return "", err
	}
	return TurnID(sessionID, hostTurn)
}

func (session *SessionClient) checkExistingIdentity(ctx context.Context) error {
	err := session.requireIdentity(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (session *SessionClient) requireIdentity(ctx context.Context) error {
	if session == nil || session.runner == nil || session.runner.store == nil {
		return ErrInvalidRequest
	}
	id, err := SessionID(session.hostThread)
	if err != nil {
		return err
	}
	stored, err := loadVerifiedSession(ctx, session.runner.store, id)
	if err != nil {
		return err
	}
	if stored.HostThread != session.hostThread || stored.Actor != session.actor {
		return ErrConflict
	}
	return nil
}
