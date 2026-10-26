package harness

import (
	"context"
	"errors"
	"strings"

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
// Advanced orchestration continues to use Runner.Start directly.
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
	if session == nil || session.runner == nil || session.transcript == nil || strings.TrimSpace(prompt) == "" {
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
	last := normalized.Entries[len(normalized.Entries)-1].Message
	if last.Role != model.RoleUser || strings.TrimSpace(last.Content) != strings.TrimSpace(prompt) {
		return Snapshot{}, ErrInvalidRequest
	}
	return session.runner.Start(ctx, StartRequest{
		HostThread: session.hostThread, HostTurn: hostTurn, Actor: session.actor,
		Thread: session.thread, RequestID: strings.TrimSpace(turnID), Goal: prompt,
		Config: cloneConfigSnapshot(session.config), Context: normalized,
	})
}

// Load obtains an existing durable Turn without generating another model call.
func (session *SessionClient) Load(ctx context.Context, turnID string) (Snapshot, error) {
	id, err := session.turnID(turnID)
	if err != nil {
		return Snapshot{}, err
	}
	if err = session.requireIdentity(ctx); err != nil {
		return Snapshot{}, err
	}
	return session.runner.Load(ctx, id)
}

// LoadState reads bounded status/config facts without scanning Timeline history.
// Like Load, it does not advance the underlying Agent Run.
func (session *SessionClient) LoadState(ctx context.Context, turnID string) (StateSnapshot, error) {
	id, err := session.turnID(turnID)
	if err != nil {
		return StateSnapshot{}, err
	}
	if err = session.requireIdentity(ctx); err != nil {
		return StateSnapshot{}, err
	}
	return session.runner.LoadState(ctx, id)
}

// Timeline reads one page of the committed Turn Item history.
func (session *SessionClient) Timeline(ctx context.Context, turnID string, afterSeq uint64, limit int) ([]Item, error) {
	id, err := session.turnID(turnID)
	if err != nil {
		return nil, err
	}
	if err = session.requireIdentity(ctx); err != nil {
		return nil, err
	}
	return session.runner.ListTimeline(ctx, id, afterSeq, limit)
}

// Refresh synchronizes one existing Turn from its underlying Runtime facts.
func (session *SessionClient) Refresh(ctx context.Context, turnID string) (Snapshot, error) {
	id, err := session.turnID(turnID)
	if err != nil {
		return Snapshot{}, err
	}
	if err = session.requireIdentity(ctx); err != nil {
		return Snapshot{}, err
	}
	return session.runner.Refresh(ctx, id)
}

// Resume continues one admitted direct-Agent Turn from its committed Context.
// It neither reads TranscriptSource nor creates a new execution attempt. A
// waiting or terminal Turn is returned without resolving approval or budget.
func (session *SessionClient) Resume(ctx context.Context, turnID string) (Snapshot, error) {
	id, err := session.turnID(turnID)
	if err != nil {
		return Snapshot{}, err
	}
	if err = session.requireIdentity(ctx); err != nil {
		return Snapshot{}, err
	}
	return session.runner.ResumeTurn(ctx, id)
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
	stored, err := session.runner.store.GetSession(ctx, id)
	if err != nil {
		return err
	}
	if stored.HostThread != session.hostThread || stored.Actor != session.actor {
		return ErrConflict
	}
	return nil
}
