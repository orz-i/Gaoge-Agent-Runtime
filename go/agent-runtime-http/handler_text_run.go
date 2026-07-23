package http

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueTitle48EAAEED      = "title"
	valueToolCallID5A0636DD = "toolCallID"
	valueEndedAt            = "endedAt"
	valueContentHash        = "contentHash"
	valueErrorMessage       = "errorMessage"
	valueStartedAt          = "startedAt"
	valueEvidenceID         = "evidenceID"
	valueThread             = "thread"
	valueUpdatedAt          = "updatedAt"
	valueExcerpt            = "excerpt"
	valueMessageID          = "messageID"
	valueSourceContentHash  = "sourceContentHash"
	valueSourceKind         = "sourceKind"
)

var errMultipleJSONValues = errors.New("request body must contain one JSON value")

const (
	valueRunID1DA2F0B6   = "runID"
	valueStatus00E8FE8E  = "status"
	valueSummary15D65CC8 = "summary"
)

const (
	valueCheckpointID85E4F670 = "checkpointID"
	valueCookieB28505BD       = "cookie"
	valueCreatedAtE3B65D13    = "createdAt"
	valueGoal51342CCB         = "goal"
	valueKind72883EFB         = "kind"
	valueResults3F4B84CD      = "results"
	valueRunA037153B          = "run"
	valueStepIDF52B51EE       = "stepID"
	valueStepsF083D597        = "steps"
	valueType9065E5F9         = "type"
)

type TextRunRequestInput struct {
	Content             string   `json:"content" binding:"required,max=20000"`
	ContentType         string   `json:"contentType" binding:"omitempty,oneof=text markdown"`
	FileIDs             []string `json:"fileIDs" binding:"max=20"`
	OutputIDs           []string `json:"outputIDs" binding:"max=50"`
	EvidenceIDs         []string `json:"evidenceIDs" binding:"max=50"`
	HTMLVisualPrompt    bool     `json:"htmlVisualPrompt"`
	HTMLVisualColorMode string   `json:"htmlVisualColorMode" binding:"omitempty,oneof=light dark"`
}

type StartTextRunRequest struct {
	Thread        RunThreadRequest          `json:"thread" binding:"required"`
	Input         TextRunRequestInput       `json:"input" binding:"required"`
	ClientRunID   string                    `json:"clientRunID" binding:"omitempty,max=64"`
	Model         string                    `json:"model" binding:"omitempty,max=128"`
	ExecutionMode string                    `json:"executionMode" binding:"omitempty,oneof=auto direct plan"`
	Options       map[string]interface{}    `json:"options"`
	ToolKeys      *[]string                 `json:"toolKeys" binding:"omitempty,max=128,dive,max=255"`
	SkillKeys     *[]string                 `json:"skillKeys" binding:"omitempty,max=128,dive,max=64"`
	Workspace     *runtime.WorkspaceRequest `json:"workspace"`
}

type RunThreadRequest struct {
	Kind             string               `json:"kind" binding:"required,max=64"`
	ID               string               `json:"id" binding:"required,max=128"`
	ParentProjection *model.ProjectionRef `json:"parentProjection,omitempty"`
	SourceProjection *model.ProjectionRef `json:"sourceProjection,omitempty"`
	BranchReason     string               `json:"branchReason" binding:"omitempty,oneof=default retry edit"`
}

type RunQueueRequest struct {
	Thread           RunThreadRequest          `json:"thread" binding:"required"`
	Input            TextRunRequestInput       `json:"input" binding:"required"`
	ClientQueueID    string                    `json:"clientQueueID" binding:"omitempty,max=64"`
	Model            string                    `json:"model" binding:"omitempty,max=128"`
	ExecutionMode    string                    `json:"executionMode" binding:"omitempty,oneof=auto direct plan"`
	Options          map[string]interface{}    `json:"options"`
	ToolKeys         *[]string                 `json:"toolKeys" binding:"omitempty,max=128,dive,max=255"`
	SkillKeys        *[]string                 `json:"skillKeys" binding:"omitempty,max=128,dive,max=64"`
	ExpectedRevision int                       `json:"expectedRevision"`
	Workspace        *runtime.WorkspaceRequest `json:"workspace"`
}

func (h *Handler) GetTextRunPolicy(c *gin.Context) {
	writeSuccess(c, h.service.TextRunPolicy())
}

// StartTextRun creates the sole text-runtime run contract and both message projections.
func (h *Handler) StartTextRun(c *gin.Context) {
	var req StartTextRunRequest
	if err := bindStrictJSON(c, &req); err != nil {
		invalidBody(c, err)
		return
	}
	actor := h.actorRef(c)
	thread := threadRef(req.Thread.Kind, req.Thread.ID)
	snapshot, err := h.service.ResolveThread(c.Request.Context(), actor, thread)
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	result, err := h.service.StartTextRun(c.Request.Context(), runtime.StartTextRunInput{Actor: actor, Thread: thread, RequestID: h.requestID(c), Goal: req.Input.Content, ContentType: req.Input.ContentType, Environment: snapshot.Environment, ClientRunID: req.ClientRunID, PlatformModelName: req.Model, ExecutionMode: req.ExecutionMode, Options: sanitizeRunOptions(req.Options), FileIDs: req.Input.FileIDs, OutputIDs: req.Input.OutputIDs, EvidenceIDs: req.Input.EvidenceIDs, ToolKeys: req.ToolKeys, SkillRefs: skillRefs(req.SkillKeys), ParentProjection: req.Thread.ParentProjection, SourceProjection: req.Thread.SourceProjection, BranchReason: req.Thread.BranchReason, HTMLVisualPromptEnabled: req.Input.HTMLVisualPrompt, HTMLVisualColorMode: req.Input.HTMLVisualColorMode, ThreadModel: snapshot.DefaultModel, ThreadProvider: snapshot.ModelProvider, ThreadScope: snapshot.BindingScope, Workspace: req.Workspace})
	if err != nil {
		writeTextRunError(c, err)
		return
	}
	data := map[string]interface{}{valueRunA037153B: toRunResponse(result.Run, req.Thread.ID), "rootStep": toRunStepResponse(result.Step), "inputProjectionRef": projectionRefResponse(result.Projection.Input), "outputProjectionRef": projectionRefResponse(result.Projection.Output)}
	c.JSON(http.StatusAccepted, data)
}

