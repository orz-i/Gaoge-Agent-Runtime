package harness

import (
	"context"
	"errors"
	"strings"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
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
	// ReadArtifacts explicitly grants the built-in scoped archive reader.
	ReadArtifacts bool
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

// SessionTool pairs one selected local Tool with its explicit Host policy.
// Policy.Key may be omitted; when supplied it must match Definition.Key.
type SessionTool struct {
	Registration tools.Registration
	Policy       ToolPolicySnapshot
}

// AgentSessionOptions composes a single Agent with bounded Harness Context.
// The Host owns both Stores, authenticated identity, model binding and transcript.
// Selected local Tools may request durable per-call approval. The standard
// Session uses the same Kernel approval checkpoint and Harness Turn policy.
type AgentSessionOptions struct {
	Runtime *kernel.Runtime
	Store   Store
	Model   model.Client
	// LocalTools supplies both the selected handlers and their policies. Leave
	// Config.ToolKeys and Config.ToolPolicies unset when using this field.
	LocalTools []SessionTool
	// Handoffs, Relations and DelegationPolicy are the Host's already-authorized
	// SubAgent route, original owner index and policy. All three are required
	// when Config.DelegationPolicy enables the built-in delegation Tool.
	Handoffs         HandoffStarter
	Relations        RunRelationSource
	DelegationPolicy DelegationToolPolicy
	// ExploreReads opts Host-certified nonterminal read tools (including an
	// explicitly granted archive reader) into bounded exploration.
	ExploreReads bool
	// Inputs optionally binds an authenticated, Host-owned user input log.
	// It is not supported with either native managed recovery option.
	Inputs       agent.RunInputSource
	Session      SessionOptions
	Context      AgentSessionContextOptions
	Limits       agent.Limits
	Execution    agent.ExecutionPolicy
	RunExecution *agent.RunExecution
}

// OpenAgentSession is the standard bounded Session constructor, with or without
// local Tools. It does not read history, write a Store or call a model. A nil
// Transcript permits reopening for Load/Refresh/Resume but not Prompt.
func OpenAgentSession(options AgentSessionOptions) (*SessionClient, error) {
	options.Session.Config.ContextBase = nil
	if options.Runtime == nil || options.Store == nil || options.Model == nil || options.Context.ResolveWindow == nil {
		return nil, ErrInvalidRequest
	}
	if (options.Context.ManagedRecovery && options.Context.SummaryNativeRecovery) ||
		(options.Inputs != nil && (options.Context.ManagedRecovery || options.Context.SummaryNativeRecovery)) {
		return nil, ErrInvalidRequest
	}
	options.Session.Config = cloneConfigSnapshot(options.Session.Config)
	config := &options.Session.Config
	if len(config.ToolKeys) != 0 || len(config.ToolPolicies) != 0 {
		return nil, ErrInvalidRequest
	}
	delegated := options.Handoffs != nil || options.Relations != nil || options.DelegationPolicy != nil ||
		config.DelegationPolicy.MaxDepth != 0 || len(config.DelegationPolicy.LocalToolGrants) != 0
	if delegated {
		if options.Handoffs == nil || options.Relations == nil || options.DelegationPolicy == nil ||
			config.DelegationPolicy.MaxDepth <= 0 || len(config.DelegationPolicy.LocalToolGrants) != 0 {
			// Member-specific Tool grants need the advanced Host composition;
			// never expose a Child-only Tool to the root to make wiring easier.
			return nil, ErrInvalidRequest
		}
	}
	if options.ExploreReads && len(options.LocalTools) == 0 && !options.Context.ReadArtifacts {
		return nil, ErrInvalidRequest
	}
	registrations, exploration, err := selectSessionTools(config, options.LocalTools, options.Context.ReadArtifacts)
	if err != nil {
		return nil, err
	}
	if delegated {
		config.ToolKeys = append(config.ToolKeys, DelegationToolKey)
		config.ToolPolicies = append(config.ToolPolicies, DelegationToolPolicySnapshot())
	}
	if !options.ExploreReads {
		exploration = nil
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
	return openBoundedAgentSession(options, registrations, exploration)
}

func selectSessionTools(config *ConfigSnapshot, selectedTools []SessionTool, readArtifacts bool) ([]tools.Registration, []string, error) {
	registrations := make([]tools.Registration, 0, len(selectedTools))
	var reads []string
	for _, selected := range selectedTools {
		key := selected.Registration.Definition.Key
		policy := selected.Policy
		if policy.Key == "" {
			policy.Key = key
		}
		policies, err := normalizeToolPolicies([]ToolPolicySnapshot{policy})
		if err != nil {
			return nil, nil, err
		}
		policy = policies[0]
		if policy.Key != key || key == ContextArtifactToolKey || key == DelegationToolKey || key == agent.ToolDiscoveryKey ||
			!(policy.ApprovalMode == approvalModeNever && policy.ApprovalCapability == "" ||
				policy.ApprovalMode == approvalModeAlways && policy.ApprovalCapability == approvalCapabilityPerCall) ||
			(policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") ||
			(policy.SideEffectLevel != "read" && policy.SideEffectLevel != "write") {
			return nil, nil, ErrInvalidRequest
		}
		if policy.SideEffectLevel == "read" && !selected.Registration.Definition.Terminal {
			reads = append(reads, key)
		}
		registrations = append(registrations, selected.Registration)
		config.ToolKeys = append(config.ToolKeys, key)
		config.ToolPolicies = append(config.ToolPolicies, policy)
	}
	if readArtifacts {
		config.ToolKeys = append(config.ToolKeys, ContextArtifactToolKey)
		config.ToolPolicies = append(config.ToolPolicies, ContextArtifactToolPolicySnapshot())
		reads = append(reads, ContextArtifactToolKey)
	}
	return registrations, reads, nil
}

func openBoundedAgentSession(options AgentSessionOptions, registrations []tools.Registration, exploration []string) (*SessionClient, error) {
	var delegation *DelegationToolHandler
	if options.DelegationPolicy != nil {
		delegation = NewDelegationToolHandler()
		registrations = append(registrations, DelegationToolRegistration(delegation))
	}
	if options.Context.ReadArtifacts {
		registrations = append(registrations, ContextArtifactToolRegistration(NewContextArtifactToolHandler(options.Store, nil)))
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
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
	approvals, err := interaction.New(options.Runtime)
	if err != nil {
		return nil, err
	}
	dependencies := agent.Dependencies{
		Runtime: options.Runtime, Model: options.Model, Catalog: registry, Executor: registry,
		Approvals:     interactionadapter.New(approvals),
		RunMiddleware: []plugin.RunMiddleware{bounded}, Inputs: options.Inputs,
		Limits: options.Limits, Execution: options.Execution, RunExecution: options.RunExecution,
	}
	if len(exploration) != 0 {
		dependencies.ToolProgressPolicy = &agent.ToolProgressPolicy{ExplorationToolKeys: exploration}
	}
	approval, err := NewFrozenApprovalPolicy(options.Store)
	if err != nil {
		return nil, err
	}
	dependencies.ApprovalPolicies = []plugin.ApprovalPolicy{approval}
	if delegation != nil {
		dependencies.ToolOutcomeVerifier = delegation
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
		Handoffs: options.Handoffs, Relations: options.Relations, DelegationPolicy: options.DelegationPolicy,
	})
	if err != nil {
		return nil, err
	}
	if delegation != nil {
		if err := delegation.Bind(host); err != nil {
			return nil, err
		}
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
		strings.TrimSpace(config.MemoryPolicy) != "" || config.SharedBudget != nil ||
		agent.ValidateWorkingStateSummaryPolicy(config.WorkingStateSummary) != nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	var skillErr error
	config.Skills, skillErr = normalizeSkillSnapshots(config.Skills)
	if skillErr != nil {
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
		selected[key] = true
	}
	for _, policy := range policies {
		if !selected[policy.Key] ||
			(policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if _, err := frozenApprovalRequirement(config, policy.Key); err != nil ||
			(policy.ApprovalMode == approvalModeNever && policy.ApprovalCapability != "" && policy.Key != ContextArtifactToolKey && policy.Key != DelegationToolKey) {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if policy.Key == DelegationToolKey && (policy != DelegationToolPolicySnapshot() || config.DelegationPolicy.MaxDepth <= 0) {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if policy.Key == ContextArtifactToolKey && policy != ContextArtifactToolPolicySnapshot() {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
	}
	return config, nil
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
	allowedSubAgents := false
	for _, key := range config.ToolKeys {
		if key == DelegationToolKey {
			if session.runner.handoffs == nil || session.runner.relationReader == nil || session.runner.delegationPolicy == nil {
				return ErrInvalidRequest
			}
			allowedSubAgents = true
		}
	}
	owners, err := store.ListInvocations(ctx, turnID)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if owner.TurnID != turnID {
			return ErrConflict
		}
		if owner.ParentItemID != "" && !allowedSubAgents {
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
		if (item.Kind == ItemDelegation || item.Kind == ItemSubtask) && !allowedSubAgents ||
			item.Kind == ItemInteraction || item.Kind == ItemBudget {
			return ErrInvalidRequest
		}
	}
	return nil
}
