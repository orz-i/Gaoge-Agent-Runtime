package harness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// SkillReadToolKey exposes only the original Host-selected Skill revisions.
// An Agent learns the manifest first and requests exact content as needed.
const SkillReadToolKey = "harness.read_skill"

const (
	skillReadDefaultRunes = 4000
	skillReadMaxRunes     = 8000
)

type skillReadTool struct{ store Store }

type skillReadInput struct {
	ID     string `json:"id"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (handler skillReadTool) Execute(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	if handler.store == nil || request.RunID == "" || request.Call.ID == "" ||
		request.Call.ToolKey != SkillReadToolKey || !json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	var input skillReadInput
	if !decodeTypedAuthorityJSON(request.Call.Arguments, &input) ||
		strings.TrimSpace(input.ID) == "" || input.ID != strings.TrimSpace(input.ID) ||
		input.Offset < 0 || input.Limit < 0 || input.Limit > skillReadMaxRunes {
		return tools.RecoverableNoEffectResult(request.Call.ID,
			tools.NewRecoverableCallError("skill.invalid_read", "Invalid selected Skill read request", tools.ErrInvalidCall))
	}
	if input.Limit == 0 {
		input.Limit = skillReadDefaultRunes
	}
	owner, err := handler.store.GetInvocationByExecutionRefID(ctx, request.RunID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if owner.ExecutionRefID != request.RunID || owner.ParentItemID != "" || !validInvocation(owner) {
		return tools.ExecutionResult{}, ErrConflict
	}
	turn, err := loadVerifiedTurn(ctx, handler.store, owner.TurnID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	config, err := loadSealedConfig(ctx, handler.store, turn.ConfigSnapshotID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if !slices.Contains(config.ToolKeys, SkillReadToolKey) {
		return tools.ExecutionResult{}, ErrConflict
	}
	for _, skill := range config.Skills {
		if skill.ID != input.ID {
			continue
		}
		// Pagination is in Unicode code points; never truncate UTF-8 bytes.
		runes := []rune(skill.Markdown)
		if input.Offset > len(runes) {
			return tools.RecoverableNoEffectResult(request.Call.ID,
				tools.NewRecoverableCallError("skill.invalid_range", "Skill offset exceeds selected content", tools.ErrInvalidCall))
		}
		end := min(input.Offset+input.Limit, len(runes))
		encoded, err := json.Marshal(struct {
			ID          string `json:"id"`
			Revision    uint64 `json:"revision"`
			ContentHash string `json:"contentHash"`
			Offset      int    `json:"offset"`
			NextOffset  int    `json:"nextOffset"`
			Done        bool   `json:"done"`
			Markdown    string `json:"markdown"`
		}{skill.ID, skill.Revision, skill.ContentHash, input.Offset, end, end == len(runes), string(runes[input.Offset:end])})
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		return tools.ExecutionResult{Content: encoded, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"}}, nil
	}
	return tools.RecoverableNoEffectResult(request.Call.ID,
		tools.NewRecoverableCallError("skill.not_selected", "Skill was not selected for this Agent", tools.ErrInvalidCall))
}

func skillReadToolRegistration(store Store) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key: SkillReadToolKey, Name: "read_selected_skill",
			Description: "Read exact instructions from a selected Skill by its frozen ID, in bounded pages. Only use a Skill when its manifest is relevant to the task.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8000}}}`),
		}, Handler: skillReadTool{store: store},
	}
}

func skillReadToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: SkillReadToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "read", IdempotencyMode: toolIdempotencyRequestKey,
	}
}