func (h *Handler) ListRuns(c *gin.Context) {
	if strings.TrimSpace(c.Query("threadKind")) == "" || strings.TrimSpace(c.Query("threadID")) == "" {
		writeError(c, http.StatusBadRequest, "", "threadKind and threadID are required")
		return
	}
	actor := h.actorRef(c)
	thread := threadRef(c.Query("threadKind"), c.Query("threadID"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	items, total, err := h.service.ListRunRecords(c.Request.Context(), actor, thread, page, pageSize)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		results = append(results, toRunResponse(item, c.Query("threadID")))
	}
	writePage(c, total, results)
}

func bindStrictJSON(c *gin.Context, target interface{}) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errMultipleJSONValues
	}
	return binding.Validator.ValidateStruct(target)
}

func (h *Handler) runQueueContext(c *gin.Context, ref model.ThreadRef) (model.ActorRef, model.ThreadRef, *runtime.ThreadSnapshot, error) {
	actor := h.actorRef(c)
	thread := threadRef(ref.Kind, ref.ID)
	snapshot, err := h.service.ResolveThread(c.Request.Context(), actor, thread)
	if err != nil {
		return model.ActorRef{}, model.ThreadRef{}, nil, err
	}
	return actor, thread, snapshot, nil
}
func toRunQueueRequest(req RunQueueRequest, snapshot *runtime.ThreadSnapshot) runtime.RunQueueRequest {
	request := runtime.RunQueueRequest{Input: runtime.RunQueueInput{Content: req.Input.Content, ContentType: req.Input.ContentType, FileIDs: req.Input.FileIDs, OutputIDs: req.Input.OutputIDs, EvidenceIDs: req.Input.EvidenceIDs, HTMLVisualPrompt: req.Input.HTMLVisualPrompt, HTMLVisualColorMode: req.Input.HTMLVisualColorMode}, Model: req.Model, ExecutionMode: req.ExecutionMode, Options: sanitizeRunOptions(req.Options), ToolKeys: req.ToolKeys, SkillRefs: skillRefs(req.SkillKeys), ParentProjection: req.Thread.ParentProjection, SourceProjection: req.Thread.SourceProjection, BranchReason: req.Thread.BranchReason, Workspace: req.Workspace}
	if snapshot != nil {
		request.Environment = snapshot.Environment
	}
	return request
}

func skillRefs(keys *[]string) *[]model.ResourceRef {
	if keys == nil {
		return nil
	}
	result := make([]model.ResourceRef, 0, len(*keys))
	for _, key := range *keys {
		if value := strings.TrimSpace(key); value != "" {
			result = append(result, model.ResourceRef{Kind: runtime.ResourceKindSkill, ID: value})
		}
	}
	return &result
}
func runQueueResponse(item model.QueueItem) map[string]interface{} {
	var request map[string]interface{}
	_ = json.Unmarshal([]byte(item.RequestJSON), &request)
	return map[string]interface{}{"queueID": item.QueueID, "clientQueueID": item.ClientQueueID, valueThread: threadRefResponse(item.Thread), valueStatus00E8FE8E: item.Status, "position": item.Position, "revision": item.Revision, "attemptCount": item.AttemptCount, "request": canonicalizeKnownRuntimeRefs(request), "anchorRunID": item.AnchorRunID, "startedRunID": item.StartedRunID, valueErrorCode8B63C5B4: item.ErrorCode, valueErrorMessage: item.ErrorMessage, "nextAttemptAt": item.NextAttemptAt, valueCreatedAtE3B65D13: item.CreatedAt, valueUpdatedAt: item.UpdatedAt}
}

// ListRunQueue lists the durable queue for a host thread.
func (h *Handler) ListRunQueue(c *gin.Context) {
	threadID := c.Query("threadID")
	actor, thread, _, err := h.runQueueContext(c, threadRef(c.Query("threadKind"), threadID))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	items, err := h.service.ListRunQueue(c.Request.Context(), actor, thread)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		results = append(results, runQueueResponse(item))
	}
	writeSuccess(c, map[string]interface{}{valueResults3F4B84CD: results})
}

// EnqueueRun appends a durable text run request.
func (h *Handler) EnqueueRun(c *gin.Context) {
	var req RunQueueRequest
	if err := bindStrictJSON(c, &req); err != nil {
		invalidBody(c, err)
		return
	}
	actor, thread, snapshot, err := h.runQueueContext(c, threadRef(req.Thread.Kind, req.Thread.ID))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	item, reused, err := h.service.EnqueueRun(c.Request.Context(), runtime.EnqueueRunInput{Actor: actor, Thread: thread, ClientQueueID: req.ClientQueueID, Request: toRunQueueRequest(req, snapshot)})
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	data := runQueueResponse(*item)
	data["reused"] = reused
	c.JSON(http.StatusAccepted, data)
}

// UpdateRunQueue updates a queued text run using an expected revision CAS.
func (h *Handler) UpdateRunQueue(c *gin.Context) {
	var req RunQueueRequest
	if err := bindStrictJSON(c, &req); err != nil {
		invalidBody(c, err)
		return
	}
	actor, thread, snapshot, err := h.runQueueContext(c, threadRef(req.Thread.Kind, req.Thread.ID))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	item, err := h.service.UpdateRunQueue(c.Request.Context(), actor, thread, strings.TrimSpace(c.Param("queue_id")), req.ExpectedRevision, toRunQueueRequest(req, snapshot))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, runQueueResponse(*item))
}

