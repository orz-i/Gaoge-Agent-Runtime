package harness

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// SessionTool pairs a local Tool registration with its explicit Host policy.
// Policy.Key may be omitted; when supplied it must match Definition.Key.
type SessionTool struct {
	Registration tools.Registration
	Policy       ToolPolicySnapshot
}

// ToolAgentSessionOptions is the recommended bounded local-Tool composition.
// The Host owns both Stores, identity, transcript, model capacity and actual Tool
// semantics. This facade supports local Tools with explicit ApprovalMode=never;
// use the full runners for approval, delegation, middleware or shared budgets.
// Persistent Stores are required for process-loss recovery; Memory is ephemeral.
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

// OpenToolAgentSession wires the existing Agent loop, bounded Context lifecycle,
// exact archive reader and Harness Session. Host-certified read tools opt into
// bounded different-argument exploration. Writes retain legacy suppression.
// It creates no Store, transcript, alternative execution loop or worker.
func OpenToolAgentSession(options ToolAgentSessionOptions) (*SessionClient, error) {
	config := cloneConfigSnapshot(options.Session.Config)
	if options.Runtime == nil || options.Store == nil || options.Model == nil || options.ResolveModelWindow == nil ||
		len(config.ToolKeys) != 0 || len(config.ToolPolicies) != 0 || len(config.Skills) != 0 ||
		strings.TrimSpace(config.MemoryPolicy) != "" || config.SharedBudget != nil ||
		config.DelegationPolicy.MaxDepth != 0 || config.ApprovalPolicyVersion != 0 || config.ContentHash != "" {
		return nil, ErrInvalidRequest
	}
	registrations := make([]tools.Registration, 0, len(options.Tools)+1)
	readKeys := []string{ContextArtifactToolKey}
	for _, selected := range options.Tools {
		key := selected.Registration.Definition.Key
		policy := selected.Policy
		if policy.Key == "" {
			policy.Key = key
		}
		policies, err := normalizeToolPolicies([]ToolPolicySnapshot{policy})
		if err != nil {
			return nil, err
		}
		policy = policies[0]
		if policy.Key != key || key == ContextArtifactToolKey || key == DelegationToolKey || key == agent.ToolDiscoveryKey ||
			policy.ApprovalMode != "never" || policy.ApprovalCapability != "" ||
			(policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") ||
			(policy.SideEffectLevel != "read" && policy.SideEffectLevel != "write") {
			return nil, ErrInvalidRequest
		}
		if policy.SideEffectLevel == "read" && !selected.Registration.Definition.Terminal {
			readKeys = append(readKeys, key)
		}
		registrations = append(registrations, selected.Registration)
		config.ToolKeys = append(config.ToolKeys, key)
		config.ToolPolicies = append(config.ToolPolicies, policy)
	}
	reader := ContextArtifactToolRegistration(NewContextArtifactToolHandler(options.Store, nil))
	registrations = append(registrations, reader)
	config.ToolKeys = append(config.ToolKeys, ContextArtifactToolKey)
	config.ToolPolicies = append(config.ToolPolicies, ContextArtifactToolPolicySnapshot())
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		return nil, err
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: options.Counter})
	bounded, err := NewBoundedContextAdapter(options.Store, manager, options.ResolveModelWindow, nil)
	if err != nil {
		return nil, err
	}
	loop, err := agent.NewRunner(agent.Dependencies{
		Runtime: options.Runtime, Model: options.Model, Catalog: registry, Executor: registry,
		RunExecution: options.RunExecution, Limits: config.Limits,
		Inputs:              options.Inputs,
		PrepareModelRequest: bounded.Prepare, RunMiddleware: []plugin.RunMiddleware{bounded},
		ToolProgressPolicy: &agent.ToolProgressPolicy{ExplorationToolKeys: readKeys},
	})
	if err != nil {
		return nil, err
	}
	host, err := NewRunner(Dependencies{
		Runtime: options.Runtime, Agent: loop, Store: options.Store, Context: manager, Catalog: registry,
	})
	if err != nil {
		return nil, err
	}
	options.Session.Config = config
	return host.OpenSession(options.Session)
}
