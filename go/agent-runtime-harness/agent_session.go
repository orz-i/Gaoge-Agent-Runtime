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
	// ReadArtifacts explicitly grants the built-in scoped archive reader.
	// It cannot be combined with the legacy Config.ToolKeys/ToolPolicies form.
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
// Only explicitly selected, versioned local Tools that do not require approval
// are supported; use the explicit runners for advanced capabilities.
type AgentSessionOptions struct {
	Runtime *kernel.Runtime
	Store   Store
	Model   model.Client
	// LocalTools supplies both the selected handlers and their policies. Leave
	// Config.ToolKeys and Config.ToolPolicies unset when using this field.
	LocalTools []SessionTool
	// ExploreReads opts Host-certified nonterminal read tools (including an
	// explicitly granted archive reader) into bounded exploration. It is only
	// available with the paired LocalTools/ReadArtifacts selection.
	ExploreReads bool
	// Inputs optionally binds an authenticated, Host-owned user input log.
	// It is not supported with either native managed recovery option.
	Inputs agent.RunInputSource
	// Tools retains the existing inventory + explicit Config selection form.
	// Registering a tool here does not authorize it.
	//
	// Deprecated: Pair selected registrations and policies in LocalTools.
	Tools        []tools.Registration
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
	if config.DelegationPolicy.MaxDepth != 0 {
		return nil, ErrInvalidRequest
	}
	registrations := options.Tools
	var exploration []string
	if len(options.LocalTools) != 0 || options.Context.ReadArtifacts {
		if len(options.Tools) != 0 || len(config.ToolKeys) != 0 || len(config.ToolPolicies) != 0 {
			return nil, ErrInvalidRequest // One authorization source, not a merge.
		}
		paired, reads, err := selectSessionTools(config, options.LocalTools, options.Context.ReadArtifacts)
		if err != nil {
			return nil, err
		}
		registrations = paired
		if options.ExploreReads {
			exploration = reads
		}
	} else if options.ExploreReads {
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
	return openBoundedAgentSession(options, registrations, exploration, false)
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
			policy.ApprovalMode != approvalModeNever || policy.ApprovalCapability != "" ||
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

// Both bounded facades use the same registry, Context and runner assembly.
// The deprecated Tool facade retains its original callback stack, zero-cap
// behavior and Resume handling until its source-level removal.
func openBoundedAgentSession(options AgentSessionOptions, registrations []tools.Registration, exploration []string, legacyTool bool) (*SessionClient, error) {
	registry, err := agentSessionRegistry(options.Session.Config.ToolKeys, registrations, options.Store)
	if err != nil {
		if legacyTool {
			return nil, err
		}
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: options.Context.Counter})
	resolve := options.Context.ResolveWindow
	resolveWindow := func(ctx context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
		window, resolveErr := resolve(ctx, model.CloneRequest(request))
		if resolveErr != nil {
			return runtimecontext.ModelWindow{}, resolveErr
		}
		if !legacyTool {
			if _, windowErr := runtimecontext.EffectiveInputLimit(window, 0); windowErr != nil {
				return runtimecontext.ModelWindow{}, windowErr
			}
		}
		// Use this invocation's frozen ceiling, not a reopened handle's Config.
		if (!legacyTool && request.MaxOutputTokens <= 0) || (request.MaxOutputTokens > 0 && window.ReservedOutputTokens < request.MaxOutputTokens) {
			return runtimecontext.ModelWindow{}, ErrInvalidRequest
		}
		return window, nil
	}
	bounded, err := NewBoundedContextAdapter(options.Store, manager, resolveWindow, nil)
	if err != nil {
		return nil, err
	}
	dependencies := agent.Dependencies{
		Runtime: options.Runtime, Model: options.Model, Catalog: registry, Executor: registry,
		RunMiddleware: []plugin.RunMiddleware{bounded}, Inputs: options.Inputs,
		Limits: options.Limits, Execution: options.Execution, RunExecution: options.RunExecution,
	}
	if len(exploration) != 0 {
		dependencies.ToolProgressPolicy = &agent.ToolProgressPolicy{ExplorationToolKeys: exploration}
	}
	if legacyTool {
		dependencies.PrepareModelRequest = bounded.Prepare
	} else {
		approval, approvalErr := NewFrozenApprovalPolicy(options.Store)
		if approvalErr != nil {
			return nil, approvalErr
		}
		dependencies.ApprovalPolicies = []plugin.ApprovalPolicy{approval}
		if options.Context.ManagedRecovery {
			dependencies.ManagedContextRecovery = bounded
		} else {
			dependencies.ContextSummary, err = NewContextSummaryAdapter(options.Store, options.Runtime, bounded, resolveWindow)
			if err != nil {
				return nil, err
			}
			dependencies.SummaryNativeRecovery = options.Context.SummaryNativeRecovery
		}
	}
	session, err := openComposedSession(dependencies, options.Store, manager, options.Session)
	if err != nil {
		return nil, err
	}
	session.agentSessionOnly = !legacyTool
	return session, nil
}

func openComposedSession(dependencies agent.Dependencies, store Store, manager *runtimecontext.Manager, options SessionOptions) (*SessionClient, error) {
	loop, err := agent.NewRunner(dependencies)
	if err != nil {
		return nil, err
	}
	host, err := NewRunner(Dependencies{
		Runtime: dependencies.Runtime, Agent: loop, Store: store, Context: manager, Catalog: dependencies.Catalog,
	})
	if err != nil {
		return nil, err
	}
	return host.OpenSession(options)
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

func agentSessionRegistry(selected []string, registrations []tools.Registration, store Store) (*tools.Registry, error) {
	owned := make([]tools.Registration, 0, len(registrations)+1)
	archiveReader := false
	for _, key := range selected {
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
		return nil, err
	}
	if _, err = registry.List(selected); err != nil {
		return nil, err
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