// CancelRunQueue cancels one queued text run request.
func (h *Handler) CancelRunQueue(c *gin.Context) {
	actor, thread, _, err := h.runQueueContext(c, threadRef(c.Query("threadKind"), c.Query("threadID")))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	item, err := h.service.CancelRunQueue(c.Request.Context(), actor, thread, strings.TrimSpace(c.Param("queue_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, runQueueResponse(*item))
}

// PrioritizeRunQueue moves one queue item to the front.
func (h *Handler) PrioritizeRunQueue(c *gin.Context) {
	actor, thread, _, err := h.runQueueContext(c, threadRef(c.Query("threadKind"), c.Query("threadID")))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	item, err := h.service.PrioritizeRunQueue(c.Request.Context(), actor, thread, strings.TrimSpace(c.Param("queue_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, runQueueResponse(*item))
}

// InterruptAndSendRun interrupts the active run and prioritizes one queued request.
func (h *Handler) InterruptAndSendRun(c *gin.Context) {
	actor, thread, _, err := h.runQueueContext(c, threadRef(c.Query("threadKind"), c.Query("threadID")))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "thread not found")
		return
	}
	item, err := h.service.InterruptAndSendRun(c.Request.Context(), actor, thread, strings.TrimSpace(c.Param("queue_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, runQueueResponse(*item))
}

// GetTextRun returns one text run and its durable projections.
func (h *Handler) GetTextRun(c *gin.Context) {
	runID := strings.TrimSpace(c.Param("run_id"))
	detail, err := h.service.GetTextRunDetail(c.Request.Context(), h.actorRef(c), runID)
	if err != nil {
		writeError(c, http.StatusNotFound, "", "text run not found")
		return
	}
	steps := make([]map[string]interface{}, 0, len(detail.Steps))
	for _, s := range detail.Steps {
		steps = append(steps, toRunStepResponse(s))
	}
	result := map[string]interface{}{valueRunA037153B: toRunResponse(detail.Run, h.runThreadID(c, detail.Run)), valueStepsF083D597: steps}
	result["inputProjectionRef"] = projectionRefResponse(detail.Projection.Input)
	result["outputProjectionRef"] = projectionRefResponse(detail.Projection.Output)
	if detail.Config != nil {
		result["effectiveConfig"] = textRunConfigResponse(detail.Config)
	}
	if detail.Context != nil {
		result["context"] = detail.Context
	}
	writeSuccess(c, result)
}

func (h *Handler) GetWorkbench(c *gin.Context) {
	view, err := h.service.GetWorkbench(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	steps, phases, groups := workbenchTraceResponses(view)
	interactions, checkpoints, outputs := workbenchDetailResponses(view)
	result := map[string]interface{}{"projectionVersion": runtime.WorkbenchContractVersion, "projectionSeq": view.ProjectionSeq, "projectionPersisted": view.ProjectionPersisted, valueRunA037153B: toRunResponse(view.Run, h.runThreadID(c, view.Run)), "overview": view.Overview, "phases": phases, "toolGroups": groups, valueStepsF083D597: steps, "interactions": interactions, "checkpoints": checkpoints, "outputs": outputs, "context": view.Context, "effectiveConfig": textRunConfigResponse(view.Config), "graph": map[string]interface{}{"nodes": view.GraphNodes, "edges": view.GraphEdges}, "selectionIndex": view.SelectionIndex}
	if view.Plan != nil {
		revisions := make([]map[string]interface{}, 0, len(view.Plan.Revisions))
		for _, item := range view.Plan.Revisions {
			revisions = append(revisions, planResponse(item))
		}
		result["plan"] = map[string]interface{}{"current": planResponse(*view.Plan.Current), "revisions": revisions, valueStepsF083D597: steps}
	}
	if view.PendingInteraction != nil {
		result["pendingInteraction"] = runInteractionResponse(*view.PendingInteraction)
	}
	writeSuccess(c, result)
}

func (h *Handler) runThreadID(c *gin.Context, run model.Run) string {
	return run.Thread.ID
}

func workbenchTraceResponses(view *runtime.Workbench) ([]map[string]interface{}, []map[string]interface{}, []map[string]interface{}) {
	steps := make([]map[string]interface{}, 0, len(view.Steps))
	for _, item := range view.Steps {
		steps = append(steps, toRunStepResponse(item))
	}
	phases := make([]map[string]interface{}, 0, len(view.Phases))
	for _, item := range view.Phases {
		phases = append(phases, map[string]interface{}{"phaseID": item.PhaseID, valueKind72883EFB: item.Kind, valueTitle48EAAEED: item.Title, valueSummary15D65CC8: item.Summary, valueStatus00E8FE8E: item.Status, "startSeq": item.StartSeq, "endSeq": nullableRunSeq(item.EndSeq), "stepIDs": item.StepIDs, "toolCallIDs": item.ToolCallIDs, "outputIDs": item.OutputIDs, valueStartedAt: item.StartedAt, valueEndedAt: item.EndedAt})
	}
	groups := make([]map[string]interface{}, 0, len(view.ToolGroups))
	for _, item := range view.ToolGroups {
		groups = append(groups, map[string]interface{}{"groupID": item.GroupID, "phaseID": item.PhaseID, valueStepIDF52B51EE: item.StepID, valueTitle48EAAEED: item.Title, valueStatus00E8FE8E: item.Status, "startSeq": item.StartSeq, "endSeq": nullableRunSeq(item.EndSeq), "toolCallIDs": item.ToolCallIDs, "toolNames": item.ToolNames, "toolEventIDs": item.ToolEventIDs, "toolStatuses": item.ToolStatuses})
	}
	return steps, phases, groups
}

func workbenchDetailResponses(view *runtime.Workbench) ([]map[string]interface{}, []map[string]interface{}, []map[string]interface{}) {
	interactions := make([]map[string]interface{}, 0, len(view.Interactions))
	for _, item := range view.Interactions {
		interactions = append(interactions, runInteractionResponse(item))
	}
	checkpoints := make([]map[string]interface{}, 0, len(view.Checkpoints))
	for _, item := range view.Checkpoints {
		checkpoints = append(checkpoints, map[string]interface{}{valueCheckpointID85E4F670: item.CheckpointID, valueRunID1DA2F0B6: item.RunID, "eventSeq": item.EventSeq, valueStepIDF52B51EE: item.StepID, valueToolCallID5A0636DD: item.ToolCallID, valueKind72883EFB: item.Kind, valueStatus00E8FE8E: item.Status, valueCreatedAtE3B65D13: item.CreatedAt})
	}
	outputs := make([]map[string]interface{}, 0, len(view.Outputs))
	for _, item := range view.Outputs {
		outputs = append(outputs, outputResponse(item))
	}
	return interactions, checkpoints, outputs
}

func nullableRunSeq(value int64) interface{} {
	if value <= 0 {
		return nil
	}
	return value
}

// StreamRunEvents replays and tails schemaVersion 1 NDJSON events after a sequence cursor.
func (h *Handler) StreamRunEvents(c *gin.Context) {
	runID := strings.TrimSpace(c.Param("run_id"))
	after, _ := strconv.ParseInt(c.Query("afterSeq"), 10, 64)
	if _, err := h.service.GetRunCursor(c.Request.Context(), h.actorRef(c), runID); err != nil {
		writeError(c, http.StatusNotFound, "", "text run not found")
		return
	}
	// Subscribe before reading the store so a commit between authorization and
	// catch-up becomes a wake-up instead of a missed realtime notification.
	_, tail, unsubscribe, subscribed := h.service.SubscribeRunNotifications(c.Request.Context(), h.actorRef(c), runID, 0)
	beginNDJSONStream(c)
	stream := runEventStream{handler: h, context: c, runID: runID, writer: newStreamEventWriter(c, nil), last: after, tail: tail, unsubscribe: unsubscribe, subscribed: subscribed}
	defer stream.close()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		cursor, err := h.service.GetRunCursor(c.Request.Context(), h.actorRef(c), runID)
		if err != nil {
			return
		}
		if err = stream.catchUp(cursor.LastEventSeq); err != nil {
			return
		}
		if isRunStreamClosableStatus(cursor.Status) && stream.last >= cursor.LastEventSeq {
			return
		}
		if !stream.wait(ticker.C) {
			return
		}
	}
}

type runEventStream struct {
	handler     *Handler
	context     *gin.Context
	runID       string
	writer      *streamEventWriter
	last        int64
	tail        <-chan runtime.GenerationStreamEvent
	unsubscribe func()
	subscribed  bool
}

func (stream *runEventStream) close() {
	if stream.unsubscribe != nil {
		stream.unsubscribe()
	}
}

func (stream *runEventStream) catchUp(target int64) error {
	for target <= 0 || stream.last < target {
		events, err := stream.handler.service.ListRunEventsAfter(stream.context.Request.Context(), stream.handler.actorRef(stream.context), stream.runID, stream.last)
		if err != nil || len(events) == 0 {
			return err
		}
		for index := range events {
			if int64(events[index].Seq) <= stream.last {
				continue
			}
			if err = stream.writer.Write(runEventResponse(events[index])); err != nil {
				return err
			}
			stream.last = int64(events[index].Seq)
		}
	}
	return nil
}

func (stream *runEventStream) wait(ticks <-chan time.Time) bool {
	select {
	case <-stream.context.Request.Context().Done():
		return false
	case _, ok := <-stream.tail:
		if stream.subscribed && !ok {
			stream.tail, stream.subscribed = nil, false
		}
	case <-ticks:
		stream.resubscribe()
	}
	return true
}

func (stream *runEventStream) resubscribe() {
	if stream.subscribed {
		return
	}
	_, tail, unsubscribe, ok := stream.handler.service.SubscribeRunNotifications(stream.context.Request.Context(), stream.handler.actorRef(stream.context), stream.runID, 0)
	if !ok {
		return
	}
	stream.close()
	stream.tail, stream.unsubscribe, stream.subscribed = tail, unsubscribe, true
}

func (h *Handler) GetRunEventHistory(c *gin.Context) {
	runID := strings.TrimSpace(c.Param("run_id"))
	beforeSeq, _ := strconv.ParseInt(c.Query("beforeSeq"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	page, err := h.service.ListRunEventHistory(c.Request.Context(), h.actorRef(c), runID, beforeSeq, limit)
	if err != nil {
		writeError(c, http.StatusNotFound, "", "text run not found")
		return
	}
	results := make([]map[string]interface{}, 0, len(page.Results))
	for i := range page.Results {
		results = append(results, runEventResponse(page.Results[i]))
	}
	result := map[string]interface{}{valueResults3F4B84CD: results, "hasMore": page.HasMore}
	if page.NextBeforeSeq > 0 {
		result["nextBeforeSeq"] = page.NextBeforeSeq
	}
	writeSuccess(c, result)
}

func (h *Handler) GetRunEvent(c *gin.Context) {
	event, err := h.service.GetRunEvent(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")), strings.TrimSpace(c.Param("event_id")))
	if err != nil {
		writeError(c, http.StatusNotFound, "", "run event not found")
		return
	}
	result := runEventResponse(*event)
	result["inputJSON"] = redactedRunJSONString(event.InputJSON)
	result["outputJSON"] = redactedRunJSONString(event.OutputJSON)
	result["errorJSON"] = redactedRunJSONString(event.ErrorJSON)
	writeSuccess(c, result)
}

type resolveRunInteractionRequest struct {
	ClientResolveID string      `json:"clientResolveID" binding:"required,max=64"`
	Response        interface{} `json:"response" binding:"required"`
}

type ResumeTextRunRequest struct {
	CheckpointID   string `json:"checkpointID" binding:"omitempty,max=96"`
	ClientResumeID string `json:"clientResumeID" binding:"required,max=64"`
}

func (h *Handler) GetPlan(c *gin.Context) {
	view, err := h.service.GetPlan(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	revisions := make([]map[string]interface{}, 0, len(view.Revisions))
	for _, item := range view.Revisions {
		revisions = append(revisions, planResponse(item))
	}
	steps := make([]map[string]interface{}, 0, len(view.Steps))
	for _, item := range view.Steps {
		if item.PlanID != "" {
			steps = append(steps, toRunStepResponse(item))
		}
	}
	writeSuccess(c, map[string]interface{}{"current": planResponse(*view.Current), "revisions": revisions, valueStepsF083D597: steps})
}

func (h *Handler) ListRunInteractions(c *gin.Context) {
	items, err := h.service.ListRunInteractions(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		results = append(results, runInteractionResponse(item))
	}
	writeSuccess(c, map[string]interface{}{valueResults3F4B84CD: results})
}

func (h *Handler) ResolveRunInteraction(c *gin.Context) {
	var req resolveRunInteractionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		invalidBody(c, err)
		return
	}
	item, err := h.service.ResolveRunInteraction(c.Request.Context(), runtime.ResolveRunInteractionInput{Actor: h.actorRef(c), RunID: strings.TrimSpace(c.Param("run_id")), InteractionID: strings.TrimSpace(c.Param("interaction_id")), ClientResolveID: req.ClientResolveID, Response: req.Response})
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, runInteractionResponse(*item))
}

func (h *Handler) ListRunCheckpoints(c *gin.Context) {
	items, err := h.service.ListRunCheckpoints(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		results = append(results, map[string]interface{}{valueCheckpointID85E4F670: item.CheckpointID, valueRunID1DA2F0B6: item.RunID, "eventSeq": item.EventSeq, valueStepIDF52B51EE: item.StepID, valueToolCallID5A0636DD: item.ToolCallID, valueKind72883EFB: item.Kind, valueStatus00E8FE8E: item.Status, valueCreatedAtE3B65D13: item.CreatedAt})
	}
	writeSuccess(c, map[string]interface{}{valueResults3F4B84CD: results})
}

// ResumeTextRun resumes one suspended text run from a durable checkpoint.
func (h *Handler) ResumeTextRun(c *gin.Context) {
	var req ResumeTextRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		invalidBody(c, err)
		return
	}
	checkpoint, reused, err := h.service.ResumeTextRun(c.Request.Context(), runtime.ResumeTextRunInput{Actor: h.actorRef(c), RunID: strings.TrimSpace(c.Param("run_id")), CheckpointID: req.CheckpointID, ClientResumeID: req.ClientResumeID})
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, map[string]interface{}{valueCheckpointID85E4F670: checkpoint.CheckpointID, valueRunID1DA2F0B6: checkpoint.RunID, valueStatus00E8FE8E: checkpoint.Status, "reused": reused})
}

// RetireTextRun abandons recovery and retires one suspended text run.
func (h *Handler) RetireTextRun(c *gin.Context) {
	run, reused, err := h.service.RetireTextRun(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, map[string]interface{}{valueRunID1DA2F0B6: run.RunID, valueStatus00E8FE8E: run.Status, "reused": reused})
}

func (h *Handler) ListOutputs(c *gin.Context) {
	items, err := h.service.ListOutputs(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		results = append(results, outputResponse(item))
	}
	writeSuccess(c, map[string]interface{}{valueResults3F4B84CD: results})
}

func (h *Handler) ListUserOutputs(c *gin.Context) {
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	items, nextCursor, err := h.service.ListUserOutputs(c.Request.Context(), h.actorRef(c), c.Query("q"), c.Query("cursor"), limit)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		result := outputResponse(item.OutputRef)
		result["sourceRun"] = map[string]interface{}{valueGoal51342CCB: item.SourceRunGoal, valueStatus00E8FE8E: item.SourceRunStatus, "model": item.SourceRunModel}
		result[valueThread] = map[string]interface{}{valueKind72883EFB: item.Thread.Kind, "id": item.Thread.ID, valueTitle48EAAEED: item.ThreadTitle}
		results = append(results, result)
	}
	writeSuccess(c, map[string]interface{}{valueResults3F4B84CD: results, "nextCursor": nextCursor})
}

func (h *Handler) GetOutput(c *gin.Context) {
	version, _ := strconv.Atoi(strings.TrimSpace(c.Query("version")))
	item, err := h.service.GetOutputVersion(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("output_id")), version)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, outputListItemResponse(*item))
}

func (h *Handler) ListOutputVersions(c *gin.Context) {
	before, _ := strconv.Atoi(strings.TrimSpace(c.Query("cursor")))
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	items, hasMore, err := h.service.ListOutputVersions(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("output_id")), before, limit)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	results := make([]map[string]interface{}, 0, len(items))
	next := 0
	for _, item := range items {
		results = append(results, outputListItemResponse(item))
		next = item.Version
	}
	result := map[string]interface{}{valueResults3F4B84CD: results, "hasMore": hasMore}
	if hasMore && next > 0 {
		result["nextCursor"] = strconv.Itoa(next)
	}
	writeSuccess(c, result)
}

func (h *Handler) GetOutputPreview(c *gin.Context) {
	version, err := strconv.Atoi(strings.TrimSpace(c.Param("version")))
	if err != nil || version <= 0 {
		writeError(c, http.StatusBadRequest, "", "invalid output version")
		return
	}
	item, preview, err := h.service.BuildOutputPreview(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("output_id")), version)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	writeSuccess(c, map[string]interface{}{"output": outputListItemResponse(*item), "preview": preview})
}

func (h *Handler) DownloadOutput(c *gin.Context) {
	version, err := strconv.Atoi(strings.TrimSpace(c.Param("version")))
	if err != nil || version <= 0 {
		writeError(c, http.StatusBadRequest, "", "invalid output version")
		return
	}
	_, content, err := h.service.OpenOutputDownload(c.Request.Context(), h.actorRef(c), strings.TrimSpace(c.Param("output_id")), version)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	defer func() { _ = content.Reader.Close() }()
	contentType := strings.TrimSpace(content.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": content.FileName})
	c.Header("Content-Disposition", disposition)
	c.Header("Cache-Control", "private, max-age=60")
	if _, err = io.Copy(c.Writer, content.Reader); err != nil {
		c.Abort()
	}
}

// CreateEvidence freezes a selected output range or host projection.
func (h *Handler) CreateEvidence(c *gin.Context) {
	var req CreateEvidenceRequest
	if err := bindStrictJSON(c, &req); err != nil {
		invalidBody(c, err)
		return
	}
	input := runtime.CreateEvidenceInput{
		Actor: h.actorRef(c), SourceKind: req.Source.Kind, OutputID: req.Source.ID, Version: req.Source.Version,
		Kind: req.Selection.Kind, Title: req.Selection.Title, Start: req.Selection.Start, End: req.Selection.End,
		RowStart: req.Selection.RowStart, RowEnd: req.Selection.RowEnd, ColumnStart: req.Selection.ColumnStart, ColumnEnd: req.Selection.ColumnEnd,
	}
	switch req.Source.Kind {
	case "output":
		if strings.TrimSpace(req.Source.ID) == "" || req.Source.Version <= 0 || req.Source.Thread != nil || req.Source.Projection != nil {
			writeError(c, http.StatusBadRequest, "", "output evidence requires id and version")
			return
		}
	case "projection":
		if req.Source.Thread == nil || req.Source.Projection == nil || strings.TrimSpace(req.Source.ID) != "" || req.Source.Version != 0 {
			writeError(c, http.StatusBadRequest, "", "projection evidence requires thread and projection references")
			return
		}
		input.Thread, input.Projection = *req.Source.Thread, *req.Source.Projection
	default:
		writeError(c, http.StatusBadRequest, "", "unsupported evidence source")
		return
	}
	item, err := h.service.CreateEvidence(c.Request.Context(), input)
	if err != nil {
		writeRunControlError(c, err)
		return
	}
	source := map[string]interface{}{"kind": req.Source.Kind}
	if req.Source.Kind == "output" {
		source["id"], source["version"] = item.SourceID, req.Source.Version
	} else {
		source["thread"], source["projection"] = threadRefResponse(input.Thread), projectionRefResponse(item.Projection)
	}
	data := map[string]interface{}{valueEvidenceID: item.EvidenceID, "source": source, valueKind72883EFB: item.Kind, valueTitle48EAAEED: item.Title, valueExcerpt: item.Excerpt, valueContentHash: item.ContentHash, valueSourceContentHash: item.SourceContentHash, valueCreatedAtE3B65D13: item.CreatedAt}
	if strings.TrimSpace(item.Projection.Kind) != "" && strings.TrimSpace(item.Projection.ID) != "" {
		data["projectionRef"] = projectionRefResponse(item.Projection)
	}
	writeSuccess(c, data)
}

func outputListItemResponse(item model.OutputListItem) map[string]interface{} {
	result := outputResponse(item.OutputRef)
	result["sourceRun"] = map[string]interface{}{valueGoal51342CCB: item.SourceRunGoal, valueStatus00E8FE8E: item.SourceRunStatus, "model": item.SourceRunModel}
	result[valueThread] = map[string]interface{}{valueKind72883EFB: item.Thread.Kind, "id": item.Thread.ID, valueTitle48EAAEED: item.ThreadTitle}
	return result
}

func outputResponse(item model.OutputRef) map[string]interface{} {
	result := map[string]interface{}{
		"outputID":         item.OutputID,
		valueRunID1DA2F0B6: item.RunID, valueStepIDF52B51EE: item.StepID, valueToolCallID5A0636DD: item.ToolCallID,
		"sourceToolCallID": item.SourceToolCallID, "sourceEventID": item.SourceEventID,
		"sourceSnapshotID": item.SourceSnapshotID, "parentOutputID": item.ParentOutputID,
		valueKind72883EFB: item.Kind, valueTitle48EAAEED: item.Title, valueSummary15D65CC8: item.Summary, "fileID": item.FileID,
		"fileSHA256": item.FileSHA256, "fileMIMEType": item.FileMIMEType,
		"version": item.Version, valueStatus00E8FE8E: item.Status, valueCreatedAtE3B65D13: item.CreatedAt,
	}
	if strings.TrimSpace(item.Projection.Kind) != "" && strings.TrimSpace(item.Projection.ID) != "" {
		result["projectionRef"] = projectionRefResponse(item.Projection)
	}
	var artifact map[string]interface{}
	if json.Unmarshal([]byte(item.PreviewJSON), &artifact) == nil && len(artifact) > 0 {
		result["artifact"] = artifact
	}
	return result
}

func planResponse(item model.Plan) map[string]interface{} {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(item.PayloadJSON), &payload)
	return map[string]interface{}{"planID": item.PlanID, valueRunID1DA2F0B6: item.RunID, "revision": item.Revision, valueStatus00E8FE8E: item.Status, valueGoal51342CCB: item.Goal, valueSummary15D65CC8: item.Summary, "plan": payload, "approvedAt": item.ApprovedAt, valueCreatedAtE3B65D13: item.CreatedAt}
}

func runInteractionResponse(item model.Interaction) map[string]interface{} {
	var requestPayload map[string]interface{}
	_ = json.Unmarshal([]byte(item.RequestPayloadJSON), &requestPayload)
	if item.Type == model.InteractionApproveTool {
		arguments := requestPayload["arguments"]
		preview := map[string]interface{}{
			"action": requestPayload["toolName"], "sideEffectLevel": requestPayload["sideEffectLevel"],
			"redactedArguments": redactRunValue(arguments, 0),
		}
		if values, ok := arguments.(map[string]interface{}); ok {
			if value, exists := values["action"]; exists {
				preview["action"] = value
			}
			if value, exists := values["resource"]; exists {
				preview["resource"] = redactRunValue(value, 1)
			}
			if value, exists := values["target"]; exists {
				preview["target"] = redactRunValue(value, 1)
			}
		}
		requestPayload = map[string]interface{}{"toolName": requestPayload["toolName"], valueToolCallID5A0636DD: requestPayload[valueToolCallID5A0636DD], "preview": preview}
	}
	return map[string]interface{}{"interactionID": item.InteractionID, valueRunID1DA2F0B6: item.RunID, valueStepIDF52B51EE: item.StepID, valueToolCallID5A0636DD: item.ToolCallID, valueType9065E5F9: item.Type, valueStatus00E8FE8E: item.Status, "request": requestPayload, "responseSchemaJSON": item.ResponseSchemaJSON, "requestedAt": item.RequestedAt, "expiresAt": item.ExpiresAt, "resolvedAt": item.ResolvedAt}
}

func redactedRunJSONString(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var value interface{}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return `{"value":"[redacted-unstructured]"}`
	}
	encoded, err := json.Marshal(redactRunValue(value, 0))
	if err != nil {
		return `{"value":"[redacted-encoding-error]"}`
	}
	if len(encoded) > 4096 {
		return `{"value":"[truncated]"}`
	}
	return string(encoded)
}

func redactRunValue(value interface{}, depth int) interface{} {
	if depth >= 4 {
		return "[max-depth]"
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		return redactRunMap(typed, depth)
	case []interface{}:
		return redactRunSlice(typed, depth)
	case string:
		return truncateRedactedRunString(typed)
	default:
		return typed
	}
}

func redactRunMap(value map[string]interface{}, depth int) map[string]interface{} {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 30 {
		keys = keys[:30]
	}
	result := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if isSensitiveRunKey(key) {
			result[key] = "[redacted]"
		} else {
			result[key] = redactRunValue(value[key], depth+1)
		}
	}
	return result
}

