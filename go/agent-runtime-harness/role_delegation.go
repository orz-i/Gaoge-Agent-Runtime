package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func findRole(roles []RoleSnapshot, id string) (RoleSnapshot, bool) {
	for _, role := range roles {
		if role.ID == id {
			return role, true
		}
	}
	return RoleSnapshot{}, false
}

func toolsRoleUnavailable() error {
	return tools.NewRecoverableCallError("delegation.role_unavailable", "Choose a roleID from this task's frozen available roles.", ErrInvalidRequest)
}

func roleInstructions(role RoleSnapshot) string {
	parts := []string{strings.TrimSpace(role.Instructions)}
	for _, skill := range role.Skills {
		parts = append(parts, "## "+skill.Title+"\n"+skill.Markdown)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func roleToolKeys(role RoleSnapshot) []string {
	result := make([]string, 0, len(role.ToolKeys))
	for _, key := range role.ToolKeys {
		key = strings.TrimSpace(key)
		if key == "" || key == GroupChatHandoffToolKey {
			continue
		}
		result = append(result, key)
	}
	return normalizeStrings(result)
}

func intersectToolKeys(requested, allowed []string) []string {
	result := []string{}
	for _, key := range requested {
		if slices.Contains(allowed, key) {
			result = append(result, key)
		}
	}
	return normalizeStrings(result)
}

// Parent-issued Tool selection is a per-delegation *subset* of a frozen,
// currently authorized parent Run. Only provider-hosted and backend-local
// dispatches are eligible; runtime controls and client/browser dispatches
// cannot be silently delegated to a child session.
// Internal Harness/Agent orchestration is never a redistributable tool grant.
// It may be available to the parent but cannot be delegated as generic local
// dispatch: doing so would bypass context, depth and scope-specific controls.
func nonAssignableRuntimeTool(key string) bool {
	return strings.HasPrefix(key, "harness.") || key == agent.ToolDiscoveryKey ||
		strings.HasPrefix(key, "runtime.")
}

func assignableParentToolPolicies(config ConfigSnapshot, parent agent.View) []ToolPolicySnapshot {
	policies := make([]ToolPolicySnapshot, 0, len(config.ToolPolicies))
	for _, policy := range config.ToolPolicies {
		if !slices.Contains(parent.ToolKeys, policy.Key) || nonAssignableRuntimeTool(policy.Key) {
			continue
		}
		if policy.ExecutionMode != "local_dispatch" && policy.ExecutionMode != "provider_hosted" {
			continue
		}
		if policy.DefinitionVersion == "" {
			continue
		}
		policies = append(policies, policy)
	}
	return policies
}

func delegatedParentToolSelection(config ConfigSnapshot, parent agent.View, role RoleSnapshot, keys []string) ([]string, []agent.HostedToolGrant, error) {
	if len(keys) == 0 {
		return nil, nil, nil
	}
	// Parent-local execution capabilities cannot be grafted onto an A2A or
	// otherwise externally routed member. Those runners own their own
	// independently negotiated capabilities and credential boundaries.
	if role.MemberID != "" && role.MemberID != role.ID {
		return nil, nil, ErrInvalidRequest
	}
	policies := assignableParentToolPolicies(config, parent)
	local := []string{}
	hosted := []agent.HostedToolGrant{}
	effectiveChildModel := firstNonEmpty(role.Model, parent.Model)
	for _, key := range keys {
		matched := false
		for _, policy := range policies {
			if policy.Key != key {
				continue
			}
			matched = true
			if policy.ExecutionMode == "provider_hosted" {
				// Provider-native Tools are tied to the exact parent model, not
				// simply their key or vendor. A different child model must
				// receive its own independently validated Role grant.
				if effectiveChildModel != parent.Model || parent.Model == "" {
					return nil, nil, ErrInvalidRequest
				}
				hosted = append(hosted, agent.HostedToolGrant{Key: key, DefinitionVersion: policy.DefinitionVersion})
			} else {
				local = append(local, key)
			}
			break
		}
		if !matched {
			return nil, nil, ErrInvalidRequest
		}
	}
	return normalizeStrings(local), hosted, nil
}

func joinDelegatedHostedGrants(roleGrants, selectedGrants []agent.HostedToolGrant) ([]agent.HostedToolGrant, error) {
	versions := map[string]string{}
	for _, grant := range append(append([]agent.HostedToolGrant(nil), roleGrants...), selectedGrants...) {
		if existing, ok := versions[grant.Key]; ok && existing != grant.DefinitionVersion {
			return nil, ErrInvalidRequest
		}
		versions[grant.Key] = grant.DefinitionVersion
	}
	result := make([]agent.HostedToolGrant, 0, len(versions))
	for key, version := range versions {
		result = append(result, agent.HostedToolGrant{Key: key, DefinitionVersion: version})
	}
	return agent.NormalizeHostedToolGrantsForRole(result)
}

func roleAgentLimits(parent agent.Limits, role budget.Limits) agent.Limits {
	// Shared child/concurrency limits are enforced by the Turn ledger. The
	// Feature still receives its ordinary local call/token limits.
	role.MaxChildRuns, role.MaxConcurrentRuns, role.MaxCostUnits, role.MaxStateBytes = 0, 0, 0, 0
	parent.MaxChildRuns, parent.MaxConcurrentRuns, parent.MaxCostUnits, parent.MaxStateBytes = 0, 0, 0, 0
	role.MaxConcurrentDescendants, parent.MaxConcurrentDescendants = 0, 0
	resolved, _ := budget.TightenLimits(parent, role)
	return resolved
}

func frozenDelegationExecution(value handoff.Delegation) *handoff.Delegation {
	if value.RoleID == "" {
		return nil
	}
	value.Status, value.Result, value.ErrorCode, value.Error = handoff.StatusQueued, nil, "", ""
	return &value
}

func (runner *Runner) frozenDelegation(ctx context.Context, turnID, id string) (handoff.Delegation, bool, error) {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return handoff.Delegation{}, false, err
	}
	for _, item := range items {
		if item.Kind != ItemDelegation || (item.Status != ItemStarted && item.Status != ItemWaiting) {
			continue
		}
		var payload delegationItemPayload
		if err = json.Unmarshal(item.Payload, &payload); err != nil {
			return handoff.Delegation{}, false, err
		}
		if payload.DelegationID == id && payload.Execution != nil {
			return *payload.Execution, true, nil
		}
	}
	return handoff.Delegation{}, false, nil
}

func (runner *Runner) admitRoleChild(ctx context.Context, turn Turn, parent Invocation, delegation handoff.Delegation) error {
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return err
	}
	var limits budget.Limits
	if delegation.RoleID != "" {
		role, found := findRole(config.Roles, delegation.RoleID)
		if !found {
			return ErrConflict
		}
		limits = role.Limits
	}
	if runner.budgets == nil {
		return ErrInvalidRequest
	}
	coordinator := runner.budgets.coordinator()
	ledger, err := runner.budgets.bindRun(ctx, parent.ExecutionRefID)
	if err != nil {
		return err
	}
	// Recover terminal releases missed between the Runtime commit and the budget
	// commit. Waiting coordinators and merely cancelled requests still occupy
	// capacity until their Run has actually ended.
	var finished []string
	for id, binding := range ledger.Runs {
		if ledger.FinishedRuns[id] {
			continue
		}
		snapshot, loadErr := runner.runtime.Load(ctx, id)
		if errors.Is(loadErr, kernel.ErrNotFound) {
			// A registered child that was never created must not consume
			// descendant capacity forever after its owner has terminated.
			// Do not reclaim under a live/unknown parent: it may still be
			// preparing the child in another process.
			if binding.ParentRunID != "" {
				owner, ownerErr := runner.runtime.Load(ctx, binding.ParentRunID)
				if ownerErr == nil && terminalRuntimeStatus(owner.Run.Status) {
					// Cancellation prevents a late StartRun from spending budget;
					// reconciliation frees only lifetime capacity, not usage.
					if _, cancelErr := coordinator.CancelRun(ctx, turn.ID, id); cancelErr != nil {
						return cancelErr
					}
					finished = append(finished, id)
				} else if ownerErr != nil && !errors.Is(ownerErr, kernel.ErrNotFound) {
					return ownerErr
				}
			}
			continue
		}
		if loadErr != nil {
			return loadErr
		}
		if terminalRuntimeStatus(snapshot.Run.Status) {
			finished = append(finished, id)
		}
	}
	if len(finished) > 0 {
		if _, err = coordinator.FinishRuns(ctx, turn.ID, finished...); err != nil {
			return err
		}
	}
	// Admission happens before child topology facts are appended so a hard
	// descendant ceiling cannot leave a phantom relation or child Invocation.
	ledger, err = coordinator.RegisterRun(ctx, turn.ID, delegation.ChildRunID, budget.RunBudget{
		ParentRunID: parent.ExecutionRefID,
		Limits:      limits,
	})
	if err != nil {
		return err
	}
	return projectBudgetItem(ctx, runner.store, runner.turnFeed, runner.clock, ledger)
}

