package harness

import (
	"context"
	"errors"
	"reflect"
	"slices"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// AgentSessionContextOptions supplies the routed model's actual window and an
// optional exact counter and route accounting projection. ResolveWindow runs at the existing pre-intent Context
// boundary, never while OpenAgentSession constructs the session.
type AgentSessionContextOptions struct {
	ResolveWindow ModelWindowResolver
	Counter       runtimecontext.TokenCounter
	Accounting    runtimecontext.RequestAccounting
	// ReadArtifacts explicitly grants the built-in scoped archive reader.
	ReadArtifacts bool
}

// SessionTool pairs one selected local Tool with its explicit Host policy.
// Policy.Key may be omitted; when supplied it must match Definition.Key.
type SessionTool struct {
	Registration tools.Registration
	Policy       ToolPolicySnapshot
}

// NamedAgent is one complete Host-selected Agent: its model, instructions,
// Tools, private Skills and limits are frozen into the original Turn. Tools
// contains registered child-only handlers and per-call authorization policy;
// ToolKeys can refer to previously selected root or member Tools. The model
// sees only MemberID and Goal in the delegation Tool input, never this bundle.
type NamedAgent struct {
	MemberID       string
	MemberRevision string
	Remote         bool // External goal-only execution; not native Model/Tool permissions.
	AgentSpec
	Tools []SessionTool
}

// AgentSessionOptions composes a single Agent with bounded Harness Context.
// The Host owns both Stores, authenticated identity, model binding and transcript.
// Selected local Tools may request durable per-call approval. The standard
// Session uses the same Kernel approval checkpoint and Harness Turn policy.
type AgentSessionOptions struct {
	Runtime *kernel.Runtime
	Store   Store
	Model   model.Client
	// TurnFeed is the existing retained/live delivery transport, shared with
	// the same Harness Runner that executes this Session.
	TurnFeed *TurnFeed
	// BudgetMeter certifies model token reservations and authoritative usage.
	// Count-only SharedBudget does not require a token meter. A selected model
	// that implements TokenAdmissionPlanner is used by default.
	BudgetMeter model.TokenAdmissionPlanner
	// LocalTools supplies both the selected handlers and their policies. Leave
	// Config.ToolKeys and Config.ToolPolicies unset when using this field.
	LocalTools []SessionTool
	// MemberTools are registered but never offered to the root Agent. Their
	// explicit member/revision grants are frozen in Session.Config.DelegationPolicy.
	MemberTools []SessionTool
	// NamedAgents selects a finite Host-owned local/remote Agent roster.
	// Local execution uses one Agent loop; remote members have goal-only
	// capabilities, fixed identity/revision and explicit RemoteChildren.
	NamedAgents    []NamedAgent
	RemoteChildren handoff.ChildRunnerResolver
	// The original native Hosted Tool resolver and local Tool discovery are
	// still Host-owned. Discovery can only reveal already frozen Tool grants.
	HostedTools   agent.HostedToolCatalog
	ToolDiscovery agent.ToolDiscovery
	// Relations and DelegationPolicy select and authorize named SubAgents.
	// Handoffs may explicitly supply a child route when no SharedBudget is used;
	// otherwise one original Agent loop is shared across root and children.
	Handoffs         HandoffStarter
	Relations        RunRelationSource
	DelegationPolicy DelegationToolPolicy
	// ExploreReads opts Host-certified nonterminal read tools (including an
	// explicitly granted archive reader) into bounded exploration.
	ExploreReads bool
	// Inputs optionally binds an authenticated, Host-owned user input log.
	// Managed recovery retains the original binding and committed cursor.
	Inputs  agent.RunInputSource
	Session SessionOptions
	Context AgentSessionContextOptions
	// Per-Agent limits are selected only through Session.Config.AgentSpec.Limits.
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
	options.Session.Config = cloneConfigSnapshot(options.Session.Config)
	config := &options.Session.Config
	if err := selectNamedAgents(&options); err != nil {
		return nil, err
	}
	if len(config.ToolKeys) != 0 || len(config.ToolPolicies) != 0 {
		return nil, ErrInvalidRequest
	}
	delegated := options.DelegationPolicy != nil
	if (delegated && options.Relations == nil) ||
		(!delegated && (options.Handoffs != nil || options.RemoteChildren != nil || options.Relations != nil || len(options.MemberTools) != 0 || len(config.DelegationPolicy.LocalToolGrants) != 0)) {
		return nil, ErrInvalidRequest
	}
	if options.ExploreReads && len(options.LocalTools) == 0 && !options.Context.ReadArtifacts {
		return nil, ErrInvalidRequest
	}
	var registrations []tools.Registration
	var exploration []string
	for group, selectedTools := range [][]SessionTool{options.LocalTools, options.MemberTools} {
		for _, selected := range selectedTools {
			policy, err := normalizedSessionTool(selected)
			if err != nil {
				return nil, err
			}
			if group == 1 && policy.ExecutionMode == "" {
				policy.ExecutionMode = "local_dispatch"
			}
			registrations = append(registrations, selected.Registration)
			config.ToolPolicies = append(config.ToolPolicies, policy)
			if group == 0 {
				config.ToolKeys = append(config.ToolKeys, policy.Key)
				// Approval-gated reads are never autonomous exploration.
				if options.ExploreReads && policy.SideEffectLevel == "read" && policy.ApprovalMode == approvalModeNever && !selected.Registration.Definition.Terminal {
					exploration = append(exploration, policy.Key)
				}
			}
		}
	}
	if options.Context.ReadArtifacts {
		config.ToolKeys = append(config.ToolKeys, ContextArtifactToolKey)
		config.ToolPolicies = append(config.ToolPolicies, ContextArtifactToolPolicySnapshot())
		if options.ExploreReads {
			exploration = append(exploration, ContextArtifactToolKey)
		}
	}
	if len(config.DelegationPolicy.NamedAgents) != 0 {
		root := make(map[string]bool, len(config.ToolKeys))
		for _, key := range config.ToolKeys {
			root[key] = true
		}
		member := make(map[string]bool, len(options.MemberTools))
		for _, tool := range options.MemberTools {
			member[tool.Registration.Definition.Key] = true
		}
		for _, named := range config.DelegationPolicy.NamedAgents {
			for _, key := range named.ToolKeys {
				if nonAssignableRuntimeTool(key) {
					return nil, ErrInvalidRequest
				}
				if root[key] {
					continue
				}
				if !member[key] {
					return nil, ErrInvalidRequest
				}
				config.DelegationPolicy.LocalToolGrants = append(config.DelegationPolicy.LocalToolGrants,
					DelegatedLocalToolGrant{MemberID: named.MemberID, MemberRevision: named.MemberRevision, ToolKey: key})
			}
		}
	}
	if delegated {
		config.ToolKeys = append(config.ToolKeys, DelegationToolKey)
		config.ToolPolicies = append(config.ToolPolicies, DelegationToolPolicySnapshot())
	}
	if len(config.Skills) != 0 {
		config.ToolKeys = append(config.ToolKeys, SkillReadToolKey)
	}
	if len(config.Skills) != 0 || namedAgentHasSkills(config.DelegationPolicy.NamedAgents) {
		config.ToolPolicies = append(config.ToolPolicies, skillReadToolPolicySnapshot())
	}
	if jsoncontract.ValidateCanonicalUTF8(*config) != nil || config.MaxModelOutputTokens <= 0 ||
		agent.ValidateWorkingStateSummaryPolicy(config.WorkingStateSummary) != nil ||
		config.DelegationPolicy.MaxDepth < 0 || config.DelegationPolicy.MaxDepth > maxDelegationDepth {
		return nil, ErrInvalidRequest
	}
	var err error
	config.AgentSpec, err = normalizeAgentSpec(config.AgentSpec)
	if err != nil || (len(config.HostedToolGrants) != 0 && config.Model == "") {
		return nil, ErrInvalidRequest
	}
	config.ToolPolicies, err = normalizeToolPolicies(config.ToolPolicies)
	if err != nil {
		return nil, err
	}
	config.DelegationPolicy.LocalToolGrants, err = normalizeDelegatedLocalToolGrants(*config)
	if err != nil {
		return nil, err
	}
	// Every member-only registration needs an explicit immutable grant. Root
	// policies and built-ins are complete by construction, not a second registry.
	granted := make(map[string]bool)
	for _, grant := range config.DelegationPolicy.LocalToolGrants {
		granted[grant.ToolKey] = true
	}
	if len(granted) != len(options.MemberTools) {
		return nil, ErrInvalidRequest
	}
	if config.SharedBudget != nil {
		if !enforceableHarnessBudgetLimits(*config.SharedBudget) {
			return nil, ErrInvalidRequest
		}
		if options.Runtime.BudgetStore() == nil {
			return nil, ErrInvalidRequest
		}
		if delegated && (options.Handoffs != nil || options.RemoteChildren != nil) {
			// External Agents cannot prove their Model/Tool calls join this ledger.
			return nil, ErrInvalidRequest
		}
	}
	if options.Runtime.HasManagedRecoveryHost() {
		host, ok := options.Store.(kernel.ManagedRecoveryHostStore)
		if !ok || !options.Runtime.SupportsManagedRecovery() || !options.Runtime.UsesManagedRecoveryHost(host) {
			return nil, ErrInvalidRequest
		}
	}
	var delegation *DelegationToolHandler
	if options.DelegationPolicy != nil {
		delegation = NewDelegationToolHandler()
		registrations = append(registrations, DelegationToolRegistration(delegation))
	}
	if options.Context.ReadArtifacts {
		registrations = append(registrations, ContextArtifactToolRegistration(NewContextArtifactToolHandler(options.Store, nil)))
	}
	if len(options.Session.Config.Skills) != 0 || namedAgentHasSkills(options.Session.Config.DelegationPolicy.NamedAgents) {
		registrations = append(registrations, skillReadToolRegistration(options.Store, options.Relations))
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		return nil, errors.Join(ErrInvalidRequest, err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: options.Context.Counter, Accounting: options.Context.Accounting})
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
		HostedTools: options.HostedTools, ToolDiscovery: options.ToolDiscovery,
		Approvals:      interactionadapter.New(approvals),
		BoundedContext: bounded, Inputs: options.Inputs,
		Limits: config.Limits, Execution: options.Execution, RunExecution: options.RunExecution,
	}
	if len(exploration) != 0 {
		dependencies.ToolProgressPolicy = &agent.ToolProgressPolicy{ExplorationToolKeys: exploration}
	}
	frozenApproval, err := NewFrozenApprovalPolicy(options.Store)
	if err != nil {
		return nil, err
	}
	// Approval is a first-party read-only decision stage, not a generic
	// Host policy callback. The original native approval return and the
	// frozen Harness owner are both required for later control.
	approval, err := agent.NewBoundedToolApproval(func(ctx context.Context, request tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
		return frozenApproval.requirement(ctx, request.RunID, request.Call.ToolKey, true)
	})
	if err != nil {
		return nil, err
	}
	dependencies.ApprovalPolicies = []plugin.ApprovalPolicy{approval}
	var shared *BudgetAuthority
	if options.Session.Config.SharedBudget != nil {
		meter := options.BudgetMeter
		if meter == nil {
			meter, _ = options.Model.(model.TokenAdmissionPlanner)
		}
		shared, err = NewBudgetAuthority(BudgetAuthorityDependencies{
			Store: options.Store, Relations: options.Relations, Meter: meter, Clock: systemClock{}, Runtime: options.Runtime,
			TurnFeed: options.TurnFeed,
		})
		if err != nil {
			return nil, err
		}
		adapter, adapterErr := shared.Adapter()
		if adapterErr != nil {
			return nil, adapterErr
		}
		dependencies.BoundedBudget = adapter
		dependencies.ModelOutcomeVerifier = shared
	}
	if delegation != nil {
		dependencies.ToolOutcomeVerifier = delegation
	}
	// One bounded native composition also serves reopened Turns. Only the
	// original frozen policy can request a summary, not this handle's defaults.
	dependencies.ContextSummary, err = NewContextSummaryAdapter(options.Store, options.Runtime, bounded, resolveWindow)
	if err != nil {
		return nil, err
	}
	loop, err := agent.NewRunner(dependencies)
	if err != nil {
		return nil, err
	}
	routes := options.Handoffs
	if routes == nil && options.DelegationPolicy != nil {
		// Native children use this exact Agent loop. RemoteChildren are
		// resolved only for explicitly frozen remote NamedAgent members.
		routes, err = handoff.New(handoff.Config{Local: loop, Remote: options.RemoteChildren})
		if err != nil {
			return nil, err
		}
	}
	host, err := NewRunner(Dependencies{
		Runtime: options.Runtime, Agent: loop, Store: options.Store, Context: manager, Catalog: registry,
		Handoffs: routes, Relations: options.Relations, DelegationPolicy: options.DelegationPolicy,
		Budget: shared, TurnFeed: options.TurnFeed,
	})
	if err != nil {
		return nil, err
	}
	if delegation != nil {
		if err := delegation.Bind(host); err != nil {
			return nil, err
		}
	}
	return host.OpenSession(options.Session)
}

