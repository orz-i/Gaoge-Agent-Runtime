package http

import (
	"encoding/json"
	"strings"

	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type CancelRunResponse struct {
	Canceled bool `json:"canceled"`
}

type CreateEvidenceRequest struct {
	Source    EvidenceSourceRequest    `json:"source" binding:"required"`
	Selection EvidenceSelectionRequest `json:"selection" binding:"required"`
}

type EvidenceSourceRequest struct {
	Kind       string               `json:"kind" binding:"required,oneof=output projection"`
	ID         string               `json:"id,omitempty" binding:"omitempty,max=128"`
	Version    int                  `json:"version,omitempty" binding:"omitempty,min=1"`
	Thread     *model.ThreadRef     `json:"thread,omitempty"`
	Projection *model.ProjectionRef `json:"projection,omitempty"`
}

type EvidenceSelectionRequest struct {
	Kind                                                 string `json:"kind" binding:"required,oneof=full text_range table_range"`
	Title                                                string `json:"title,omitempty" binding:"omitempty,max=255"`
	Start, End, RowStart, RowEnd, ColumnStart, ColumnEnd int
}

type threadRefDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type projectionRefDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type resourceRefDTO struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
}

func threadRefResponse(ref model.ThreadRef) threadRefDTO {
	return threadRefDTO{Kind: ref.Kind, ID: ref.ID}
}

func projectionRefResponse(ref model.ProjectionRef) projectionRefDTO {
	return projectionRefDTO{Kind: ref.Kind, ID: ref.ID}
}

func resourceRefResponse(ref model.ResourceRef) resourceRefDTO {
	return resourceRefDTO{Kind: ref.Kind, ID: ref.ID, Revision: ref.Revision}
}

func resourceRefsResponse(refs []model.ResourceRef) []resourceRefDTO {
	result := make([]resourceRefDTO, 0, len(refs))
	for _, ref := range refs {
		result = append(result, resourceRefResponse(ref))
	}
	return result
}

func textRunConfigResponse(item *runtime.TextRunConfigSummary) interface{} {
	if item == nil {
		return nil
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return map[string]interface{}{}
	}
	var result map[string]interface{}
	if json.Unmarshal(raw, &result) != nil {
		return map[string]interface{}{}
	}
	result["environmentRef"] = resourceRefResponse(item.EnvironmentRef)
	result["skillRefs"] = resourceRefsResponse(item.SkillRefs)
	result["unavailableSkillRefs"] = resourceRefsResponse(item.UnavailableSkillRefs)
	return result
}

func canonicalizeKnownRuntimeRefs(value interface{}) interface{} {
	switch typed := value.(type) {
	case []interface{}:
		result := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			result = append(result, canonicalizeKnownRuntimeRefs(item))
		}
		return result
	case map[string]interface{}:
		if ref, ok := canonicalRuntimeRefMap(typed); ok {
			return ref
		}
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			result[key] = canonicalizeKnownRuntimeRefs(item)
		}
		return result
	default:
		return value
	}
}

func canonicalRuntimeRefMap(value map[string]interface{}) (map[string]interface{}, bool) {
	kind, kindOK := value["Kind"].(string)
	id, idOK := value["ID"].(string)
	if !kindOK || !idOK {
		return nil, false
	}
	result := map[string]interface{}{"kind": kind, "id": id}
	if revision, ok := value["Revision"].(string); ok && strings.TrimSpace(revision) != "" {
		result["revision"] = revision
	}
	return result, true
}

func toRunResponse(run model.Run, threadIDs ...string) map[string]interface{} {
	threadID := run.Thread.ID
	if len(threadIDs) > 0 && strings.TrimSpace(threadIDs[0]) != "" {
		threadID = strings.TrimSpace(threadIDs[0])
	}
	result := map[string]interface{}{
		"schemaVersion": 1,
		"runtimeKind":   "text",
		"actor":         map[string]string{"tenantID": run.Actor.TenantID, "id": run.Actor.ActorID},
		valueThread:     map[string]string{valueKind72883EFB: run.Thread.Kind, "id": threadID},
		"runID":         run.RunID, "requestID": run.RequestID, "goal": run.Goal, valueStatus00E8FE8E: run.Status,
		"statusReason": run.StatusReason, "currentStepID": run.CurrentStepID, "currentPlanID": run.CurrentPlanID,
		"pendingInteractionID": run.PendingInteractionID, "lastEventSeq": run.LastEventSeq,
		"requestedModelName": run.RequestedModelName, "platformModelName": run.PlatformModelName,
		"modelVendor": run.ModelVendor, "modelIcon": run.ModelIcon, "upstreamModelName": run.UpstreamModelName,
		"inputTokens": run.InputTokens, "outputTokens": run.OutputTokens, "cacheReadTokens": run.CacheReadTokens,
		"cacheWriteTokens": run.CacheWriteTokens, "reasoningTokens": run.ReasoningTokens, "llmCallsCount": run.LLMCallsCount,
		"toolCallsCount": run.ToolCallsCount, valueErrorCode8B63C5B4: run.ErrorCode, valueErrorMessage: run.ErrorMessage,
		"billedCurrency": run.BilledCurrency, "billedNanousd": run.BilledNanousd,
		valueStartedAt: run.StartedAt, valueEndedAt: run.EndedAt, "createdAt": run.CreatedAt, valueUpdatedAt: run.UpdatedAt,
	}
	if strings.TrimSpace(run.AgentManifest.ID) != "" {
		result["agentManifestRef"] = resourceRefResponse(run.AgentManifest)
		result["agentName"] = run.AgentName
	}
	if strings.TrimSpace(run.RootRunID) != "" {
		result["rootRunID"] = run.RootRunID
	}
	if strings.TrimSpace(run.ParentRunID) != "" {
		result["parentRunID"] = run.ParentRunID
	}
	if strings.TrimSpace(run.HandoffID) != "" {
		result["handoffID"] = run.HandoffID
	}
	if run.Depth > 0 {
		result["depth"] = run.Depth
	}
	return result
}