func (runner *Runner) prepareRoleChild(ctx context.Context, turn Turn, _ Invocation, delegation handoff.Delegation, parentItemID string) error {
	input, inputHash, err := marshalInvocationValue(delegation)
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	id, err := InvocationID(turn.ID, parentItemID, CapabilityAgent, delegation.ID)
	if err != nil {
		return err
	}
	invocation, _, err := runner.store.CreateInvocation(ctx, Invocation{
		ID: id, TurnID: turn.ID, ParentItemID: parentItemID, CapabilityKey: CapabilityAgent, DefinitionVersion: RuntimeCapabilityVersion,
		ExecutionClass: ExecutionAgent, Input: input, InputHash: inputHash, ExecutionRefID: delegation.ChildRunID,
		Status: InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []HostRef{}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return err
	}
	return runner.recordInvocationItem(ctx, invocation)
}

// NewRoleModelMiddleware exposes only role IDs frozen for the current Turn.
// Legacy stored memberID calls stay readable by the base Tool registration.
func NewRoleModelMiddleware(store Store) (plugin.ModelMiddleware, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return roleModelMiddleware{store: store}, nil
}

type roleModelMiddleware struct{ store Store }

func isRoleDelegationInvocation(invocation Invocation, runID string) bool {
	if invocation.ParentItemID == "" {
		return false
	}
	var delegation handoff.Delegation
	return json.Unmarshal(invocation.Input, &delegation) == nil && delegation.RoleID != "" && delegation.ChildRunID == runID
}

// Only advertise keys that are both frozen on this Turn and actually visible
// in the supervising model's resolved, current request. Provider-hosted names
// are provider/model-bound; their assigned child must run this same model.
func modelDelegationToolCandidates(request model.Request, config ConfigSnapshot) []string {
	visible := map[string]bool{}
	for _, tool := range request.Tools {
		visible[tool.Key] = true
	}
	for _, tool := range request.HostedTools {
		visible[tool.Key] = true
	}
	keys := []string{}
	for _, policy := range config.ToolPolicies {
		// Candidate discovery and actual delegation admission share the same
		// non-redistributable control exclusion. Never invite the model to
		// request a Tool that the frozen authorization layer will reject.
		if !visible[policy.Key] || nonAssignableRuntimeTool(policy.Key) || policy.DefinitionVersion == "" ||
			(policy.ExecutionMode != "local_dispatch" && policy.ExecutionMode != "provider_hosted") {
			continue
		}
		if policy.ExecutionMode == "provider_hosted" {
			anyCompatible := false
			for _, role := range config.Roles {
				if role.MemberID != "" && role.MemberID != role.ID {
					continue
				}
				if firstNonEmpty(role.Model, request.Model) == request.Model && request.Model != "" {
					anyCompatible = true
					break
				}
			}
			if !anyCompatible {
				continue
			}
		}
		keys = append(keys, policy.Key)
	}
	return normalizeStrings(keys)
}

func (roleModelMiddleware) Name() string { return "harness.roles" }

func (middleware roleModelMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	invocation, err := middleware.store.GetInvocationByExecutionRefID(ctx, request.RunID)
	if errors.Is(err, ErrNotFound) {
		return next(ctx, request, emit)
	}
	if err != nil {
		return model.Response{}, err
	}
	turn, err := middleware.store.GetTurn(ctx, invocation.TurnID)
	if err != nil {
		return model.Response{}, err
	}
	config, err := middleware.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil {
		return model.Response{}, err
	}
	if config.Roles == nil && config.SharedBudget == nil {
		return next(ctx, request, emit)
	}
	allowed, err := delegationAllowed(ctx, middleware.store, config.DelegationPolicy, invocation)
	if err != nil {
		return model.Response{}, err
	}
	request = model.CloneRequest(request)
	if isRoleDelegationInvocation(invocation, request.RunID) {
		instruction := "You are a delegated specialist. Execute only the delegated user goal. Use only the tools declared for this request. Never call delegate_to_specialist_agent unless it is explicitly available. Return your findings with source URLs and limitations to the parent; do not merely announce more work."
		if len(request.Messages) > 0 && request.Messages[0].Role == model.RoleSystem {
			request.Messages[0].Content += "\n\n" + instruction
		} else {
			request.Messages = append([]model.Message{{Role: model.RoleSystem, Content: instruction}}, request.Messages...)
		}
	}
	ids, descriptions := []string{}, []string{}
	for _, role := range config.Roles {
		ids = append(ids, role.ID)
		descriptions = append(descriptions, role.ID+": "+role.Name+" — "+role.Description)
	}
	assignable := modelDelegationToolCandidates(request, config)
	definitions := request.Tools[:0]
	for _, definition := range request.Tools {
		if definition.Key == DelegationToolKey {
			if len(ids) == 0 || !allowed {
				continue
			}
			definition.Description = "Delegate a focused subtask using one available role. If a child fails, take over and complete the user-facing answer with available evidence and explicit limitations. Available roles:\n" + strings.Join(descriptions, "\n")
			properties := map[string]any{
				"roleID": map[string]any{"type": "string", "enum": ids}, "goal": map[string]any{"type": "string", "minLength": 1, "maxLength": 200000},
			}
			if len(assignable) != 0 {
				properties["toolKeys"] = map[string]any{
					"type": "array", "maxItems": 16, "uniqueItems": true,
					"items":       map[string]any{"type": "string", "enum": assignable},
					"description": "OPTIONAL: select only these currently authorized parent tools for this child. Omitting this field grants no additional parent tools. A provider-hosted tool may be delegated ONLY if the child uses the exact parent model. A child using a different model requires its own role-hosted authorization.",
				}
			}
			definition.InputSchema, err = json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"roleID", "goal"}, "properties": properties})
			if err != nil {
				return model.Response{}, err
			}
		}
		definitions = append(definitions, definition)
	}
	request.Tools = definitions
	return next(ctx, request, emit)
}
