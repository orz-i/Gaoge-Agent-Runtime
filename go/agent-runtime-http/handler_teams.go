package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	agentTeamFieldHandoff  = "handoff"
	agentTeamFieldRootStep = "rootStep"
)

type StartAgentTeamRequest struct {
	Thread              RunThreadRequest          `json:"thread" binding:"required"`
	Input               TextRunRequestInput       `json:"input" binding:"required"`
	ClientTeamID        string                    `json:"clientTeamID" binding:"required,max=128"`
	CoordinatorManifest ResourceRefDTO            `json:"coordinatorManifest" binding:"required"`
	Model               string                    `json:"model" binding:"omitempty,max=128"`
	ExecutionMode       string                    `json:"executionMode" binding:"omitempty,oneof=auto direct plan"`
	Options             map[string]interface{}    `json:"options"`
	Workspace           *runtime.WorkspaceRequest `json:"workspace"`
	Members             []AgentTeamMemberRequest  `json:"members" binding:"required,min=1,max=16,dive"`
	Join                AgentTeamJoinRequest      `json:"join" binding:"required"`
}

type AgentTeamMemberRequest struct {
	MemberID      string                 `json:"memberID" binding:"required,max=64"`
	AgentManifest ResourceRefDTO         `json:"agentManifest" binding:"required"`
	Goal          string                 `json:"goal" binding:"required,max=20000"`
	ContentType   string                 `json:"contentType" binding:"omitempty,oneof=text markdown"`
	OutputIDs     []string               `json:"outputIDs" binding:"max=128,dive,max=64"`
	EvidenceIDs   []string               `json:"evidenceIDs" binding:"max=128,dive,max=64"`
	Options       map[string]interface{} `json:"options"`
}

type AgentTeamJoinRequest struct {
	Mode           string `json:"mode" binding:"omitempty,oneof=all any quorum"`
	Quorum         int    `json:"quorum" binding:"omitempty,min=1,max=16"`
	FailurePolicy  string `json:"failurePolicy" binding:"omitempty,oneof=collect fail_fast"`
	TimeoutSeconds int    `json:"timeoutSeconds" binding:"omitempty,min=60,max=604800"`
	TimeoutPolicy  string `json:"timeoutPolicy" binding:"omitempty,oneof=cancel_pending leave_running"`
}

func (h *Handler) StartAgentTeam(c *gin.Context) {
	var request StartAgentTeamRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeError(c, http.StatusBadRequest, "agent.team_invalid_request", "invalid agent team request")
		return
	}
	actor := h.actorRef(c)
	thread := threadRef(request.Thread.Kind, request.Thread.ID)
	snapshot, err := h.service.ResolveThread(c.Request.Context(), actor, thread)
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	result, err := h.service.StartAgentTeam(c.Request.Context(), runtime.StartAgentTeamInput{
		ClientTeamID: request.ClientTeamID,
		Coordinator: runtime.StartTextRunInput{
			Actor: actor, Thread: thread, RequestID: h.requestID(c), Goal: request.Input.Content, ContentType: request.Input.ContentType,
			Environment: snapshot.Environment, PlatformModelName: request.Model, ExecutionMode: request.ExecutionMode, Options: sanitizeRunOptions(request.Options),
			FileIDs: request.Input.FileIDs, OutputIDs: request.Input.OutputIDs, EvidenceIDs: request.Input.EvidenceIDs,
			ParentProjection: request.Thread.ParentProjection, SourceProjection: request.Thread.SourceProjection, BranchReason: request.Thread.BranchReason,
			HTMLVisualPromptEnabled: request.Input.HTMLVisualPrompt, HTMLVisualColorMode: request.Input.HTMLVisualColorMode,
			ThreadModel: snapshot.DefaultModel, ThreadProvider: snapshot.ModelProvider, ThreadScope: snapshot.BindingScope,
			Workspace: request.Workspace, AgentManifest: resourceRefDomain(request.CoordinatorManifest),
		},
		Members: agentTeamMembers(request.Members),
		Join: runtime.AgentTeamJoinInput{
			Mode: request.Join.Mode, Quorum: request.Join.Quorum, FailurePolicy: request.Join.FailurePolicy,
			TimeoutSeconds: request.Join.TimeoutSeconds, TimeoutPolicy: request.Join.TimeoutPolicy,
		},
	})
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, agentTeamStartResponse(*result, request.Thread.ID))
}

func agentTeamMembers(items []AgentTeamMemberRequest) []runtime.AgentTeamMemberInput {
	result := make([]runtime.AgentTeamMemberInput, 0, len(items))
	for _, item := range items {
		result = append(result, runtime.AgentTeamMemberInput{
			MemberID: item.MemberID, AgentManifest: resourceRefDomain(item.AgentManifest), Goal: item.Goal, ContentType: item.ContentType,
			OutputIDs: item.OutputIDs, EvidenceIDs: item.EvidenceIDs, Options: sanitizeRunOptions(item.Options),
		})
	}
	return result
}

func resourceRefDomain(item ResourceRefDTO) model.ResourceRef {
	return model.ResourceRef{Kind: item.Kind, ID: item.ID, Revision: item.Revision}
}

func agentTeamStartResponse(result runtime.AgentTeamStartResult, threadID string) map[string]interface{} {
	tasks := make([]map[string]interface{}, 0, len(result.Tasks))
	for _, item := range result.Tasks {
		tasks = append(tasks, map[string]interface{}{
			"memberID": item.MemberID, agentTeamFieldHandoff: runHandoffResponse(item.Handoff),
			valueRunA037153B: toRunResponse(item.Run, threadID), agentTeamFieldRootStep: toRunStepResponse(item.Step),
		})
	}
	return map[string]interface{}{
		"rootRun": toRunResponse(result.Root.Run, threadID), agentTeamFieldRootStep: toRunStepResponse(result.Root.Step),
		agentFieldInputProjectionRef: projectionRefResponse(result.Root.Projection.Input),
		"outputProjectionRef":        projectionRefResponse(result.Root.Projection.Output),
		"tasks":                      tasks, "join": runHandoffJoinResponse(result.Join),
	}
}
