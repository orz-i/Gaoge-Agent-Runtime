package harness

import (
	"context"
	"slices"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// selectNamedAgents is the Host composition boundary, not a Runtime catalog.
// The explicit names are the entire allowlist; a model may submit only the
// member ID and task Goal. Authorization and exact version are frozen first.
func selectNamedAgents(options *AgentSessionOptions) error {
	if len(options.NamedAgents) == 0 {
		return nil
	}
	cfg := &options.Session.Config
	if options.DelegationPolicy != nil || options.Handoffs != nil || len(cfg.DelegationPolicy.NamedAgents) != 0 ||
		options.Relations == nil || cfg.DelegationPolicy.MaxDepth <= 0 {
		return ErrInvalidRequest
	}
	names := make([]NamedAgentSnapshot, 0, len(options.NamedAgents))
	for _, n := range options.NamedAgents {
		keys := append([]string(nil), n.ToolKeys...)
		for _, t := range n.Tools {
			key := t.Registration.Definition.Key
			keys = append(keys, key)
			if t.Policy.ExecutionMode == "" {
				t.Policy.ExecutionMode = "local_dispatch"
			}
			options.MemberTools = append(options.MemberTools, t)
		}
		names = append(names, NamedAgentSnapshot{
			MemberID: n.MemberID, MemberRevision: n.MemberRevision,
			Model: n.Model, ModelOptions: n.ModelOptions, Instructions: n.Instructions,
			ToolKeys: keys, InitialToolKeys: n.InitialToolKeys,
			HostedToolGrants: n.HostedToolGrants, Skills: n.Skills,
			Limits: n.Limits, MaxModelOutputTokens: n.MaxModelOutputTokens,
		})
	}
	normalized, err := normalizeNamedAgentSnapshots(names)
	if err != nil {
		return err
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
		request.Agent = agent.StartRequest{
			Model: selected.Model, ModelOptions: append([]byte(nil), selected.ModelOptions...),
			Instructions: selected.Instructions, ToolKeys: slices.Clone(selected.ToolKeys),
			InitialToolKeys:  slices.Clone(selected.InitialToolKeys),
			HostedToolGrants: slices.Clone(selected.HostedToolGrants),
			Limits:           selected.Limits, MaxModelOutputTokens: selected.MaxModelOutputTokens,
		}
		return request, nil
	}
	return DelegateRequest{}, ErrInvalidRequest
}
