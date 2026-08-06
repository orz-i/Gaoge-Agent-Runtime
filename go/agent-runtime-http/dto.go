package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workbench"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

var errInvalidJSONBody = errors.New("invalid JSON request body")

type ThreadRequest struct {
	Kind string `json:"kind" binding:"required,max=64"`
	ID   string `json:"id" binding:"required,max=128"`
}

type TextInputRequest struct {
	Content string `json:"content" binding:"required,max=200000"`
}

type StartTextRunRequest struct {
	Thread      ThreadRequest    `json:"thread" binding:"required"`
	Input       TextInputRequest `json:"input" binding:"required"`
	ClientRunID string           `json:"clientRunID" binding:"omitempty,max=64"`
	Model       string           `json:"model" binding:"omitempty,max=128"`
}

type StartPlanRunRequest struct {
	Thread         ThreadRequest    `json:"thread" binding:"required"`
	Input          TextInputRequest `json:"input" binding:"required"`
	ClientRunID    string           `json:"clientRunID" binding:"omitempty,max=64"`
	Model          string           `json:"model" binding:"omitempty,max=128"`
	ApprovalPolicy string           `json:"approvalPolicy" binding:"omitempty,oneof=auto required"`
	MaxSteps       int              `json:"maxSteps" binding:"omitempty,min=1,max=32"`
}

type ResolvePlanApprovalRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Decision         string `json:"decision" binding:"required,oneof=approve reject"`
	Comment          string `json:"comment" binding:"omitempty,max=2000"`
}

type StartWorkflowRunRequest struct {
	Thread      ThreadRequest            `json:"thread" binding:"required"`
	Input       json.RawMessage          `json:"input" binding:"required"`
	ClientRunID string                   `json:"clientRunID" binding:"omitempty,max=64"`
	Goal        string                   `json:"goal" binding:"required,max=200000"`
	Definition  workflow.DefinitionDraft `json:"definition" binding:"required"`
}

type ResolveWorkflowWaitRequest struct {
	ExpectedRevision uint64          `json:"expectedRevision" binding:"required,min=1"`
	Response         json.RawMessage `json:"response" binding:"required"`
}

type TeamMemberRequest struct {
	ID       string   `json:"id" binding:"required,max=128"`
	Goal     string   `json:"goal" binding:"required,max=200000"`
	ToolKeys []string `json:"toolKeys" binding:"omitempty,max=128,dive,max=255"`
}

type TeamJoinRequest struct {
	Mode          handoff.JoinMode      `json:"mode" binding:"required,oneof=all any quorum"`
	Quorum        int                   `json:"quorum" binding:"omitempty,min=1,max=16"`
	FailurePolicy handoff.FailurePolicy `json:"failurePolicy" binding:"omitempty,oneof=collect fail_fast"`
}

type StartTeamRunRequest struct {
	Thread      ThreadRequest       `json:"thread" binding:"required"`
	Goal        string              `json:"goal" binding:"required,max=200000"`
	ClientRunID string              `json:"clientRunID" binding:"omitempty,max=64"`
	Mode        team.ExecutionMode  `json:"mode" binding:"required,oneof=sequential parallel"`
	Members     []TeamMemberRequest `json:"members" binding:"required,min=1,max=16,dive"`
	Join        TeamJoinRequest     `json:"join" binding:"required"`
}

type CancelRunRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Reason           string `json:"reason" binding:"omitempty,max=2000"`
}

type CancelRunResponse struct {
	Run kernel.Run `json:"run"`
}

type RunSnapshotResponse struct {
	Run        kernel.Run         `json:"run"`
	State      json.RawMessage    `json:"state"`
	Checkpoint *kernel.Checkpoint `json:"checkpoint,omitempty"`
	Result     *kernel.Result     `json:"result,omitempty"`
	Events     []kernel.Event     `json:"events"`
}

func snapshotResponse(snapshot kernel.Snapshot) RunSnapshotResponse {
	return RunSnapshotResponse{
		Run: snapshot.Run, State: append(json.RawMessage(nil), snapshot.State...),
		Checkpoint: snapshot.Checkpoint, Result: snapshot.Result,
		Events: append([]kernel.Event(nil), snapshot.Events...),
	}
}

func workbenchResponse(detail workbench.Detail) workbench.Detail { return detail }

func bindStrictJSON(context *gin.Context, target interface{}) error {
	if context == nil || context.Request == nil || context.Request.Body == nil {
		return errInvalidJSONBody
	}
	raw, err := io.ReadAll(context.Request.Body)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return errInvalidJSONBody
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errInvalidJSONBody
	}
	if decoder.More() {
		return errInvalidJSONBody
	}
	context.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return nil
}

func normalizeThread(request ThreadRequest) kernel.ThreadRef {
	return kernel.ThreadRef{Kind: strings.TrimSpace(request.Kind), ID: strings.TrimSpace(request.ID)}
}
