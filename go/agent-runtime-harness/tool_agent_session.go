package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ToolAgentSessionOptions configures the legacy bounded local-Tool facade.
//
// Deprecated: Use AgentSessionOptions with LocalTools and an explicit archive grant.
type ToolAgentSessionOptions struct {
	Runtime            *kernel.Runtime
	Store              Store
	Model              model.Client
	Tools              []SessionTool
	ResolveModelWindow ModelWindowResolver
	Counter            runtimecontext.TokenCounter // optional; conservative fallback otherwise
	RunExecution       *agent.RunExecution
	Inputs             agent.RunInputSource // optional; Host authenticates/persists queued input
	Session            SessionOptions
}

// OpenToolAgentSession retains its automatic scoped reader, read exploration
// and legacy unspecified output cap for existing callers.
//
// Deprecated: Use OpenAgentSession. See docs/agent-session.md for migration.
func OpenToolAgentSession(options ToolAgentSessionOptions) (*SessionClient, error) {
	config := cloneConfigSnapshot(options.Session.Config)
	if options.Runtime == nil || options.Store == nil || options.Model == nil || options.ResolveModelWindow == nil ||
		len(config.ToolKeys) != 0 || len(config.ToolPolicies) != 0 || len(config.Skills) != 0 ||
		strings.TrimSpace(config.MemoryPolicy) != "" || config.SharedBudget != nil || config.WorkingStateSummary != nil ||
		config.MaxModelOutputTokens < 0 ||
		config.DelegationPolicy.MaxDepth != 0 || config.ApprovalPolicyVersion != 0 || config.ContentHash != "" {
		return nil, ErrInvalidRequest
	}
	registrations, reads, err := selectSessionTools(&config, options.Tools, true)
	if err != nil {
		return nil, err
	}
	options.Session.Config = config
	return openBoundedAgentSession(AgentSessionOptions{
		Runtime: options.Runtime, Store: options.Store, Model: options.Model,
		Session: options.Session, Context: AgentSessionContextOptions{ResolveWindow: options.ResolveModelWindow, Counter: options.Counter},
		RunExecution: options.RunExecution, Limits: config.Limits, Inputs: options.Inputs,
	}, registrations, reads, true)
}
