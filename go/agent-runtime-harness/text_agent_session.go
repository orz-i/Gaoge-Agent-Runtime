package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// TextAgentSessionOptions supplies only the dependencies needed for a plain
// text Agent conversation. The caller owns the Kernel and Harness Stores,
// model client, authenticated identity, and Host transcript.
//
// TranscriptHistoryFunc is the standard pure-text transcript adapter;
// a custom TranscriptSource must enforce its own Host history contract.
// Use the explicit Agent/Harness runners for Tools, custom middleware,
// delegation, approvals, shared budgets, or other advanced capabilities.
// RunExecution is opt-in and keeps the existing Kernel execution lease rules.
// Persistent Stores and the original Provider binding are still required for
// safe cross-process continuation of in-flight Runs.
type TextAgentSessionOptions struct {
	Runtime      *kernel.Runtime
	Store        Store
	Model        model.Client
	RunExecution *agent.RunExecution // optional; same Agent lease contract
	Session      SessionOptions
}

// OpenTextAgentSession composes the existing single Agent loop and Harness
// Session, including the durable Context rehydration middleware. It creates
// no additional model loop, Store, execution mode, or Host intent router.
// The Host must still persist its conversation transcript independently.
func OpenTextAgentSession(options TextAgentSessionOptions) (*SessionClient, error) {
	if options.Runtime == nil || options.Store == nil || options.Model == nil ||
		len(options.Session.Config.ToolKeys) != 0 || len(options.Session.Config.ToolPolicies) != 0 ||
		len(options.Session.Config.Commands) != 0 || len(options.Session.Config.Roles) != 0 ||
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
	loop, err := agent.NewRunner(agent.Dependencies{
		Runtime: options.Runtime, Model: options.Model, RunExecution: options.RunExecution,
		PrepareModelRequest: MaterializeContextWindowRequest,
		RunMiddleware:       []plugin.RunMiddleware{rehydrate},
	})
	if err != nil {
		return nil, err
	}
	host, err := NewRunner(Dependencies{
		Runtime: options.Runtime, Agent: loop, Store: options.Store, Context: manager,
	})
	if err != nil {
		return nil, err
	}
	return host.OpenSession(options.Session)
}