// Root and private member Tools use one registration/policy boundary. Selection
// remains separate: registering a private handler never grants it to the root.
func normalizedSessionTool(selected SessionTool) (ToolPolicySnapshot, error) {
	key := selected.Registration.Definition.Key
	policy := selected.Policy
	if policy.Key == "" {
		policy.Key = key
	}
	policies, err := normalizeToolPolicies([]ToolPolicySnapshot{policy})
	if err != nil {
		return ToolPolicySnapshot{}, ErrInvalidRequest
	}
	policy = policies[0]
	validApproval := policy.ApprovalMode == approvalModeNever && policy.ApprovalCapability == "" ||
		policy.ApprovalMode == approvalModeAlways && policy.ApprovalCapability == approvalCapabilityPerCall
	if policy.Key != key || nonAssignableRuntimeTool(key) || !validApproval ||
		(policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") ||
		(policy.SideEffectLevel != "read" && policy.SideEffectLevel != "write") {
		return ToolPolicySnapshot{}, ErrInvalidRequest
	}
	return policy, nil
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
	// A reopened handle may provide new Tools for FUTURE Turns, but it cannot
	// run an old checkpoint with changed approval, side-effect, or version
	// semantics. This includes private member Tool grants, not only root Tools.
	// Read-only Session operations remain available for safe inspection.
	// Reconstruct the same pure Host execution-policy resolution used at
	// original Turn admission. A reopened handle cannot silently change a
	// physical Budget participant, its resolved ceilings or delegation depth.
	current, err := session.runner.resolveConfigExecutionPolicy(session.config)
	if err != nil {
		return err
	}
	if (config.SharedBudget == nil) != (current.SharedBudget == nil) ||
		(config.SharedBudget != nil && *config.SharedBudget != *current.SharedBudget) ||
		config.DelegationPolicy.MaxDepth != current.DelegationPolicy.MaxDepth {
		return ErrInvalidRequest
	}
	// Original snapshots are already normalized and verified by loadSealedConfig.
	currentNamed, namedErr := normalizeNamedAgentSnapshots(session.config.DelegationPolicy.NamedAgents)
	if namedErr != nil {
		return namedErr
	}
	// The paused Root always uses its original sealed AgentSpec. Current
	// handle instructions, Model and ceiling may describe future Turns, but
	// existing native Hosted Tool resolver/grants may not silently diverge:
	// the model might otherwise invoke a different provider-owned capability.
	currentPolicies, err := normalizeToolPolicies(current.ToolPolicies)
	if err != nil {
		return err
	}
	currentGrants, err := normalizeDelegatedLocalToolGrants(current)
	if err != nil {
		return err
	}
	currentHosted, err := agent.NormalizeHostedToolGrantsForRole(current.HostedToolGrants)
	if err != nil {
		return ErrInvalidRequest
	}
	if !slices.Equal(config.ToolKeys, normalizeStrings(current.ToolKeys)) ||
		!slices.Equal(config.HostedToolGrants, currentHosted) ||
		!slices.Equal(config.ToolPolicies, currentPolicies) ||
		!slices.Equal(config.DelegationPolicy.LocalToolGrants, currentGrants) ||
		!reflect.DeepEqual(config.DelegationPolicy.NamedAgents, currentNamed) {
		return ErrInvalidRequest
	}
	if _, err := session.runner.resolveConfigExecutionPolicy(config); err != nil {
		return err
	}
	delegation := session.runner.handoffs != nil && session.runner.relationReader != nil && session.runner.delegationPolicy != nil
	if slices.Contains(config.ToolKeys, DelegationToolKey) && !delegation {
		return ErrInvalidRequest
	}
	// Until the original Host authority rejects these contradictory facts at
	// admission, retain its pre-effect guard. A default depth grants no child
	// capability, and a presentation Item cannot select an execution route.
	items, err := listAllItems(ctx, store, turnID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.TurnID != turnID {
			return ErrConflict
		}
		if ((item.Kind == ItemDelegation || item.Kind == ItemSubtask) && !delegation) ||
			(item.Kind == ItemInteraction && session.runner.interactions == nil) || (item.Kind == ItemBudget && config.SharedBudget == nil) {
			return ErrInvalidRequest
		}
	}
	// Native continuation owns Run identity, output caps, recovery mode and
	// pending catalog fingerprints; Session does not reconstruct those proofs.
	return nil
}
