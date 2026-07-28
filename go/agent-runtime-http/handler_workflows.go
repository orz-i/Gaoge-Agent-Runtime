package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type StartWorkflowRequest struct {
	Thread      RunThreadRequest          `json:"thread" binding:"required"`
	Definition  ResourceRefDTO            `json:"definition" binding:"required"`
	Input       json.RawMessage           `json:"input" binding:"required"`
	ClientRunID string                    `json:"clientRunID" binding:"omitempty,max=64"`
	Limits      *model.WorkflowLimits     `json:"limits,omitempty"`
	CacheMode   string                    `json:"cacheMode" binding:"omitempty,oneof=use refresh bypass"`
	Workspace   *runtime.WorkspaceRequest `json:"workspace,omitempty"`
}

type WorkflowDefinitionRevisionRequest struct {
	WorkflowID       string               `json:"workflowID" binding:"omitempty,max=64"`
	ExpectedRevision int                  `json:"expectedRevision" binding:"min=0"`
	SchemaVersion    int                  `json:"schemaVersion" binding:"omitempty,oneof=1"`
	Scope            string               `json:"scope" binding:"omitempty,oneof=actor tenant system"`
	TenantID         string               `json:"tenantID" binding:"omitempty,max=64"`
	OwnerActorID     string               `json:"ownerActorID" binding:"omitempty,max=64"`
	Name             string               `json:"name" binding:"required,max=128"`
	Description      string               `json:"description" binding:"omitempty,max=4000"`
	Status           string               `json:"status" binding:"omitempty,oneof=active disabled"`
	InputSchema      json.RawMessage      `json:"inputSchema" binding:"required"`
	OutputSchema     json.RawMessage      `json:"outputSchema" binding:"required"`
	Root             model.WorkflowNode   `json:"root" binding:"required"`
	Limits           model.WorkflowLimits `json:"limits" binding:"required"`
	RevisionNote     string               `json:"revisionNote" binding:"omitempty,max=255"`
}

func (h *Handler) StartWorkflow(c *gin.Context) {
	var request StartWorkflowRequest
	if err := bindStrictJSON(c, &request); err != nil || len(request.Input) == 0 {
		invalidBody(c, firstWorkflowRequestError(err))
		return
	}
	if request.Definition.Kind != model.WorkflowDefinitionKind {
		writeError(c, http.StatusBadRequest, "workflow.invalid_definition_ref", "definition must reference a workflow definition")
		return
	}
	actor := h.actorRef(c)
	thread := threadRef(request.Thread.Kind, request.Thread.ID)
	snapshot, err := h.service.ResolveThread(c.Request.Context(), actor, thread)
	if err != nil {
		writeError(c, http.StatusNotFound, "workflow.thread_not_found", "thread not found")
		return
	}
	result, err := h.service.StartWorkflow(c.Request.Context(), runtime.StartWorkflowInput{
		Actor: actor, Thread: thread, RequestID: h.requestID(c), ClientRunID: request.ClientRunID,
		Definition: model.ResourceRef{Kind: request.Definition.Kind, ID: request.Definition.ID, Revision: request.Definition.Revision},
		Input:      request.Input, Environment: snapshot.Environment, Limits: request.Limits, CacheMode: request.CacheMode,
		ParentProjection: request.Thread.ParentProjection, SourceProjection: request.Thread.SourceProjection, BranchReason: request.Thread.BranchReason,
		ThreadModel: snapshot.DefaultModel, ThreadProvider: snapshot.ModelProvider, ThreadScope: snapshot.BindingScope, Workspace: request.Workspace,
	})
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, map[string]interface{}{
		"run": toRunResponse(result.Run, request.Thread.ID), "rootStep": toRunStepResponse(result.Step),
		"inputProjectionRef": projectionRefResponse(result.Projection.Input), "outputProjectionRef": projectionRefResponse(result.Projection.Output),
	})
}

func (h *Handler) GetRunResult(c *gin.Context) {
	runID, err := stringParam(c, "run_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	result, err := h.service.GetRunResult(c.Request.Context(), h.actorRef(c), runID)
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	writeSuccess(c, runResultResponse(*result))
}

func (h *Handler) ListWorkflowDefinitions(c *gin.Context) {
	h.listWorkflowDefinitions(c, false)
}

func (h *Handler) ListAdminWorkflowDefinitions(c *gin.Context) {
	h.listWorkflowDefinitions(c, true)
}

func (h *Handler) listWorkflowDefinitions(c *gin.Context, admin bool) {
	limit, offset := pageParams(c, 50)
	status := strings.TrimSpace(c.Query("status"))
	if !admin {
		status = model.WorkflowDefinitionStatusActive
	}
	page, err := h.service.ListWorkflowDefinitions(c.Request.Context(), h.actorRef(c), model.WorkflowDefinitionFilter{
		Status: status, Scope: strings.TrimSpace(c.Query("scope")), TenantID: strings.TrimSpace(c.Query("tenantID")),
		OwnerActorID: strings.TrimSpace(c.Query("ownerActorID")), Admin: admin, Limit: limit, Offset: offset,
	})
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(page.Results))
	for _, item := range page.Results {
		results = append(results, workflowDefinitionResponse(item))
	}
	writePage(c, page.Total, results)
}

