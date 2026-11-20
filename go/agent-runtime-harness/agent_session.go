package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// AgentSessionContextOptions supplies the routed model's actual window and an
// optional exact counter. ResolveWindow runs at the existing pre-intent Context
// boundary, never while OpenAgentSession constructs the session.
type AgentSessionContextOptions struct {
	ResolveWindow ModelWindowResolver
	Counter       runtimecontext.TokenCounter
	// ManagedRecovery explicitly selects the existing concrete Context recovery
	// lifecycle. Runtime must already be bound to this exact Harness Store.
	// The default uses ordinary bounded Context and its existing receipt-based
	// recovery; arbitrary interrupted callbacks do not become safe to replay.
	ManagedRecovery bool
	// SummaryNativeRecovery explicitly enables the source-bound summary safe
	// points. Runtime must use this exact Harness Store as ManagedRecoveryHost.
	// It is separate from ordinary receipt recovery and ManagedRecovery.
	SummaryNativeRecovery bool
}

// AgentSessionOptions composes a single tool-using Agent with bounded Harness
// Context. The Host owns both Stores, authenticated identity, model binding and
// transcript. Config.ToolKeys is the explicit authorization set; registering a
// Tool does not authorize it. Only selected, versioned never-approval Tools are
// supported by this facade. Advanced capabilities use the explicit runners.
type AgentSessionOptions struct {
	Runtime      *kernel.Runtime
	Store        Store
	Model        model.Client
	Tools        []tools.Registration
	Session      SessionOptions
	Context      AgentSessionContextOptions
	Limits       agent.Limits
	Execution    agent.ExecutionPolicy
	RunExecution *agent.RunExecution
}

// OpenAgentSession builds the existing Agent, Context adapter and Harness; it
// does not read history, write a Store or call a model. A nil Transcript permits
// reopening for Load/Refresh/Resume, while Prompt still requires Host history.
// Selecting ContextArtifactToolKey explicitly installs the standard scoped
// reader. Its reserved registration cannot be overridden by Tools.
func OpenAgentSession(options AgentSessionOptions) (*SessionClient, error) {
	options.Session.Config.ContextBase = nil
	if options.Runtime == nil || options.Store == nil || options.Model == nil || options.Context.ResolveWindow == nil {
		return nil, ErrInvalidRequest
	}
	if options.Context.ManagedRecovery && options.Context.SummaryNativeRecovery {
		return nil, ErrInvalidRequest
	}
	options.Session.Config = cloneConfigSnapshot(options.Session.Config)
	config := &options.Session.Config
	if config.DelegationPolicy.MaxDepth != 0 {
		return nil, ErrInvalidRequest
	}
	normalized, err := normalizeAgentSessionConfig(*config)
	if err != nil {
		return nil, err
	}
	*config = normalized
	if options.Context.ManagedRecovery && config.WorkingStateSummary != nil {
		return nil, ErrInvalidRequest
	}
	if options.Context.ManagedRecovery || options.Context.SummaryNativeRecovery {
		host, ok := options.Store.(managedHostStore)
		if !ok || !options.Runtime.SupportsManagedRecovery() || !options.Runtime.UsesManagedRecoveryHost(host) {
			return nil, ErrInvalidRequest
		}
	}
	registry, err := agentSessionRegistry(config, options.Tools, options.Store)
	if err != nil {
		return nil, err
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: options.Context.Counter})
	resolve := options.Context.ResolveWindow
	resolveWindow := func(ctx context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		window, resolveErr := resolve(ctx, model.CloneRequest(request))
		if resolveErr != nil {
			return runtimecontext.ModelWindow{}, resolveErr
		}
		if _, windowErr := runtimecontext.EffectiveInputLimit(window, 0); windowErr != nil {
			return runtimecontext.ModelWindow{}, windowErr
		}
		// Use this invocation's frozen ceiling, not a reopened handle's Config.
		if request.MaxOutputTokens <= 0 || window.ReservedOutputTokens < request.MaxOutputTokens {
			return runtimecontext.ModelWindow{}, ErrInvalidRequest
		}
		return window, nil
	}
	bounded, err := NewBoundedContextAdapter(options.Store, manager, resolveWindow, nil)
	if err != nil {
		return nil, err
	}
	approval, err := NewFrozenApprovalPolicy(options.Store)
	if err != nil {
		return nil, err
	}
	dependencies := agent.Dependencies{
		Runtime: options.Runtime, Model: options.Model, Catalog: registry, Executor: registry,
		ApprovalPolicies: []plugin.ApprovalPolicy{approval},
		RunMiddleware:    []plugin.RunMiddleware{bounded},
		Limits:           options.Limits, Execution: options.Execution, RunExecution: options.RunExecution,
	}
	if options.Context.ManagedRecovery {
		dependencies.ManagedContextRecovery = bounded
	} else {
		dependencies.ContextSummary, err = NewContextSummaryAdapter(options.Store, options.Runtime, bounded, resolveWindow)
		if err != nil {
			return nil, err
		}
		dependencies.SummaryNativeRecovery = options.Context.SummaryNativeRecovery
	}
	loop, err := agent.NewRunner(dependencies)
	if err != nil {
		return nil, err
	}
	host, err := NewRunner(Dependencies{
		Runtime: options.Runtime, Agent: loop, Store: options.Store, Context: manager, Catalog: registry,
	})
	if err != nil {
		return nil, err
	}
	session, err := host.OpenSession(options.Session)
	if err != nil {
		return nil, err
	}
	session.agentSessionOnly = true
	return session, nil
}

