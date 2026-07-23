package domain

import "time"

const (
	InteractionSubmitPlan     = "submit_plan"
	InteractionAskUser        = "ask_user"
	InteractionApproveTool    = "approve_tool"
	InteractionApproveToolSet = "approve_tool_set"
	InteractionApproveStep    = "approve_step"
	InteractionPending        = "pending"
	InteractionResolved       = "resolved"
	InteractionCancelled      = "cancelled"
	InteractionExpired        = "expired"
)

type Interaction struct {
	InteractionID, RunID, StepID, ToolCallID string
	Type, Status                             string
	RequestPayloadJSON, ResponseSchemaJSON   string
	ResponseJSON, ResolveRequestID           string
	ResumeFingerprint                        string
	RequestedAt                              time.Time
	ExpiresAt, ResolvedAt                    *time.Time
	ResolvedBy                               ActorRef
	CreatedAt, UpdatedAt                     time.Time
}

type ExpiredInteraction struct {
	InteractionID string
	RunID         string
	Actor         ActorRef
	Thread        ThreadRef
}
