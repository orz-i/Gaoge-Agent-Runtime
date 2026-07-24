package http

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	agentFieldRevision           = "revision"
	agentFieldStatus             = "status"
	agentFieldInputProjectionRef = "inputProjectionRef"
)

type AgentManifestRevisionRequest struct {
	ManifestID       string   `json:"manifestID" binding:"omitempty,max=64"`
	ExpectedRevision int      `json:"expectedRevision" binding:"min=0"`
	Name             string   `json:"name" binding:"required,max=128"`
	Description      string   `json:"description" binding:"omitempty,max=4000"`
	Instructions     string   `json:"instructions" binding:"omitempty,max=20000"`
	Status           string   `json:"status" binding:"omitempty,oneof=active disabled"`
	ModelName        string   `json:"modelName" binding:"omitempty,max=128"`
	ExecutionMode    string   `json:"executionMode" binding:"omitempty,oneof=auto direct plan"`
	ToolKeys         []string `json:"toolKeys" binding:"max=128,dive,max=255"`
	SkillKeys        []string `json:"skillKeys" binding:"max=128,dive,max=64"`
	MaxChildRuns     int      `json:"maxChildRuns" binding:"omitempty,min=1,max=16"`
	MaxDepth         int      `json:"maxDepth" binding:"omitempty,min=1,max=6"`
	RevisionNote     string   `json:"revisionNote" binding:"omitempty,max=255"`
}

type DelegateTextRunRequest struct {
	ClientHandoffID  string                 `json:"clientHandoffID" binding:"required,max=64"`
	AgentManifest    ResourceRefDTO         `json:"agentManifest" binding:"required"`
	Goal             string                 `json:"goal" binding:"required,max=20000"`
	ContentType      string                 `json:"contentType" binding:"omitempty,max=64"`
	OutputIDs        []string               `json:"outputIDs" binding:"max=128,dive,max=64"`
	EvidenceIDs      []string               `json:"evidenceIDs" binding:"max=128,dive,max=64"`
	Options          map[string]interface{} `json:"options"`
	HTMLVisualPrompt bool                   `json:"htmlVisualPrompt"`
	HTMLColorMode    string                 `json:"htmlVisualColorMode" binding:"omitempty,max=32"`
}

type ResourceRefDTO struct {
	Kind     string `json:"kind" binding:"required,max=64"`
	ID       string `json:"id" binding:"required,max=128"`
	Revision string `json:"revision" binding:"omitempty,max=64"`
}

func (h *Handler) ListAgentManifests(c *gin.Context) {
	h.listAgentManifests(c, model.AgentManifestStatusActive)
}

func (h *Handler) ListAdminAgentManifests(c *gin.Context) {
	h.listAgentManifests(c, strings.TrimSpace(c.Query("status")))
}

func (h *Handler) listAgentManifests(c *gin.Context, status string) {
	limit, offset := pageParams(c, 50)
	page, err := h.service.ListAgentManifests(c.Request.Context(), h.actorRef(c), model.AgentManifestFilter{Status: status, Limit: limit, Offset: offset})
	if err != nil {
		writeAgentError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(page.Results))
	for _, item := range page.Results {
		results = append(results, agentManifestResponse(item))
	}
	writePage(c, page.Total, results)
}

func (h *Handler) GetAgentManifest(c *gin.Context) {
	manifestID, err := stringParam(c, "manifest_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	item, err := h.service.GetAgentManifest(c.Request.Context(), h.actorRef(c), model.ResourceRef{Kind: model.AgentManifestKind, ID: manifestID, Revision: strings.TrimSpace(c.Query(agentFieldRevision))})
	if err != nil {
		writeAgentError(c, err)
		return
	}
	writeSuccess(c, agentManifestResponse(*item))
}

func (h *Handler) CreateAgentManifest(c *gin.Context) {
	var request AgentManifestRevisionRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeError(c, http.StatusBadRequest, "", "invalid agent manifest request")
		return
	}
	request.ExpectedRevision = 0
	h.createAgentManifestRevision(c, request)
}