func (h *Handler) GetWorkflowDefinition(c *gin.Context) {
	workflowID, err := stringParam(c, "workflow_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	item, err := h.service.GetWorkflowDefinition(c.Request.Context(), h.actorRef(c), model.ResourceRef{
		Kind: model.WorkflowDefinitionKind, ID: workflowID, Revision: strings.TrimSpace(c.Query("revision")),
	})
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	writeSuccess(c, workflowDefinitionResponse(*item))
}

func (h *Handler) ValidateWorkflowDefinition(c *gin.Context) {
	request, ok := h.bindWorkflowDefinitionRequest(c)
	if !ok {
		return
	}
	validation, err := h.service.ValidateWorkflowDefinition(c.Request.Context(), workflowDefinitionInput(h.actorRef(c), h.requestID(c), request))
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	writeSuccess(c, map[string]interface{}{"valid": true, "nodeCount": validation.NodeCount, "definition": workflowDefinitionResponse(validation.Definition)})
}

func (h *Handler) CreateWorkflowDefinition(c *gin.Context) {
	request, ok := h.bindWorkflowDefinitionRequest(c)
	if !ok {
		return
	}
	request.ExpectedRevision = 0
	h.createWorkflowDefinition(c, request)
}

func (h *Handler) ReviseWorkflowDefinition(c *gin.Context) {
	workflowID, err := stringParam(c, "workflow_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", err.Error())
		return
	}
	request, ok := h.bindWorkflowDefinitionRequest(c)
	if !ok {
		return
	}
	if request.ExpectedRevision <= 0 {
		writeError(c, http.StatusBadRequest, "workflow.expected_revision_required", "expectedRevision must identify the current revision")
		return
	}
	request.WorkflowID = workflowID
	h.createWorkflowDefinition(c, request)
}

func (h *Handler) bindWorkflowDefinitionRequest(c *gin.Context) (WorkflowDefinitionRevisionRequest, bool) {
	var request WorkflowDefinitionRevisionRequest
	if err := bindStrictJSON(c, &request); err != nil || len(request.InputSchema) == 0 || len(request.OutputSchema) == 0 {
		invalidBody(c, firstWorkflowRequestError(err))
		return WorkflowDefinitionRevisionRequest{}, false
	}
	return request, true
}

func (h *Handler) createWorkflowDefinition(c *gin.Context, request WorkflowDefinitionRevisionRequest) {
	item, reused, err := h.service.CreateWorkflowDefinition(c.Request.Context(), workflowDefinitionInput(h.actorRef(c), h.requestID(c), request))
	if err != nil {
		writeWorkflowError(c, err)
		return
	}
	status := http.StatusCreated
	if reused {
		status = http.StatusOK
	}
	c.JSON(status, workflowDefinitionResponse(*item))
}

func workflowDefinitionInput(actor model.ActorRef, requestID string, request WorkflowDefinitionRevisionRequest) runtime.WorkflowDefinitionRevisionInput {
	return runtime.WorkflowDefinitionRevisionInput{
		Actor: actor, WorkflowID: request.WorkflowID, ExpectedRevision: request.ExpectedRevision, SchemaVersion: request.SchemaVersion,
		Scope: request.Scope, TenantID: request.TenantID, OwnerActorID: request.OwnerActorID, Name: request.Name,
		Description: request.Description, Status: request.Status, InputSchema: request.InputSchema, OutputSchema: request.OutputSchema,
		Root: request.Root, Limits: request.Limits, RequestID: requestID, RevisionNote: request.RevisionNote,
	}
}

func workflowDefinitionResponse(item model.WorkflowDefinition) map[string]interface{} {
	dependencies := make([]map[string]interface{}, 0, len(item.Dependencies))
	for _, dependency := range item.Dependencies {
		dependencies = append(dependencies, map[string]interface{}{
			"kind": dependency.Kind, "ref": resourceRefResponse(dependency.Ref), "toolKey": dependency.ToolKey,
			"definitionVersion": dependency.DefinitionVersion, "fingerprint": dependency.Fingerprint, "sideEffectLevel": dependency.SideEffectLevel,
		})
	}
	return map[string]interface{}{
		"workflowID": item.WorkflowID, "revision": item.Revision, "ref": resourceRefResponse(item.Ref()), "schemaVersion": item.SchemaVersion,
		"scope": item.Scope, "tenantID": item.TenantID, "ownerActorID": item.OwnerActorID, "name": item.Name,
		"description": item.Description, "status": item.Status, "inputSchema": item.InputSchema, "outputSchema": item.OutputSchema,
		"root": workflowSerializableValue(item.Root), "limits": item.Limits, "dependencies": dependencies,
		"dependencyHash": item.DependencyHash, "definitionHash": item.DefinitionHash, "revisionNote": item.RevisionNote,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
	}
}