// normalizeAgentSessionConfig checks composition requirements, not a new
// execution policy. A persisted Turn's default delegation depth alone does not
// require a delegation engine; selected delegation Tools and child facts do.
func normalizeAgentSessionConfig(config ConfigSnapshot) (ConfigSnapshot, error) {
	if jsoncontract.ValidateCanonicalUTF8(config) != nil || config.MaxModelOutputTokens <= 0 ||
		len(config.Skills) != 0 || strings.TrimSpace(config.MemoryPolicy) != "" || config.SharedBudget != nil ||
		agent.ValidateWorkingStateSummaryPolicy(config.WorkingStateSummary) != nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	config.WorkingStateSummary = agent.CloneWorkingStateSummaryPolicy(config.WorkingStateSummary)
	config.ToolKeys = normalizeStrings(config.ToolKeys)
	policies, err := normalizeToolPolicies(config.ToolPolicies)
	if err != nil || len(policies) != len(config.ToolKeys) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	config.ToolPolicies = policies
	selected := make(map[string]bool, len(config.ToolKeys))
	for _, key := range config.ToolKeys {
		if key == DelegationToolKey {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		selected[key] = true
	}
	for _, policy := range policies {
		if !selected[policy.Key] || !strings.EqualFold(policy.ApprovalMode, approvalModeNever) ||
			(policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if policy.Key == ContextArtifactToolKey && policy != ContextArtifactToolPolicySnapshot() {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
	}
	return config, nil
}

func agentSessionRegistry(config *ConfigSnapshot, registrations []tools.Registration, store Store) (*tools.Registry, error) {
	owned := make([]tools.Registration, 0, len(registrations)+1)
	archiveReader := false
	for _, key := range config.ToolKeys {
		archiveReader = archiveReader || key == ContextArtifactToolKey
	}
	for _, registration := range registrations {
		if strings.TrimSpace(registration.Definition.Key) == ContextArtifactToolKey {
			return nil, ErrInvalidRequest
		}
		owned = append(owned, registration)
	}
	if archiveReader {
		owned = append(owned, ContextArtifactToolRegistration(NewContextArtifactToolHandler(store, nil)))
	}
	registry, err := tools.NewRegistry(owned)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	if _, err = registry.List(config.ToolKeys); err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	return registry, nil
}

// preflightAgentSessionResume reads the original sealed configuration before
// either managed recovery or ordinary Agent.Resume can issue effects. Current
// handle settings cannot silently replace a previously required composition.
func (session *SessionClient) preflightAgentSessionResume(ctx context.Context, turnID string) error {
	store := session.runner.store
	turn, err := loadVerifiedTurn(ctx, store, turnID)
	if err != nil {
		return err
	}
	config, err := loadSealedConfig(ctx, store, turn.ConfigSnapshotID)
	if err != nil {
		return err
	}
	if _, err = normalizeAgentSessionConfig(config); err != nil {
		return err
	}
	if config.WorkingStateSummary != nil {
		if native, ok := session.runner.agent.(*agent.Runner); !ok || !native.SupportsContextSummary() || native.SupportsManagedContextRecovery() {
			return ErrInvalidRequest
		}
		root, err := loadTopLevelInvocation(ctx, store, turn.ID)
		if err != nil {
			return err
		}
		snapshot, loadErr := session.runner.runtime.Load(ctx, root.ExecutionRefID)
		if loadErr != nil && !errors.Is(loadErr, kernel.ErrNotFound) {
			return loadErr
		}
		if loadErr == nil {
			view, viewErr := agent.ViewState(snapshot)
			native := session.runner.agent.(*agent.Runner)
			if viewErr != nil || view.SummaryNativeRecovery != native.SupportsSummaryNativeRecovery() {
				return ErrInvalidRequest
			}
		}
		// An admitted Turn can lack a Run before native Start, or after a
		// proven no-effect failure/cancellation. Resume owns its existing
		// readable snapshot and ErrNotFound/terminal behavior; this preflight
		// must not replace it or create execution to reconstruct a mode.
	}
	if session.runner.catalog == nil {
		return ErrInvalidRequest
	}
	if _, err = session.runner.catalog.List(config.ToolKeys); err != nil {
		return errors.Join(ErrInvalidRequest, err)
	}
	owners, err := store.ListInvocations(ctx, turnID)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if owner.TurnID != turnID {
			return ErrConflict
		}
		if owner.ParentItemID != "" {
			return ErrInvalidRequest
		}
	}
	items, err := listAllItems(ctx, store, turnID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.TurnID != turnID {
			return ErrConflict
		}
		if item.Kind == ItemDelegation || item.Kind == ItemSubtask || item.Kind == ItemApproval ||
			item.Kind == ItemInteraction || item.Kind == ItemBudget {
			return ErrInvalidRequest
		}
	}
	return nil
}