func (h *Handler) ReviseAgentManifest(c *gin.Context) {
	manifestID, err := stringParam(c, "manifest_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	var request AgentManifestRevisionRequest
	if err = bindStrictJSON(c, &request); err != nil || request.ExpectedRevision <= 0 {
		writeError(c, http.StatusBadRequest, "", "invalid agent manifest revision request")
		return
	}
	request.ManifestID = manifestID
	h.createAgentManifestRevision(c, request)
}

func (h *Handler) createAgentManifestRevision(c *gin.Context, request AgentManifestRevisionRequest) {
	item, reused, err := h.service.CreateAgentManifestRevision(c.Request.Context(), runtime.AgentManifestRevisionInput{
		Actor: h.actorRef(c), ManifestID: request.ManifestID, ExpectedRevision: request.ExpectedRevision, Name: request.Name,
		Description: request.Description, Instructions: request.Instructions, Status: request.Status, ModelName: request.ModelName,
		ExecutionMode: request.ExecutionMode, ToolKeys: request.ToolKeys, SkillRefs: skillRefsFromKeys(request.SkillKeys),
		MaxChildRuns: request.MaxChildRuns, MaxDepth: request.MaxDepth, RequestID: h.requestID(c), RevisionNote: request.RevisionNote,
	})
	if err != nil {
		writeAgentError(c, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	c.JSON(status, agentManifestResponse(*item))
}

func (h *Handler) DelegateTextRun(c *gin.Context) {
	parentRunID, err := stringParam(c, "run_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	var request DelegateTextRunRequest
	if err = bindStrictJSON(c, &request); err != nil {
		writeError(c, http.StatusBadRequest, "", "invalid run handoff request")
		return
	}
	result, err := h.service.DelegateTextRun(c.Request.Context(), runtime.DelegateTextRunInput{
		Actor: h.actorRef(c), ParentRunID: parentRunID, ClientHandoffID: request.ClientHandoffID,
		AgentManifest: model.ResourceRef{Kind: request.AgentManifest.Kind, ID: request.AgentManifest.ID, Revision: request.AgentManifest.Revision},
		Goal:          request.Goal, ContentType: request.ContentType, OutputIDs: request.OutputIDs, EvidenceIDs: request.EvidenceIDs,
		Options: sanitizeRunOptions(request.Options), RequestID: h.requestID(c), HTMLVisualPrompt: request.HTMLVisualPrompt, HTMLColorMode: request.HTMLColorMode,
	})
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, map[string]interface{}{"handoff": runHandoffResponse(result.Handoff), valueRunA037153B: toRunResponse(result.Run), "rootStep": toRunStepResponse(result.Step)})
}

func (h *Handler) GetRunTaskTree(c *gin.Context) {
	runID, err := stringParam(c, "run_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	tree, err := h.service.GetRunTaskTree(c.Request.Context(), h.actorRef(c), runID)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	tasks := make([]map[string]interface{}, 0, len(tree.Tasks))
	for _, item := range tree.Tasks {
		tasks = append(tasks, map[string]interface{}{"handoff": runHandoffResponse(item.Handoff), valueRunA037153B: toRunResponse(item.Run)})
	}
	writeSuccess(c, map[string]interface{}{"rootRunID": tree.RootRunID, "currentRunID": tree.CurrentRunID, "rootRun": toRunResponse(tree.RootRun), "tasks": tasks})
}

func skillRefsFromKeys(keys []string) []model.ResourceRef {
	refs := make([]model.ResourceRef, 0, len(keys))
	for _, key := range keys {
		if value := strings.TrimSpace(key); value != "" {
			refs = append(refs, model.ResourceRef{Kind: runtime.ResourceKindSkill, ID: value})
		}
	}
	return refs
}

func agentManifestResponse(item model.AgentManifest) map[string]interface{} {
	skillKeys := make([]string, 0, len(item.SkillRefs))
	for _, ref := range item.SkillRefs {
		skillKeys = append(skillKeys, ref.ID)
	}
	return map[string]interface{}{
		"manifestID": item.ManifestID, agentFieldRevision: item.Revision, "ref": resourceRefResponse(item.Ref()), "name": item.Name,
		"description": item.Description, "instructions": item.Instructions, agentFieldStatus: item.Status, "modelName": item.ModelName,
		"executionMode": item.ExecutionMode, "toolKeys": item.ToolKeys, "skillKeys": skillKeys, "maxChildRuns": item.MaxChildRuns,
		"maxDepth": item.MaxDepth, "revisionNote": item.RevisionNote, valueCreatedAtE3B65D13: item.CreatedAt, valueUpdatedAt: item.UpdatedAt,
	}
}

func runHandoffResponse(item model.RunHandoff) map[string]interface{} {
	return map[string]interface{}{
		"handoffID": item.HandoffID, "clientHandoffID": item.ClientHandoffID, "rootRunID": item.RootRunID,
		"parentRunID": item.ParentRunID, "childRunID": item.ChildRunID, "agentManifest": resourceRefResponse(item.AgentManifest),
		"agentName": item.AgentName, "goal": item.Goal, "status": item.Status, "depth": item.Depth,
		agentFieldInputProjectionRef: projectionRefResponse(item.InputProjection), "resultSummary": item.ResultSummary,
		"resultOutputIDs": item.ResultOutputIDs, valueErrorCode8B63C5B4: item.ErrorCode, valueErrorMessage: item.ErrorMessage,
		valueCreatedAtE3B65D13: item.CreatedAt, valueUpdatedAt: item.UpdatedAt, "completedAt": item.CompletedAt,
	}
}

func pageParams(c *gin.Context, defaultLimit int) (int, int) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	offset, _ := strconv.Atoi(c.Query("offset"))
	if limit <= 0 {
		limit = defaultLimit
	}
	return limit, max(offset, 0)
}

func writeAgentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, runtime.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "agent.invalid_request", "invalid agent request")
	case errors.Is(err, runtime.ErrNotFound):
		writeError(c, http.StatusNotFound, "agent.not_found", "agent resource not found")
	case errors.Is(err, runtime.ErrAgentManifestConflict), errors.Is(err, runtime.ErrRunHandoffConflict):
		writeError(c, http.StatusConflict, "agent.request_conflict", err.Error())
	case errors.Is(err, runtime.ErrRunHandoffParentBlocked):
		writeError(c, http.StatusConflict, "agent.parent_run_blocked", "parent run cannot delegate in its current state")
	case errors.Is(err, runtime.ErrRunHandoffLimit):
		writeError(c, http.StatusConflict, "agent.child_limit_reached", "agent child run limit reached")
	case errors.Is(err, runtime.ErrRunHandoffDepth):
		writeError(c, http.StatusConflict, "agent.depth_limit_reached", "agent handoff depth limit reached")
	case errors.Is(err, runtime.ErrAgentManifestDisabled):
		writeError(c, http.StatusUnprocessableEntity, "agent.manifest_disabled", "agent manifest is disabled")
	default:
		writeTextRunError(c, err)
	}
}
