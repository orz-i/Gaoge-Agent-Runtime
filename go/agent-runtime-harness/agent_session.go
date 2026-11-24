package harness

import (
	"context"
	"errors"
	"reflect"
	"slices"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
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
	if err := selectNamedAgents(&options); err != nil {
		return nil, err
	}
	if len(config.ToolKeys) != 0 || len(config.ToolPolicies) != 0 {
		return nil, ErrInvalidRequest
	}
	delegated := options.Handoffs != nil || options.Relations != nil || options.DelegationPolicy != nil ||
		config.DelegationPolicy.MaxDepth != 0 || len(config.DelegationPolicy.LocalToolGrants) != 0
	if delegated {
		if options.Relations == nil || options.DelegationPolicy == nil ||
			config.DelegationPolicy.MaxDepth <= 0 ||
			(len(config.DelegationPolicy.LocalToolGrants) != 0 && len(options.MemberTools) == 0) {
			// Member-specific Tool grants need the advanced Host composition;
			// never expose a Child-only Tool to the root to make wiring easier.
			return nil, ErrInvalidRequest
		}
	}
	if len(options.MemberTools) != 0 && !delegated {
		return nil, ErrInvalidRequest
	}
	if options.ExploreReads && len(options.LocalTools) == 0 && !options.Context.ReadArtifacts {
		return nil, ErrInvalidRequest
	}
	registrations, exploration, err := selectSessionTools(config, options.LocalTools, options.Context.ReadArtifacts)
	if err != nil {
		return nil, err
	}
	for _, selected := range options.MemberTools {
		policy, policyErr := normalizedSessionTool(selected)
		if policyErr != nil || policy.ExecutionMode != "local_dispatch" {
			return nil, ErrInvalidRequest
		}
		config.ToolPolicies = append(config.ToolPolicies, policy)
		registrations = append(registrations, selected.Registration)
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
				if root[key] {
					continue
				}
				if !member[key] || nonAssignableRuntimeTool(key) {
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
	if config.SharedBudget != nil {
		if !enforceableHarnessBudgetLimits(*config.SharedBudget) || options.Context.ManagedRecovery ||
			options.Context.SummaryNativeRecovery || config.WorkingStateSummary != nil {
			return nil, ErrInvalidRequest
		}
		if _, ok := options.Store.(budget.LedgerStore); !ok {
			return nil, ErrInvalidRequest
		}
		if delegated && (options.Handoffs != nil || options.RemoteChildren != nil) {
			// External Agents cannot prove their Model/Tool calls join this ledger.
			return nil, ErrInvalidRequest
		}
	}
	if options.Context.ManagedRecovery || options.Context.SummaryNativeRecovery {
		host, ok := options.Store.(managedHostStore)
		if !ok || !options.Runtime.SupportsManagedRecovery() || !options.Runtime.UsesManagedRecoveryHost(host) {
			return nil, ErrInvalidRequest
		}
	}
	return openBoundedAgentSession(options, registrations, exploration)
}

// normalizedSessionTool is shared by root and member-only registrations. It
// never grants a Tool to an Agent; the frozen allowlist/grant is separate.
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

func selectSessionTools(config *ConfigSnapshot, selectedTools []SessionTool, readArtifacts bool) ([]tools.Registration, []string, error) {
	registrations := make([]tools.Registration, 0, len(selectedTools))
	var reads []string
	for _, selected := range selectedTools {
		policy, err := normalizedSessionTool(selected)
		if err != nil {
			return nil, nil, err
		}
		// Exploration may change read arguments to find new evidence, but
		// cannot classify a Tool requiring per-call Host approval as autonomous.
		if policy.SideEffectLevel == "read" && policy.ApprovalMode == approvalModeNever &&
			policy.ApprovalCapability == "" && !selected.Registration.Definition.Terminal {
			reads = append(reads, policy.Key)
		}
		registrations = append(registrations, selected.Registration)
		config.ToolKeys = append(config.ToolKeys, policy.Key)
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
	if len(options.Session.Config.Skills) != 0 || namedAgentHasSkills(options.Session.Config.DelegationPolicy.NamedAgents) {
		registrations = append(registrations, skillReadToolRegistration(options.Store, options.Relations))
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
		HostedTools: options.HostedTools, ToolDiscovery: options.ToolDiscovery,
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
	var shared *BudgetAuthority
	if options.Session.Config.SharedBudget != nil {
		meter := options.BudgetMeter
		if meter == nil {
			meter, _ = options.Model.(model.TokenAdmissionPlanner)
		}
		shared, err = NewBudgetAuthority(BudgetAuthorityDependencies{
			Store: options.Store, Relations: options.Relations, Meter: meter, Clock: systemClock{},
		})
		if err != nil {
			return nil, err
		}
		adapter, adapterErr := shared.Adapter()
		if adapterErr != nil {
			return nil, adapterErr
		}
		// Budget wraps Context: Context preparation must run in its own
		// original trusted Context ticket, not inside the Budget Run ticket.
		dependencies.RunMiddleware = append([]plugin.RunMiddleware{adapter}, dependencies.RunMiddleware...)
		dependencies.ModelMiddleware = []plugin.ModelMiddleware{adapter}
		dependencies.ToolMiddleware = []plugin.ToolMiddleware{adapter}
		// Keep the same Context preparer without a competing summary lifecycle.
		dependencies.PrepareModelRequest = bounded.Prepare
	}
	if delegation != nil {
		if shared != nil {
			dependencies.ToolOutcomeVerifier, err = shared.VerifiedToolOutcomeVerifier(delegation)
			if err != nil {
				return nil, err
			}
		} else {
			dependencies.ToolOutcomeVerifier = delegation
		}
	}
	if options.Context.ManagedRecovery {
		dependencies.ManagedContextRecovery = bounded
	} else if shared == nil {
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
	routes := options.Handoffs
	if routes == nil && options.DelegationPolicy != nil {
		// Native children use this exact Agent loop. RemoteChildren are
		// resolved only for explicitly frozen remote NamedAgent members.
		if options.RemoteChildren != nil {
			routes, err = handoff.NewAgentWithRemote(loop, options.RemoteChildren)
		} else {
			routes, err = handoff.New(loop)
		}
		if err != nil {
			return nil, err
		}
	}
	host, err := NewRunner(Dependencies{
		Runtime: options.Runtime, Agent: loop, Store: options.Store, Context: manager, Catalog: registry,
		Handoffs: routes, Relations: options.Relations, DelegationPolicy: options.DelegationPolicy,
		Budget: shared,
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
		agent.ValidateWorkingStateSummaryPolicy(config.WorkingStateSummary) != nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	var err error
	config.AgentSpec, err = normalizeAgentSpec(config.AgentSpec)
	if err != nil || (len(config.HostedToolGrants) != 0 && config.Model == "") {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	config.WorkingStateSummary = agent.CloneWorkingStateSummaryPolicy(config.WorkingStateSummary)
	var namedErr error
	config.DelegationPolicy.NamedAgents, namedErr = normalizeNamedAgentSnapshots(config.DelegationPolicy.NamedAgents)
	if namedErr != nil || (len(config.DelegationPolicy.NamedAgents) != 0 && config.DelegationPolicy.MaxDepth <= 0) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	policies, err := normalizeToolPolicies(config.ToolPolicies)
	if err != nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	config.ToolPolicies = policies
	grants, err := normalizeDelegatedLocalToolGrants(config)
	if err != nil || (len(grants) != 0 && config.DelegationPolicy.MaxDepth <= 0) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	config.DelegationPolicy.LocalToolGrants = grants
	memberOnly := make(map[string]bool, len(grants))
	for _, grant := range grants {
		memberOnly[grant.ToolKey] = true
	}
	if namedAgentHasSkills(config.DelegationPolicy.NamedAgents) && len(config.Skills) == 0 {
		memberOnly[SkillReadToolKey] = true
	}
	selected := make(map[string]bool, len(config.ToolKeys))
	for _, key := range config.ToolKeys {
		selected[key] = true
	}
	// Every callable or member-only grant must have exactly one immutable
	// Tool policy. A partial policy set cannot defer approval/dispatch checks
	// until after a Model has already selected the ungoverned Tool.
	if len(policies) != len(selected)+len(memberOnly) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	for _, named := range config.DelegationPolicy.NamedAgents {
		for _, key := range named.ToolKeys {
			if nonAssignableRuntimeTool(key) {
				return ConfigSnapshot{}, ErrInvalidRequest
			}
			if selected[key] {
				continue
			}
			found := false
			for _, grant := range grants {
				if grant.MemberID == named.MemberID && grant.MemberRevision == named.MemberRevision && grant.ToolKey == key {
					found = true
					break
				}
			}
			if !found {
				return ConfigSnapshot{}, ErrInvalidRequest
			}
		}
	}
	for _, policy := range policies {
		if (!selected[policy.Key] && !memberOnly[policy.Key]) ||
			(policy.ExecutionMode != "" && policy.ExecutionMode != "local_dispatch") {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if _, err := frozenApprovalRequirement(config, policy.Key); err != nil ||
			(policy.ApprovalMode == approvalModeNever && policy.ApprovalCapability != "" && policy.Key != ContextArtifactToolKey && policy.Key != DelegationToolKey && policy.Key != SkillReadToolKey) {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if policy.Key == DelegationToolKey && (policy != DelegationToolPolicySnapshot() || config.DelegationPolicy.MaxDepth <= 0) {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if policy.Key == ContextArtifactToolKey && policy != ContextArtifactToolPolicySnapshot() {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if policy.Key == SkillReadToolKey && (policy != skillReadToolPolicySnapshot() ||
			(len(config.Skills) == 0 && !namedAgentHasSkills(config.DelegationPolicy.NamedAgents))) {
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
	// Persisted JSON omits optional empty ToolKey lists. Compare the two
	// independently normalized immutable Named Agent snapshots, never their
	// raw nil/empty slices or a mutable current Host selection.
	originalNamed, namedErr := normalizeNamedAgentSnapshots(config.DelegationPolicy.NamedAgents)
	if namedErr != nil {
		return namedErr
	}
	currentNamed, namedErr := normalizeNamedAgentSnapshots(session.config.DelegationPolicy.NamedAgents)
	if namedErr != nil {
		return namedErr
	}
	// The paused Root always uses its original sealed AgentSpec. Current
	// handle instructions, Model and ceiling may describe future Turns, but
	// existing native Hosted Tool resolver/grants may not silently diverge:
	// the model might otherwise invoke a different provider-owned capability.
	if !slices.Equal(config.ToolKeys, session.config.ToolKeys) ||
		!slices.Equal(config.HostedToolGrants, current.HostedToolGrants) ||
		!slices.Equal(config.ToolPolicies, session.config.ToolPolicies) ||
		!slices.Equal(config.DelegationPolicy.LocalToolGrants, session.config.DelegationPolicy.LocalToolGrants) ||
		!reflect.DeepEqual(originalNamed, currentNamed) {
		return ErrInvalidRequest
	}
	if config.SharedBudget != nil && (session.runner.budgets == nil ||
		config.WorkingStateSummary != nil ||
		session.runner.agent.(*agent.Runner).SupportsManagedContextRecovery() ||
		session.runner.agent.(*agent.Runner).SupportsSummaryNativeRecovery()) {
		return ErrInvalidRequest
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
	toolKeys := append([]string(nil), config.ToolKeys...)
	for _, grant := range config.DelegationPolicy.LocalToolGrants {
		toolKeys = append(toolKeys, grant.ToolKey)
	}
	if _, err = session.runner.catalog.List(normalizeStrings(toolKeys)); err != nil {
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
			item.Kind == ItemInteraction || (item.Kind == ItemBudget && config.SharedBudget == nil) {
			return ErrInvalidRequest
		}
	}
	return nil
}