func redactRunSlice(value []interface{}, depth int) []interface{} {
	if len(value) > 10 {
		value = value[:10]
	}
	result := make([]interface{}, 0, len(value))
	for _, item := range value {
		result = append(result, redactRunValue(item, depth+1))
	}
	return result
}

func truncateRedactedRunString(value string) string {
	runes := []rune(value)
	if len(runes) > 256 {
		return string(runes[:256]) + "…"
	}
	return value
}

func isSensitiveRunKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, fragment := range []string{"password", "passwd", "token", "secret", "authorization", "api_key", "apikey", valueCookieB28505BD, "signature", "private_key", "credential"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func writeRunControlError(c *gin.Context, err error) {
	mapped := mapRunControlError(err)
	if mapped.code != "" {
		writeError(c, mapped.status, mapped.code, mapped.message)
		return
	}
	writeError(c, mapped.status, "", mapped.message)
}

type runControlErrorMapping struct {
	target  error
	status  int
	code    string
	message string
}

func mapRunControlError(err error) runControlErrorMapping {
	rules := []runControlErrorMapping{
		{runtime.ErrOutputVersionConflict, http.StatusConflict, "output.version_conflict", "output head changed; retry with the latest published version"},
		{runtime.ErrOutputLineageInvalid, http.StatusBadRequest, "output.lineage_invalid", "output file or parent lineage is invalid"},
		{runtime.ErrEvidenceSelectionTooLarge, http.StatusUnprocessableEntity, "evidence.too_large", "the message is too long to reference in full; select a shorter excerpt"},
		{runtime.ErrEvidenceSelectionInvalid, http.StatusBadRequest, "evidence.invalid", "evidence selection is invalid"},
		{runtime.ErrRunInteractionResponseInvalid, http.StatusBadRequest, "interaction.response_invalid", "interaction response does not match the persisted schema"},
		{runtime.ErrRunInteractionSchemaIncompatible, http.StatusConflict, "interaction.schema_incompatible", "interaction response schema is invalid or unsupported"},
		{runtime.ErrRunInteractionConflict, http.StatusConflict, "interaction.idempotency_conflict", "interaction was already resolved with a different response"},
		{runtime.ErrRunSnapshotIncompatible, http.StatusConflict, "run.snapshot_incompatible", "text run uses an incompatible immutable snapshot; cancel it and retry from the original input"},
		{runtime.ErrPlanRevisionLimit, http.StatusConflict, "plan.revision_limit", "plan revision limit reached; approve the step or cancel the run"},
		{runtime.ErrRunResumeConflict, http.StatusConflict, "run.resume_conflict", "text run cannot be resumed from this checkpoint"},
		{runtime.ErrRunResumeIDConflict, http.StatusConflict, "run.resume_idempotency_conflict", "resume request id was already used with a different checkpoint"},
		{runtime.ErrRunRetireConflict, http.StatusConflict, "run.retire_conflict", "only suspended text runs can be retired"},
		{runtime.ErrTextRunAlreadyActive, http.StatusConflict, "run.already_active", "a text run is already active in this thread"},
		{runtime.ErrRunQueueConflict, http.StatusConflict, "run_queue.conflict", "run queue item changed or the client id was reused"},
		{runtime.ErrRunEnvironmentUnavailable, http.StatusUnprocessableEntity, "run.environment_unavailable", "environment or model is unavailable"},
		{runtime.ErrEnvironmentModelUnconfigured, http.StatusUnprocessableEntity, "environment.model_unconfigured", "environment has no configured model"},
		{runtime.ErrEnvironmentDefaultUnavailable, http.StatusUnprocessableEntity, "environment.default_model_unavailable", "environment default model is unavailable"},
		{runtime.ErrEnvironmentModelNotAccessible, http.StatusUnprocessableEntity, "environment.model_not_accessible", "model is not accessible to the current user"},
		{runtime.ErrEnvironmentModelNotAuthorized, http.StatusUnprocessableEntity, "environment.model_not_authorized", "model is not authorized by the environment"},
		{runtime.ErrExecutionModeNotAllowed, http.StatusUnprocessableEntity, "run.execution_mode_not_allowed", "execution mode is not allowed by the environment"},
		{runtime.ErrRunToolUnavailable, http.StatusUnprocessableEntity, "run.tool_unavailable", "one or more selected tools are unavailable"},
		{runtime.ErrRunSkillUnavailable, http.StatusUnprocessableEntity, "run.skill_unavailable", "one or more selected skills are unavailable"},
		{runtime.ErrInvalidInput, http.StatusBadRequest, "", "invalid text run control request"},
		{runtime.ErrNotFound, http.StatusNotFound, "", "run resource not found"},
	}
	for _, rule := range rules {
		if errors.Is(err, rule.target) {
			return rule
		}
	}
	if errors.Is(err, runtime.ErrUsageBalanceInsufficient) || errors.Is(err, runtime.ErrModelPricingRequired) {
		return runControlErrorMapping{status: http.StatusPaymentRequired, message: err.Error()}
	}
	return runControlErrorMapping{status: http.StatusInternalServerError, message: "run control request failed"}
}

func toRunStepResponse(s model.Step) map[string]interface{} {
	var dependsOn []string
	var expectedTools, resourceRefs []string
	_ = json.Unmarshal([]byte(s.DependsOnJSON), &dependsOn)
	_ = json.Unmarshal([]byte(s.ExpectedToolsJSON), &expectedTools)
	_ = json.Unmarshal([]byte(s.ResourceRefsJSON), &resourceRefs)
	return map[string]interface{}{valueStepIDF52B51EE: s.StepID, valueRunID1DA2F0B6: s.RunID, "parentStepID": s.ParentStepID, "planID": s.PlanID, "stepIndex": s.StepIndex, "attempt": s.Attempt, valueKind72883EFB: s.Kind, valueTitle48EAAEED: s.Title, "description": s.Description, valueStatus00E8FE8E: s.Status, "dependsOn": dependsOn, "approvalRequired": s.ApprovalRequired, "expectedTools": expectedTools, "resourceRefs": resourceRefs, "resultSummary": s.ResultSummary, valueStartedAt: nullableRunStepTime(s.StartedAt), valueEndedAt: s.EndedAt}
}

func nullableRunStepTime(value time.Time) interface{} {
	if value.IsZero() {
		return nil
	}
	return value
}
func runEventResponse(e model.Event) map[string]interface{} {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(e.PayloadJSON), &payload)
	if payload == nil {
		payload = map[string]interface{}{}
	}
	for key, value := range map[string]string{"summary": e.Summary, valueStatus00E8FE8E: e.Status, valueToolCallID5A0636DD: e.ToolCallID, "toolName": e.ToolName} {
		if _, exists := payload[key]; exists || strings.TrimSpace(value) == "" {
			continue
		}
		payload[key] = strings.TrimSpace(value)
	}
	result := map[string]interface{}{"schemaVersion": 1, "eventID": e.EventID, valueRunID1DA2F0B6: e.RunID, "actor": map[string]string{"tenantID": e.Actor.TenantID, "id": e.Actor.ActorID}, "thread": threadRefResponse(e.Thread), "seq": e.Seq, valueType9065E5F9: e.EventType, valueStepIDF52B51EE: e.StepID, "parentEventID": e.ParentEventID, "timestamp": e.StartedAt.UTC().Format(time.RFC3339Nano), "payload": canonicalizeKnownRuntimeRefs(payload)}
	if strings.TrimSpace(e.Projection.Kind) != "" && strings.TrimSpace(e.Projection.ID) != "" {
		result["projection"] = projectionRefResponse(e.Projection)
	}
	return result
}
func isRunStreamClosableStatus(status string) bool {
	switch status {
	case model.RunStatusCompleted, model.RunStatusFailed, model.RunStatusCancelled, model.RunStatusSuspended, model.RunStatusWaitingInput:
		return true
	}
	return false
}
func writeTextRunError(c *gin.Context, err error) {
	switch {
	case isEnvironmentModelError(err):
		code, message, _ := environmentModelError(err)
		writeError(c, http.StatusUnprocessableEntity, code, message)
	case errors.Is(err, runtime.ErrEnvironmentBindingNotAllowed):
		writeError(c, http.StatusUnprocessableEntity, "environment.binding_not_allowed", "environment is incompatible with this workspace")
	case errors.Is(err, runtime.ErrRunToolUnavailable), errors.Is(err, runtime.ErrRunToolIncompatible):
		writeError(c, http.StatusUnprocessableEntity, "run.tool_unavailable", "one or more selected tools are unavailable")
	case errors.Is(err, runtime.ErrRunSkillUnavailable):
		writeError(c, http.StatusUnprocessableEntity, "run.skill_unavailable", "one or more selected skills are unavailable")
	case errors.Is(err, runtime.ErrWorkspaceSourceStale):
		writeError(c, http.StatusConflict, "workspace.source_stale", "the frozen thread head or evidence changed; refresh the intent and try again")
	case errors.Is(err, runtime.ErrWorkspaceSourceTooLarge):
		writeError(c, http.StatusUnprocessableEntity, "workspace.source_too_large", "the thread source exceeds the configured context limit; select a smaller range")
	case errors.Is(err, runtime.ErrWorkspaceSourceCompacted):
		writeError(c, http.StatusUnprocessableEntity, "workspace.source_compacted", "the thread source contains a compacted gap; select a smaller range")
	case errors.Is(err, runtime.ErrUsageBalanceInsufficient), errors.Is(err, runtime.ErrModelPricingRequired):
		writeError(c, http.StatusPaymentRequired, "", err.Error())
	case errors.Is(err, runtime.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "", "invalid text run")
	case errors.Is(err, runtime.ErrTextRunIdempotencyConflict):
		writeError(c, http.StatusConflict, "run.idempotency_conflict", "clientRunID was already used for a different text run request")
	case errors.Is(err, runtime.ErrTextRunAlreadyActive):
		writeError(c, http.StatusConflict, "run.already_active", "a text run is already active in this thread")
	default:
		writeError(c, http.StatusInternalServerError, "", "start text run failed")
	}
}
