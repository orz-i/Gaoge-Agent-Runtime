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

func intersectToolKeys(requested, allowed []string) []string {
	result := []string{}
	for _, key := range requested {
		if slices.Contains(allowed, key) {
			result = append(result, key)
		}
	}
	return normalizeStrings(result)
}

func roleAgentLimits(parent agent.Limits, role budget.Limits) agent.Limits {
	// Shared child/concurrency limits are enforced by the Turn ledger. The
	// Feature still receives its ordinary local call/token limits.
	role.MaxChildRuns, role.MaxConcurrentRuns, role.MaxCostUnits, role.MaxStateBytes = 0, 0, 0, 0
	parent.MaxChildRuns, parent.MaxConcurrentRuns, parent.MaxCostUnits, parent.MaxStateBytes = 0, 0, 0, 0
	resolved, _ := budget.ResolveLimits(parent, role)
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
		if item.Kind != ItemDelegation || item.Status != ItemStarted {
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

func (runner *Runner) prepareRoleChild(ctx context.Context, turn Turn, parent Invocation, delegation handoff.Delegation, parentItemID string) error {
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
	if err = runner.recordInvocationItem(ctx, invocation); err != nil {
		return err
	}
	config, err := runner.store.GetConfigSnapshot(ctx, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return err
	}
	role, found := findRole(config.Roles, delegation.RoleID)
	if !found {
		return ErrConflict
	}
	if runner.budgets == nil {
		return ErrInvalidRequest
	}
	coordinator := runner.budgets.coordinator()
	if _, err = runner.budgets.bindRun(ctx, parent.ExecutionRefID); err != nil {
		return err
	}
	// Parent registration is performed at the public Agent start boundary.
	ledger, err := coordinator.RegisterRun(ctx, turn.ID, delegation.ChildRunID, budget.RunBudget{ParentRunID: parent.ExecutionRefID, Limits: role.Limits})
	if err != nil {
		return err
	}
	return projectBudgetItem(ctx, runner.store, runner.turnFeed, runner.clock, ledger)
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
	request = model.CloneRequest(request)
	ids, descriptions := []string{}, []string{}
	for _, role := range config.Roles {
		ids = append(ids, role.ID)
		descriptions = append(descriptions, role.ID+": "+role.Name+" — "+role.Description)
	}
	definitions := request.Tools[:0]
	for _, definition := range request.Tools {
		if definition.Key == DelegationToolKey {
			if len(ids) == 0 {
				continue
			}
			definition.Description = "Delegate a focused subtask using one available role. Available roles:\n" + strings.Join(descriptions, "\n")
			definition.InputSchema, err = json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"roleID", "goal"}, "properties": map[string]any{
				"roleID": map[string]any{"type": "string", "enum": ids}, "goal": map[string]any{"type": "string", "minLength": 1, "maxLength": 200000},
			}})
			if err != nil {
				return model.Response{}, err
			}
		}
		definitions = append(definitions, definition)
	}
	request.Tools = definitions
	return next(ctx, request, emit)
}
