package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// selectNamedAgents is the Host composition boundary, not a Runtime catalog.
// The explicit names are the entire allowlist; a model may submit only the
// member ID and task Goal. Authorization and exact version are frozen first.
func selectNamedAgents(options *AgentSessionOptions) error {
	// Frozen snapshots are output, not a second Host selection API.
	if len(options.Session.Config.DelegationPolicy.NamedAgents) != 0 {
		return ErrInvalidRequest
	}
	if len(options.NamedAgents) == 0 {
		return nil
	}
	cfg := &options.Session.Config
	if options.DelegationPolicy != nil || options.Handoffs != nil || options.Relations == nil {
		return ErrInvalidRequest
	}
	names := make([]NamedAgentSnapshot, 0, len(options.NamedAgents))
	for _, n := range options.NamedAgents {
		keys := append([]string(nil), n.ToolKeys...)
		for _, t := range n.Tools {
			key := t.Registration.Definition.Key
			keys = append(keys, key)
			options.MemberTools = append(options.MemberTools, t)
		}
		spec := cloneAgentSpec(n.AgentSpec)
		spec.ToolKeys = keys
		names = append(names, NamedAgentSnapshot{
			MemberID: n.MemberID, MemberRevision: n.MemberRevision, Remote: n.Remote,
			AgentSpec: spec,
		})
	}
	normalized, err := normalizeNamedAgentSnapshots(names)
	if err != nil {
		return err
	}
	// The Host binds an explicit remote route only when a remote member is
	// selected; local-only rosters retain the original native loop.
	remote := false
	for _, selected := range normalized {
		remote = remote || selected.Remote
	}
	if remote != (options.RemoteChildren != nil) {
		return ErrInvalidRequest
	}
	cfg.DelegationPolicy.NamedAgents = normalized
	options.DelegationPolicy = namedAgentPolicy{selected: cloneNamedAgentSnapshots(normalized)}
	return nil
}

func namedAgentHasSkills(values []NamedAgentSnapshot) bool {
	for _, n := range values {
		if len(n.Skills) != 0 {
			return true
		}
	}
	return false
}

// namedAgentPolicy is a static Host-selected delegation authorization policy.
// It never resolves a Model/Tool/Skill from mutable Runtime or model text.
type namedAgentPolicy struct{ selected []NamedAgentSnapshot }

func (p namedAgentPolicy) PrepareDelegation(_ context.Context, _ tools.ExecutionRequest, request DelegateRequest) (DelegateRequest, error) {
	for _, selected := range p.selected {
		if selected.MemberID != request.MemberID {
			continue
		}
		if request.MemberRevision != "" && request.MemberRevision != selected.MemberRevision {
			return DelegateRequest{}, ErrInvalidRequest
		}
		request.MemberRevision = selected.MemberRevision
		request.Agent = selected.startRequest()
		request.Remote = selected.Remote
		return request, nil
	}
	return DelegateRequest{}, ErrInvalidRequest
}
