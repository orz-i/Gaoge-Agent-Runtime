package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// TextAgentSessionOptions configures the legacy unbounded text facade.
//
// Deprecated: Use AgentSessionOptions without local Tools for bounded text Sessions.
type TextAgentSessionOptions struct {
	Runtime      *kernel.Runtime
	Store        Store
	Model        model.Client
	RunExecution *agent.RunExecution // optional; same Agent lease contract
	Session      SessionOptions
}

// OpenTextAgentSession retains its original unbounded text projection for
// existing callers. The Host still owns the conversation transcript.
//
// Deprecated: Use OpenAgentSession. See docs/agent-session.md for migration.
func OpenTextAgentSession(options TextAgentSessionOptions) (*SessionClient, error) {
	if options.Runtime == nil || options.Store == nil || options.Model == nil ||
		len(options.Session.Config.ToolKeys) != 0 || len(options.Session.Config.ToolPolicies) != 0 ||
		len(options.Session.Config.Skills) != 0 || strings.TrimSpace(options.Session.Config.MemoryPolicy) != "" ||
		runtimecontext.NormalizePolicy(options.Session.Config.ContextPolicy) != runtimecontext.NormalizePolicy(runtimecontext.Policy{}) ||
		options.Session.Config.SharedBudget != nil || options.Session.Config.DelegationPolicy.MaxDepth != 0 {
		return nil, ErrInvalidRequest
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	rehydrate, err := NewContextRunMiddleware(options.Store, nil)
	if err != nil {
		return nil, err
	}
	return openComposedSession(agent.Dependencies{
		Runtime: options.Runtime, Model: options.Model, RunExecution: options.RunExecution,
		PrepareModelRequest: MaterializeContextWindowRequest,
		RunMiddleware:       []plugin.RunMiddleware{rehydrate},
	}, options.Store, manager, options.Session)
}