func workflowExecutionResponse(item model.WorkflowExecution) map[string]interface{} {
	return map[string]interface{}{
		"runID": item.RunID, "definitionRef": resourceRefResponse(model.ResourceRef{Kind: model.WorkflowDefinitionKind, ID: item.WorkflowID, Revision: strconv.Itoa(item.WorkflowRevision)}), "definitionHash": item.DefinitionHash,
		"dependencyHash": item.DependencyHash, "budgetOwnerRunID": item.BudgetOwnerRunID, "rootRunID": item.RootRunID,
		"parentRunID": item.ParentRunID, "depth": item.Depth, "version": item.Version, "status": item.Status,
		"state": rawWorkflowJSON(item.StateJSON), "vars": rawWorkflowJSON(item.VarsJSON), "waits": rawWorkflowJSON(item.WaitsJSON),
		"compensations": rawWorkflowJSON(item.CompensationJSON), "budget": rawWorkflowJSON(item.BudgetJSON),
		"threadSnapshotHash": item.ThreadSnapshotHash, "completionSeq": item.CompletionSeq,
		"errorCode": item.ErrorCode, "errorMessage": item.ErrorMessage, "startedAt": item.StartedAt,
		"endedAt": item.EndedAt, "createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
	}
}

func runResultResponse(item model.RunResult) map[string]interface{} {
	return map[string]interface{}{
		"runID": item.RunID, "runtimeKind": model.NormalizeRuntimeKind(item.RuntimeKind), "value": json.RawMessage(item.CanonicalJSON),
		"presentation": item.Presentation, "schemaHash": item.SchemaHash, "contentHash": item.ContentHash,
		"createdAt": item.CreatedAt, "updatedAt": item.UpdatedAt,
	}
}

func workflowSerializableValue(value interface{}) interface{} {
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]interface{}{}
	}
	var decoded interface{}
	if json.Unmarshal(raw, &decoded) != nil {
		return map[string]interface{}{}
	}
	return canonicalizeKnownRuntimeRefs(decoded)
}

func rawWorkflowJSON(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return json.RawMessage(value)
}

func firstWorkflowRequestError(err error) error {
	if err != nil {
		return err
	}
	return errors.New("required workflow JSON value is missing")
}

func writeWorkflowError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, runtime.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "workflow.invalid_request", "invalid workflow request")
	case errors.Is(err, runtime.ErrNotFound):
		writeError(c, http.StatusNotFound, "workflow.not_found", "workflow resource not found")
	case errors.Is(err, runtime.ErrWorkflowDefinitionConflict), errors.Is(err, runtime.ErrWorkflowVersionConflict), errors.Is(err, runtime.ErrWorkflowResultConflict):
		writeError(c, http.StatusConflict, "workflow.state_conflict", err.Error())
	case errors.Is(err, runtime.ErrWorkflowDefinitionDisabled):
		writeError(c, http.StatusUnprocessableEntity, "workflow.definition_disabled", err.Error())
	case errors.Is(err, runtime.ErrWorkflowDefinitionInvalid), errors.Is(err, runtime.ErrWorkflowDependencyCycle),
		errors.Is(err, runtime.ErrWorkflowDependencyMissing), errors.Is(err, runtime.ErrWorkflowSchemaInvalid),
		errors.Is(err, runtime.ErrWorkflowSchemaValidation), errors.Is(err, runtime.ErrWorkflowExpressionInvalid),
		errors.Is(err, runtime.ErrWorkflowExpressionLimit):
		writeError(c, http.StatusUnprocessableEntity, "workflow.definition_invalid", err.Error())
	case errors.Is(err, runtime.ErrWorkflowBudgetExceeded), errors.Is(err, runtime.ErrWorkflowStateTooLarge):
		writeError(c, http.StatusUnprocessableEntity, "workflow.limit_exceeded", err.Error())
	case errors.Is(err, runtime.ErrWorkflowResultInvalid):
		writeError(c, http.StatusUnprocessableEntity, "workflow.result_invalid", err.Error())
	default:
		writeRunControlError(c, err)
	}
}

func workflowRevision(value string) int {
	revision, _ := strconv.Atoi(strings.TrimSpace(value))
	return max(revision, 0)
}
