package http

import (
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type CancelRunResponse struct {
	Canceled bool `json:"canceled"`
}

type StartTextRunResponseDoc struct {
	ErrorMsg string      `json:"errorMsg"`
	Data     interface{} `json:"data"`
}

type EvidenceResponseDoc struct {
	ErrorMsg string      `json:"errorMsg"`
	Data     interface{} `json:"data"`
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

type ErrorDoc struct {
	ErrorMsg  string      `json:"errorMsg"`
	ErrorCode string      `json:"errorCode,omitempty"`
	Details   interface{} `json:"details,omitempty"`
	RequestID string      `json:"requestId,omitempty"`
	Data      interface{} `json:"data"`
}

func toRunResponse(run model.Run, threadIDs ...string) map[string]interface{} {
	threadID := run.Thread.ID
	if len(threadIDs) > 0 && strings.TrimSpace(threadIDs[0]) != "" {
		threadID = strings.TrimSpace(threadIDs[0])
	}
	return map[string]interface{}{
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
}
