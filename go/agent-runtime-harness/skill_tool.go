package harness

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// SkillReadToolKey exposes only the original Host-selected Skill revisions.
// An Agent learns the manifest first and requests exact content as needed.
const SkillReadToolKey = "harness.read_skill"

const (
	skillReadDefaultRunes = 4000
	skillReadMaxRunes     = 8000
)

type skillReadTool struct {
	store     Store
	relations RunRelationSource
}

type skillReadInput struct {
	ID     string `json:"id"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// Skill requests are untrusted Tool arguments, not durable authority proofs.
// Accept a valid explicit zero offset (the zero-valued field may also be
// omitted), but never accept duplicates, case aliases or unknown fields.
func decodeSkillReadInput(raw json.RawMessage) (skillReadInput, bool) {
	parsed, ok := decodeUniqueAuthorityJSON(raw)
	fields, object := parsed.(map[string]any)
	if !ok || !object || len(fields) == 0 || len(fields) > 3 {
		return skillReadInput{}, false
	}
	for key, field := range fields {
		switch key {
		case "id":
			if _, ok := field.(string); !ok {
				return skillReadInput{}, false
			}
		case "offset", "limit":
			if _, ok := field.(json.Number); !ok {
				return skillReadInput{}, false
			}
		default:
			return skillReadInput{}, false
		}
	}
	var value skillReadInput
	if json.Unmarshal(raw, &value) != nil || value.Offset < 0 || value.Limit < 0 {
		return skillReadInput{}, false
	}
	return value, true
}

func (handler skillReadTool) Execute(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
	if handler.store == nil || request.RunID == "" || request.Call.ID == "" ||
		request.Call.ToolKey != SkillReadToolKey || !json.Valid(request.Call.Arguments) {
		return tools.ExecutionResult{}, tools.ErrInvalidCall
	}
	input, decoded := decodeSkillReadInput(request.Call.Arguments)
	if !decoded ||
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
	if owner.ExecutionRefID != request.RunID || !validInvocation(owner) {
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
	skills, err := authorizedSkillsForRun(ctx, handler.store, handler.relations, owner, config)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	for _, skill := range skills {
		if skill.ID != input.ID {
			continue
		}
		// Pagination is in Unicode code points; never truncate UTF-8 bytes.
		runes := []rune(skill.Markdown)
		if input.Offset > len(runes) {
			return tools.RecoverableNoEffectResult(request.Call.ID,
				tools.NewRecoverableCallError("skill.invalid_range", "Skill offset exceeds selected content", tools.ErrInvalidCall))
		}
		// The ordinary Context compactor archives oversized Tool results. Skill
		// access is a separate, narrower capability than general archive reads;
		// never require a hidden extra archive grant to see the requested page.
		maxBytes := runtimecontext.NormalizePolicy(config.ContextPolicy).MaxToolResultBytes
		encode := func(end int) (json.RawMessage, error) {
			return json.Marshal(struct {
				ID          string `json:"id"`
				Revision    uint64 `json:"revision"`
				ContentHash string `json:"contentHash"`
				Offset      int    `json:"offset"`
				NextOffset  int    `json:"nextOffset"`
				Done        bool   `json:"done"`
				Markdown    string `json:"markdown"`
			}{skill.ID, skill.Revision, skill.ContentHash, input.Offset, end, end == len(runes), string(runes[input.Offset:end])})
		}
		end := min(input.Offset+input.Limit, len(runes))
		encoded, err := encode(end)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		if len(encoded) > maxBytes {
			low, high := input.Offset, end
			for low < high {
				mid := low + (high-low+1)/2
				candidate, encodeErr := encode(mid)
				if encodeErr != nil {
					return tools.ExecutionResult{}, encodeErr
				}
				if len(candidate) <= maxBytes {
					low = mid
				} else {
					high = mid - 1
				}
			}
			if low == input.Offset && input.Offset < len(runes) {
				return tools.RecoverableNoEffectResult(request.Call.ID,
					tools.NewRecoverableCallError("skill.context_too_small", "Selected Skill page cannot fit the frozen Context Tool-result limit", tools.ErrInvalidCall))
			}
			encoded, err = encode(low)
			if err != nil {
				return tools.ExecutionResult{}, err
			}
		}
		return tools.ExecutionResult{Content: encoded, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"}}, nil
	}
	return tools.RecoverableNoEffectResult(request.Call.ID,
		tools.NewRecoverableCallError("skill.not_selected", "Skill was not selected for this Agent", tools.ErrInvalidCall))
}

// authorizedSkillsForRun resolves the original physical Agent -> Handoff
// parent -> frozen NamedAgent version. Unlike a display/manifest lookup, this
// check must reject a forged child with a matching Skill ID or a valid root
// Config but no original started Handoff ownership. No delegation is started,
// canceled or resumed during this read-only proof.
func authorizedSkillsForRun(ctx context.Context, store Store, relations RunRelationSource, owner Invocation, config ConfigSnapshot) ([]SkillSnapshot, error) {
	if owner.ParentItemID == "" {
		if !slices.Contains(config.ToolKeys, SkillReadToolKey) {
			return nil, ErrConflict
		}
		return config.Skills, nil
	}
	if relations == nil || !isHandoffDelegationInvocation(owner) {
		return nil, ErrConflict
	}
	selected, err := decodeFrozenHandoffDelegation(owner.Input)
	if err != nil || selected.ChildRunID != owner.ExecutionRefID ||
		!slices.Contains(selected.ToolKeys, SkillReadToolKey) {
		return nil, ErrConflict
	}
	named, found := namedAgentForMember(config, selected.MemberID, selected.MemberRevision)
	if !found || len(named.Skills) == 0 || selected.Model != named.Model ||
		!strings.Contains(selected.Instructions, strings.TrimSpace(selectedSkillManifest(named.Skills))) {
		return nil, ErrConflict
	}
	parent, err := store.GetItem(ctx, owner.ParentItemID)
	if err != nil {
		return nil, err
	}
	if parent.ID != owner.ParentItemID || parent.Kind != ItemDelegation || parent.Status != ItemStarted ||
		parent.RunID != owner.ExecutionRefID || parent.TurnID != owner.TurnID || parent.InvocationID == owner.ID ||
		parent.ParentItemID != "" || parent.Seq == 0 {
		return nil, ErrConflict
	}
	var source delegationItemPayload
	if !decodeTypedAuthorityJSON(parent.Payload, &source) || source.Execution == nil ||
		parent.ID != stableID("hid", owner.TurnID, selected.ID, string(ItemStarted)) ||
		source.ParentRunID == "" || source.DelegationID != selected.ID ||
		source.MemberID != selected.MemberID || source.MemberRevision != selected.MemberRevision ||
		source.ChildRunID != selected.ChildRunID || source.Status != "queued" ||
		!sameHandoffFrozenExecution(*source.Execution, selected) {
		return nil, ErrConflict
	}
	parentOwner, err := store.GetInvocationByExecutionRefID(ctx, source.ParentRunID)
	if err != nil {
		return nil, err
	}
	if !validInvocation(parentOwner) || parentOwner.TurnID != owner.TurnID ||
		parentOwner.ExecutionRefID != source.ParentRunID || parentOwner.ID != parent.InvocationID {
		return nil, ErrConflict
	}
	// Handoff Item and Invocation are Host-authorized facts; the immutable
	// RunRelation is the independent physical-child ancestry. Neither a
	// guessed RunID nor an unrelated same-ID Store may authorize a Skill read.
	link, err := relations.GetByChild(ctx, owner.ExecutionRefID)
	if err != nil {
		return nil, err
	}
	if link.ChildRunID != owner.ExecutionRefID || link.ParentRunID != source.ParentRunID ||
		link.Kind != runrelation.KindDelegation || link.OwnerNodeID != selected.ID {
		return nil, ErrConflict
	}
	return named.Skills, nil
}

func skillReadToolRegistration(store Store, relations RunRelationSource) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{
			Key: SkillReadToolKey, Name: "read_selected_skill",
			Description: "Read exact instructions from a selected Skill by its frozen ID, in bounded pages. Only use a Skill when its manifest is relevant to the task.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","minLength":1},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8000}}}`),
		}, Handler: skillReadTool{store: store, relations: relations},
	}
}

func skillReadToolPolicySnapshot() ToolPolicySnapshot {
	return ToolPolicySnapshot{
		Key: SkillReadToolKey, DefinitionVersion: harnessToolDefinitionVersion,
		ApprovalCapability: approvalCapabilityPerCall, ApprovalMode: approvalModeNever,
		RiskLevel: toolRiskLevelLow, SideEffectLevel: "read", IdempotencyMode: toolIdempotencyRequestKey,
	}
}
