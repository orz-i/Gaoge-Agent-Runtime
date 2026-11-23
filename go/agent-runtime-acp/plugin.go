// Package acp connects Agent Client Protocol v1 peers to the existing
// Host-selected Agent/Handoff/Kernel execution boundary. ACP sessions are
// protocol state, never an independent Runtime executor.
package acp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const TargetPrefix = "acp:"
const ProtocolVersion = "1"

var (
	ErrInvalidBinding  = errors.New("invalid ACP remote binding")
	ErrBindingChanged  = errors.New("ACP frozen binding mismatch")
	ErrOutcomeUnknown  = errors.New("ACP remote effect outcome unverified")
	ErrSessionMismatch = errors.New("ACP session identity mismatch")
	ErrInvalidRunner   = errors.New("invalid ACP shadow runner")
)

// RemoteClient is supplied by the Host and talks ACP v1. A returned SessionID
// is the remote protocol identity, not a local Run ID or an A2A Task ID.
// Implementations MUST bind every method to the same original run/session.
type RemoteClient interface {
	OpenSession(context.Context, string, string) (string, error)
	Prompt(context.Context, string, string, string, func(string) error) (string, error)
	CancelSession(context.Context, string, string) error
}

// Binding is one Host-selected immutable target version and working directory.
// An executable path, credential, permission UI or active session is NEVER
// persisted in the child Run. Binding resolution is exact on recovery.
type Binding struct {
	TargetID string
	Revision string
	Cwd      string
	Client   RemoteClient
}

type BindingResolver interface {
	ResolveBinding(context.Context, string, string) (Binding, error)
}
type BindingResolverFunc func(context.Context, string, string) (Binding, error)

func (f BindingResolverFunc) ResolveBinding(ctx context.Context, targetID, revision string) (Binding, error) {
	if f == nil {
		return Binding{}, ErrInvalidBinding
	}
	return f(ctx, targetID, revision)
}

type PluginDependencies struct {
	Runtime        *kernel.Runtime
	Bindings       BindingResolver
	AttestationKey []byte
}
type Plugin struct {
	runtime  *kernel.Runtime
	bindings BindingResolver
	key      []byte
}

func NewPlugin(deps PluginDependencies) (*Plugin, error) {
	if deps.Runtime == nil || deps.Bindings == nil || len(deps.AttestationKey) < 32 {
		return nil, ErrInvalidBinding
	}
	return &Plugin{runtime: deps.Runtime, bindings: deps.Bindings, key: append([]byte(nil), deps.AttestationKey...)}, nil
}
func (p *Plugin) ResolveChild(ctx context.Context, d handoff.Delegation) (handoff.ChildRunner, error) {
	if p == nil || p.runtime == nil || p.bindings == nil || !strings.HasPrefix(d.MemberID, TargetPrefix) ||
		strings.TrimSpace(d.ChildRunID) == "" || !handoff.RemoteGoalOnly(d) {
		return nil, ErrInvalidBinding
	}
	id := strings.TrimPrefix(d.MemberID, TargetPrefix)
	if !validID(id) {
		return nil, ErrInvalidBinding
	}
	revision := d.MemberRevision
	persisted, err := p.runtime.Load(ctx, d.ChildRunID)
	if err == nil {
		if persisted.Run.Kind != handoff.RemoteAgentRunKind {
			return nil, ErrBindingChanged
		}
		var state remoteState
		if decodeErr := decodeRemoteState(persisted.State, &state); decodeErr != nil ||
			state.TargetID != id || state.Revision == "" {
			return nil, ErrBindingChanged
		}
		revision = state.Revision
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return nil, err
	}
	binding, err := p.bindings.ResolveBinding(ctx, id, revision)
	if err != nil || !validBinding(binding, id, revision) {
		return nil, errors.Join(ErrInvalidBinding, err)
	}
	return NewShadowRunner(ShadowDependencies{Runtime: p.runtime, Binding: binding, AttestationKey: p.key})
}

func (p *Plugin) Cancel(ctx context.Context, id string, revision uint64, _ string) (kernel.Snapshot, error) {
	if p == nil || p.runtime == nil || revision == 0 {
		return kernel.Snapshot{}, ErrInvalidBinding
	}
	snap, err := p.runtime.Load(ctx, id)
	if err != nil {
		return snap, err
	}
	if snap.Run.Revision != revision {
		return snap, kernel.ErrConflict
	}
	var state remoteState
	if decodeRemoteState(snap.State, &state) != nil || state.TargetID == "" || state.Revision == "" {
		return snap, ErrBindingChanged
	}
	d := handoff.Delegation{Remote: true, MemberID: TargetPrefix + state.TargetID, MemberRevision: state.Revision,
		ChildRunID: id, Goal: snap.Run.Goal, ID: "control", Status: handoff.StatusRunning}
	child, err := p.ResolveChild(ctx, d)
	if err != nil {
		return snap, err
	}
	remote := child.(*ShadowRunner)
	if _, err = remote.decodeState(snap); err != nil {
		return snap, err
	}
	return remote.CancelRun(ctx, id, revision)
}

// OwnsRemoteDelegation is the read-only proof required by the Harness
// RuntimeCanceller: the Host's exact Kernel, admitted member revision and
// signed ACP session state must match before any cancellation is dispatched.
func (p *Plugin) OwnsRemoteDelegation(
	ctx context.Context, rt *kernel.Runtime, d handoff.Delegation, snapshot kernel.Snapshot,
) bool {
	if p == nil || p.runtime != rt || !handoff.RemoteGoalOnly(d) {
		return false
	}
	child, err := p.ResolveChild(ctx, d)
	if err != nil {
		return false
	}
	observer, ok := child.(handoff.RemoteShadowObserver)
	return ok && observer.ObservesFrozenRemoteShadow(rt, d, snapshot)
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}
func validBinding(b Binding, id, revision string) bool {
	return b.Client != nil && b.TargetID == id && b.Revision != "" &&
		(revision == "" || b.Revision == revision) && filepath.IsAbs(b.Cwd) && filepath.Clean(b.Cwd) == b.Cwd
}

var _ handoff.ChildRunnerResolver = (*Plugin)(nil)
