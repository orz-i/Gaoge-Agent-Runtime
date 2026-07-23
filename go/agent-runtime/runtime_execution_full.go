package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueTitle90A9E177 = "title"
)

const (
	valueResourceRefs325F3483 = "resourceRefs"
)

const (
	valueKey0159BCC1 = "key"
)

const (
	valueExpectedTools5667305F = "expectedTools"
)

const (
	valueDescription97BF8ECD = "description"
	valueRevision83764640    = "revision"
)

const (
	valueDependsOn21C7B963      = "dependsOn"
	valueItems52A20B8F          = "items"
	valueKindE5B2EFB3           = "kind"
	valuePerCall065DDC2C        = "per_call"
	valuePlanID320F2BB9         = "planID"
	valueProviderHostedF3C237B6 = "provider_hosted"
	valueSegmentKeyB3442EFB     = "segmentKey"
	valueStringCE0122C0         = "string"
	valueToolFailedFB145984     = "tool.failed"
	valueToolName4234B607       = "toolName"
	valueType5EE8C955           = "type"
)

const maxIdenticalDeterministicToolFailures = 2

var errRepeatedDeterministicWorkspaceToolFailure = errors.New("repeated deterministic workspace tool failure")

// errRequiredToolCallNotProduced is returned when the model emitted tool-protocol
// plain text (or no tool call) while a native tool call was required, and the
// remaining LLM budget is exhausted.
var errRequiredToolCallNotProduced = errors.New("required native tool call was not produced")

const (
	valueActive1F3964EF               = "active"
	valueAdditionalPropertiesC0172FF1 = "additionalProperties"
	valueAlwaysE613B9F9               = "always"
	valueArtifactReply1A2B3C4D        = "reply"
	valueAnswer89191F03               = "answer"
	valueApprovalRequired4260AFDA     = "approvalRequired"
	valueApproveFF07A766              = "approve"
	valueArrayE0D77340                = "array"
	valueAssistantCE8D479A            = "assistant"
	valueAuto60DC1905                 = "auto"
	valueBoolean12977957              = "boolean"
	valueCheckpointID9CD08C70         = "checkpointID"
	valueContinuationTypeDCB4DE9C     = "continuationType"
	valueDefault572954E1              = "default"
	valueDelta1F5E22EC                = "delta"
	valueErrorA8DE48C2                = "error"
	valueFeedback83F69355             = "feedback"
	valueFileA5BAA909                 = valueFileBE372696
	valueHighB19D217F                 = "high"
	valueImageB8C50585                = "image"
	valueInteractionIDA8491B1B        = "interactionID"
	valueLocalDispatchC00F9A8D        = "local_dispatch"
	valueMcpCE1A7808                  = "mcp"
	valueMessage69246916              = "message"
	valueMinLength8AE83CD1            = "minLength"
	valueMode06EC588F                 = "mode"
	valueName68D33990                 = "name"
	valueNeverF5C79F24                = "never"
	valueOrchestration7969B2CD        = "orchestration"
	valueOutputID7E64D749             = "outputID"
	valuePhaseA62799FA                = "phase"
	valueProperties3B4F6409           = "properties"
	valueProviderKind7144A4D9         = "providerKind"
	valueReasonB5B063AA               = "reason"
	valueRequest91B6AFF3              = "request"
	valueRequired26A4A382             = "required"
	valueRevise9EA811FD               = "revise"
	valueStatus327C4193               = "status"
	valueStepB959B536                 = "step"
	valueStepResumedF8C2AD47          = "step.resumed"
	valueStepID23C5C586               = "stepID"
	valueSteps82EB3C5C                = "steps"
	valueSuccess4D886D19              = "success"
	valueSummaryCE2A127F              = "summary"
	valueToolCCF14517                 = "tool"
	valueToolCompleted8D0A12FD        = "tool.completed"
	valueToolStartedB113F313          = "tool.started"
	valueToolCallID64CA70DB           = "toolCallID"
	valueToolKey560014C9              = "toolKey"
	valueUsageUpdatedABC8B0B2         = "usage.updated"
	valueUser19341906                 = "user"
	// model.tool_protocol_rejected records that the next LLM turn must use
	// tool_choice=required after a malformed protocol / missing-tool attempt.
	valueModelToolProtocolRejected = "model.tool_protocol_rejected"
	valueNextToolChoice            = "nextToolChoice"
	valueRetryCount                = "retryCount"
)

const (
	runControlAskUser                  = "run_ask_user"
	runControlPublishOutput            = "run_publish_output"
	runStrategyDirect                  = "direct"
	runStrategyPlanned                 = "planned"
	runReasonSimple                    = "simple_self_contained"
	runReasonPlanRequired              = "plan_required"
	runContinuationStartDirect         = "start_direct"
	runContinuationStartPlanning       = "start_planning"
	runContinuationContinuePlan        = "continue_plan"
	runContinuationReplan              = "replan"
	runContinuationExecuteApprovedTool = "execute_approved_tool"
	runContinuationRenewInteraction    = "renew_interaction"
)

const runPublicProgressInstruction = `When you call tools, you may include a brief user-facing progress update in the assistant text only when the goal, phase, or material result changes. Use at most two short sentences and 320 characters. State what you are doing or what changed; never reveal hidden reasoning, system instructions, credentials, or raw tool payloads. Do not add an update merely to announce every tool call.`

var (
	errPlanBudgetExceeded = errors.New("plan budget exceeded")
	errPlanInvalid        = errors.New("plan invalid")
)

type planPayload struct {
	Summary string         `json:"summary"`
	Steps   []planStepSpec `json:"steps"`
}

type planStepSpec struct {
	Key              string   `json:"key"`
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	DependsOn        []string `json:"dependsOn"`
	ApprovalRequired bool     `json:"approvalRequired"`
	ExpectedTools    []string `json:"expectedTools"`
	ResourceRefs     []string `json:"resourceRefs"`
}

type runUsage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64
	CacheWrite5mTokens, CacheWrite1hTokens, ReasoningTokens      int64
	ServerSideToolUsage                                          map[string]int64
	ServiceItems                                                 []ServiceUsageInput
	RawUsageJSON, UsageSpeed, UsageServiceTier, BillingRateClass string
}

type runFrozenToolCall struct {
	ToolKey      string          `json:"toolKey"`
	ToolName     string          `json:"toolName"`
	OriginalName string          `json:"originalName"`
	ToolCallID   string          `json:"toolCallID"`
	Arguments    json.RawMessage `json:"arguments"`
	Fingerprint  string          `json:"fingerprint"`
}

type runDurableToolResult struct {
	ToolCallID string `json:"toolCallID"`
	EventType  string `json:"eventType"`
}

type runFrozenInteraction struct {
	InteractionID  string          `json:"interactionID"`
	Type           string          `json:"type"`
	StepID         string          `json:"stepID"`
	ToolCallID     string          `json:"toolCallID,omitempty"`
	Request        json.RawMessage `json:"request"`
	ResponseSchema json.RawMessage `json:"responseSchema"`
	Fingerprint    string          `json:"fingerprint"`
}

type runContinuation struct {
	SemanticVersion   int                   `json:"semanticVersion"`
	SegmentKey        string                `json:"segmentKey"`
	Type              string                `json:"type"`
	TargetStatus      string                `json:"targetStatus"`
	InteractionID     string                `json:"interactionID,omitempty"`
	PlanID            string                `json:"planID,omitempty"`
	StepID            string                `json:"stepID,omitempty"`
	SourceStepID      string                `json:"sourceStepID,omitempty"`
	NextRevision      int                   `json:"nextRevision,omitempty"`
	Feedback          string                `json:"feedback,omitempty"`
	DurableToolResult *runDurableToolResult `json:"durableToolResult,omitempty"`
	FrozenToolCall    *runFrozenToolCall    `json:"frozenToolCall,omitempty"`
	FrozenInteraction *runFrozenInteraction `json:"frozenInteraction,omitempty"`
}

type runCheckpointManifest struct {
	SemanticVersion int    `json:"semanticVersion"`
	RunID           string `json:"runID"`
	State           struct {
		Continuation *runContinuation `json:"continuation"`
	} `json:"state"`
}

type runSegmentKeyContextKey struct{}

func runSegmentKey(ctx context.Context, run model.Run) string {
	if key, ok := ctx.Value(runSegmentKeyContextKey{}).(string); ok && strings.TrimSpace(key) != "" {
		return key
	}
	return run.RunID + ":start"
}

type runSegmentLifecycle struct {
	service     *Engine
	ctx         context.Context
	runID       string
	reservation *UsageBalanceReservation
	done        bool
}

func newRunSegmentLifecycle(service *Engine, ctx context.Context, runID string, reservation *UsageBalanceReservation) *runSegmentLifecycle {
	return &runSegmentLifecycle{service: service, ctx: context.WithoutCancel(ctx), runID: runID, reservation: reservation}
}

func (l *runSegmentLifecycle) close() {
	if l == nil || l.done {
		return
	}
	l.done = true
	l.service.FinishRunNotifications(l.runID)
}

func (l *runSegmentLifecycle) transfer() {
	if l != nil {
		l.done = true
	}
}

func (l *runSegmentLifecycle) fail(run model.Run, effective effectiveTextRunConfig, stepID string, cause error) {
	if l == nil || l.done {
		return
	}
	if billingErr := l.service.settleRunSegment(l.ctx, run, effective, l.reservation, nil, runUsage{}); billingErr != nil {
		cause = fmt.Errorf("text run segment billing failed: %w", billingErr)
	}
	l.service.failTextRun(l.ctx, run, stepID, cause)
	l.done = true
	l.service.FinishRunNotifications(l.runID)
}

func (l *runSegmentLifecycle) abort() {
	if l == nil || l.done {
		return
	}
	l.done = true
	_ = l.service.ReleaseRunUsageReservation(l.ctx, l.reservation, "Text Run segment 异常退出退回预扣")
	l.service.FinishRunNotifications(l.runID)
}

type ResolveRunInteractionInput struct {
	Actor           model.ActorRef
	RunID           string
	InteractionID   string
	ClientResolveID string
	Response        interface{}
}

type ResumeTextRunInput struct {
	Actor          model.ActorRef
	RunID          string
	CheckpointID   string
	ClientResumeID string
}

type PlanView struct {
	Current   *model.Plan
	Revisions []model.Plan
	Steps     []model.Step
}

func validActorRef(actor model.ActorRef) bool {
	return strings.TrimSpace(actor.TenantID) != "" && strings.TrimSpace(actor.ActorID) != ""
}

func boundedTextRunConfig(value, fallback, maximum int) int {
	if value == 0 {
		value = fallback
	}
	if value < 1 {
		return 1
	}
	if value > maximum {
		return maximum
	}
	return value
}

func uniqueRuntimeStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Engine) prepareTextRunMessagePair(ctx context.Context, start StartTextRunInput, effective effectiveTextRunConfig, runID string) (*ContextMessage, []ResolvedAttachment, *messageBranchState, error) {
	if err := s.validateRunFileCount(RuntimeInput{FileIDs: effective.FileIDs}); err != nil {
		return nil, nil, nil, err
	}
	branchReason := strings.TrimSpace(start.BranchReason)
	if branchReason == "" {
		branchReason = valueDefault572954E1
	}
	branch, err := s.resolveMessageBranch(ctx, start.Actor, start.Thread, start.ParentProjection, start.SourceProjection, branchReason)
	if err != nil {
		return nil, nil, nil, err
	}
	references := make([]model.ResourceRef, 0, len(effective.FileIDs))
	for _, fileID := range effective.FileIDs {
		references = append(references, model.ResourceRef{Kind: valueFileBE372696, ID: fileID})
	}
	if s.attachments == nil && len(references) > 0 {
		return nil, nil, nil, ErrAttachmentNotFound
	}
	var resolved []ResolvedAttachment
	if len(references) > 0 {
		result, resolveErr := s.attachments.ResolveAttachments(ctx, ResolveAttachmentsRequest{Actor: start.Actor, References: references})
		if resolveErr != nil {
			return nil, nil, nil, resolveErr
		}
		resolved = result.Attachments
	}
	resources := make([]model.ResourceRef, 0, len(resolved))
	for _, item := range resolved {
		resources = append(resources, item.Ref)
	}
	user := &ContextMessage{RunID: runID, Role: "user", ContentType: fallbackContentType(start.ContentType), Content: start.Goal, Status: "pending", Parent: branch.Parent, Source: branch.Source, Attachments: resources}
	return user, resolved, branch, nil
}

func (s *Engine) validateRunFileCount(input RuntimeInput) error {
	limit := s.cfg.Snapshot().Files.MaxAttachments
	if limit <= 0 {
		limit = 10
	}
	if len(input.FileIDs) > limit {
		return ErrTooManyAttachments
	}
	return nil
}

func (s *Engine) executePlanning(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, revision int, feedback string) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	s.generationStreams.register(runCtx, run.RunID, run.Actor, cancel)
	defer cancel()
	lifecycle := newRunSegmentLifecycle(s, runCtx, run.RunID, reservation)
	defer lifecycle.abort()
	if effective.SemanticVersion != RuntimeSnapshotVersion || effective.Strategy != TextRunStrategyPlanned {
		lifecycle.fail(run, effective, root.StepID, ErrRunSnapshotIncompatible)
		return
	}
	route, err := s.llmGateway.PrepareTextRoute(runCtx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID})
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	if err = s.ensurePlanningContext(runCtx, run); err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}

	payload, usage, route, err := s.generatePlan(runCtx, run, effective, route, revision, feedback)
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	bundle, err := buildPlanningBundle(run, root, effective, payload, revision)
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	saved, err := s.repo.CreatePlanningBundle(context.WithoutCancel(runCtx), run.RunID, model.RunStatusPreparing, &bundle.Plan, bundle.Steps, bundle.Interaction, bundle.Checkpoint, bundle.Events)
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	s.publishRunEvents(run.RunID, saved)
	s.logger.Info("text_runtime_plan_generated", String("run_id", run.RunID), Int("plan_steps", len(bundle.Steps)), Int(valueRevision83764640, revision))
	s.continueAfterPlanning(runCtx, run, root, effective, reservation, route, usage, bundle, lifecycle)
}

type planningBundle struct {
	Plan        model.Plan
	Steps       []model.Step
	Interaction *model.Interaction
	Checkpoint  *model.Checkpoint
	Events      []model.Event
}

func buildPlanningBundle(run model.Run, root model.Step, effective effectiveTextRunConfig, payload planPayload, revision int) (planningBundle, error) {
	planID := "plan_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	planJSON, err := json.Marshal(payload)
	if err != nil {
		return planningBundle{}, err
	}
	status := model.PlanProposed
	if effective.PlanApprovalMode == valueAuto60DC1905 {
		status = model.PlanApproved
	}
	plan := model.Plan{PlanID: planID, RunID: run.RunID, Revision: revision, Status: status, Goal: run.Goal, Summary: payload.Summary, PayloadJSON: string(planJSON)}
	steps, err := buildPlanningSteps(run, root, planID, payload.Steps)
	if err != nil {
		return planningBundle{}, err
	}
	bundle := planningBundle{Plan: plan, Steps: steps, Events: planningEvents(run, root, payload, planID, revision, steps)}
	if effective.PlanApprovalMode == valueRequired26A4A382 {
		return addRequiredPlanningApproval(bundle, run, root, effective, payload, revision)
	}
	bundle.Events = append(bundle.Events,
		newRunEvent(run, "plan.approved", root.StepID, "Plan auto-approved", map[string]interface{}{valuePlanID320F2BB9: planID, valueRevision83764640: revision, valueMode06EC588F: valueAuto60DC1905}, nil),
		newRunEvent(run, "run.status_changed", root.StepID, "Plan execution started", map[string]interface{}{valueStatus327C4193: model.RunStatusRunning}, nil),
	)
	bundle.Events[len(bundle.Events)-1].Status = model.RunStatusRunning
	return bundle, nil
}

func buildPlanningSteps(run model.Run, root model.Step, planID string, specs []planStepSpec) ([]model.Step, error) {
	stepByKey := make(map[string]string, len(specs))
	for _, spec := range specs {
		stepByKey[spec.Key] = "step_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	steps := make([]model.Step, 0, len(specs))
	for index, spec := range specs {
		step, err := buildPlanningStep(run, root, planID, index, spec, stepByKey)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func buildPlanningStep(run model.Run, root model.Step, planID string, index int, spec planStepSpec, stepByKey map[string]string) (model.Step, error) {
	depends := make([]string, 0, len(spec.DependsOn))
	for _, key := range spec.DependsOn {
		depends = append(depends, stepByKey[key])
	}
	dependsJSON, err := json.Marshal(depends)
	if err != nil {
		return model.Step{}, err
	}
	expectedToolsJSON, err := json.Marshal(spec.ExpectedTools)
	if err != nil {
		return model.Step{}, err
	}
	resourceRefsJSON, err := json.Marshal(spec.ResourceRefs)
	if err != nil {
		return model.Step{}, err
	}
	return model.Step{StepID: stepByKey[spec.Key], RunID: run.RunID, ParentStepID: root.StepID, PlanID: planID, StepIndex: index + 1, Attempt: 1, Kind: "plan_step", Title: spec.Title, Description: spec.Description, Status: model.RunStatusQueued, DependsOnJSON: string(dependsJSON), ExpectedToolsJSON: string(expectedToolsJSON), ResourceRefsJSON: string(resourceRefsJSON), ApprovalRequired: spec.ApprovalRequired}, nil
}

func planningEvents(run model.Run, root model.Step, payload planPayload, planID string, revision int, steps []model.Step) []model.Event {
	events := []model.Event{
		newRunEvent(run, "plan.created", root.StepID, payload.Summary, map[string]interface{}{valuePlanID320F2BB9: planID, valueRevision83764640: revision, valueSummaryCE2A127F: payload.Summary}, nil),
		newRunEvent(run, "plan.proposed", root.StepID, payload.Summary, map[string]interface{}{valuePlanID320F2BB9: planID, valueRevision83764640: revision}, nil),
	}
	for _, step := range steps {
		events = append(events, newRunEvent(run, "step.created", step.StepID, step.Title, runStepPayload(step), nil))
	}
	return events
}

func addRequiredPlanningApproval(bundle planningBundle, run model.Run, root model.Step, effective effectiveTextRunConfig, payload planPayload, revision int) (planningBundle, error) {
	bundle.Interaction = newRunInteraction(run, root.StepID, model.InteractionSubmitPlan, map[string]interface{}{valuePlanID320F2BB9: bundle.Plan.PlanID, valueRevision83764640: revision, valueSummaryCE2A127F: payload.Summary, valueSteps82EB3C5C: payload.Steps}, effective.InteractionTTLHours)
	checkpoint, err := newRunInteractionCheckpoint(run, bundle.Interaction, "plan_proposed")
	if err != nil {
		return planningBundle{}, err
	}
	bundle.Checkpoint = checkpoint
	bundle.Events = append(bundle.Events,
		newRunEvent(run, "checkpoint.created", root.StepID, "Plan checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueKindE5B2EFB3: checkpoint.Kind}, nil),
		newRunEvent(run, "interaction.created", root.StepID, "Plan approval required", map[string]interface{}{valueInteractionIDA8491B1B: bundle.Interaction.InteractionID, valueType5EE8C955: bundle.Interaction.Type, "expiresAt": bundle.Interaction.ExpiresAt}, nil),
		newRunEvent(run, "run.waiting_input", root.StepID, "Waiting for plan approval", map[string]interface{}{valueInteractionIDA8491B1B: bundle.Interaction.InteractionID, valueReasonB5B063AA: "plan_approval"}, nil),
	)
	return bundle, nil
}

func (s *Engine) continueAfterPlanning(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, route *LLMRoute, usage Usage, bundle planningBundle, lifecycle *runSegmentLifecycle) {
	if bundle.Interaction != nil {
		if err := s.settleRunSegment(context.WithoutCancel(ctx), run, effective, reservation, route, runUsageFromUsage(usage)); err != nil {
			s.failTextRun(context.WithoutCancel(ctx), run, root.StepID, err)
		}
		lifecycle.close()
		return
	}
	checkpoint := newRunContinuationCheckpoint(run, root.StepID, "plan_approved", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: runSegmentKey(ctx, run), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, PlanID: bundle.Plan.PlanID, StepID: root.StepID})
	event := newRunEvent(run, "checkpoint.created", root.StepID, "Approved plan checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueKindE5B2EFB3: checkpoint.Kind}, nil)
	events, err := s.repo.CreateRunCheckpointBundle(context.WithoutCancel(ctx), checkpoint, []model.Event{event})
	if err != nil {
		s.failTextRun(context.WithoutCancel(ctx), run, root.StepID, err)
		return
	}
	s.publishRunEvents(run.RunID, events)
	lifecycle.transfer()
	s.executePlan(ctx, run, root, effective, reservation, route, runUsageFromUsage(usage))
}

func (s *Engine) ensurePlanningContext(ctx context.Context, run model.Run) error {
	_, err := s.loadTextRunContextMessages(ctx, run)
	return err
}

func (s *Engine) generatePlan(ctx context.Context, run model.Run, effective effectiveTextRunConfig, route *LLMRoute, revision int, feedback string) (planPayload, Usage, *LLMRoute, error) {
	if s.llmGateway == nil {
		return planPayload{}, Usage{}, nil, ErrModelRouteNotConfigured
	}
	startedAt := time.Now()
	baseMessages, err := s.loadTextRunContextMessages(ctx, run)
	if err != nil {
		return planPayload{}, Usage{}, route, err
	}
	initial, err := s.generatePlanAttempt(ctx, run, effective, route, revision, feedback, false, baseMessages)
	if err != nil {
		return planPayload{}, initial.Usage, route, err
	}
	if initial.Validation == nil {
		s.logPlanCompleted(run.RunID, initial.Payload, startedAt, false)
		return initial.Payload, initial.Usage, route, nil
	}
	s.logger.Warn("text_runtime_plan_repairing", String("run_id", run.RunID), Int("initial_plan_steps", len(initial.Payload.Steps)), Error(initial.Validation))
	repairFeedback := strings.TrimSpace(feedback + "\n上一次计划不可执行：" + initial.Validation.Error() + "\n原始输出：" + initial.RawText)
	repaired, err := s.generatePlanAttempt(ctx, run, effective, route, revision, repairFeedback, true, baseMessages)
	if err != nil {
		if errors.Is(err, errPlanBudgetExceeded) {
			return planPayload{}, initial.Usage, route, normalizePlanFailure(initial.Validation)
		}
		return planPayload{}, mergeRunUsage(initial.Usage, repaired.Usage), route, err
	}
	merged := mergeRunUsage(initial.Usage, repaired.Usage)
	if repaired.Validation != nil {
		s.logger.Warn("text_runtime_plan_repair_failed", String("run_id", run.RunID), Int("plan_steps", len(repaired.Payload.Steps)), Error(repaired.Validation))
		return planPayload{}, merged, route, normalizePlanFailure(repaired.Validation)
	}
	s.logPlanCompleted(run.RunID, repaired.Payload, startedAt, true)
	return repaired.Payload, merged, route, nil
}

type planAttemptResult struct {
	Payload    planPayload
	Usage      Usage
	RawText    string
	Validation error
}

func (s *Engine) generatePlanAttempt(ctx context.Context, run model.Run, effective effectiveTextRunConfig, route *LLMRoute, revision int, feedback string, repair bool, baseMessages []Message) (planAttemptResult, error) {
	usedCalls, err := s.runLLMCallsUsed(ctx, run)
	if err != nil {
		return planAttemptResult{}, err
	}
	planMax, err := planMaxForNextPlanningCall(effective, usedCalls)
	if err != nil {
		if !repair {
			s.logger.Warn("text_runtime_plan_budget_rejected", String("run_id", run.RunID), Int("used_llm_calls", usedCalls), Int("max_llm_calls", effective.MaxLLMCalls), Error(err))
		}
		return planAttemptResult{}, err
	}
	request := buildPlannerRequest(run.RunID, run.Goal, effective, revision, feedback, repair, planMax, baseMessages)
	if err = s.ensureRunCallBudgetWithReserve(ctx, run, effective, true, 1); err != nil {
		return planAttemptResult{}, err
	}
	output, err := s.llmGateway.GenerateText(ctx, route, request)
	if err != nil {
		return planAttemptResult{}, err
	}
	phase := "planner"
	if repair {
		phase = "planner_repair"
	}
	if err = s.recordRunLLMUsage(context.WithoutCancel(ctx), run, phase, route, output); err != nil {
		return planAttemptResult{Usage: output.Usage}, err
	}
	payload, validationErr := parseAndValidatePlan(output.Text, planMax)
	if validationErr == nil {
		validationErr = validatePlanResourceScope(payload, effective)
	}
	return planAttemptResult{Payload: payload, Usage: output.Usage, RawText: output.Text, Validation: validationErr}, nil
}

func (s *Engine) logPlanCompleted(runID string, payload planPayload, startedAt time.Time, repaired bool) {
	s.logger.Info("text_runtime_plan_completed", String("run_id", runID), Int("plan_steps", len(payload.Steps)), Int64("latency_ms", time.Since(startedAt).Milliseconds()), Bool("repaired", repaired))
}

func textRunRequiresPlannedIntent(goal string) bool {
	normalized := strings.ToLower(strings.TrimSpace(goal))
	if containsAnyStrategyMarker(normalized, "先询问我", "询问我后", "等待我", "需要我确认", "让我审批", "ask me", "wait for my", "require my approval") {
		return true
	}
	// Explicit direct/no-plan instructions must not be inverted by the broader
	// planning keyword checks below. HITL markers above still take precedence.
	if runExplicitDirectIntent(normalized) {
		return false
	}
	if containsAnyStrategyMarker(normalized, "生成计划", "制定计划", "分步骤", "create a plan", "step by step") {
		return true
	}
	// Natural requests commonly insert modifiers between the planning verb and
	// noun (for example, "制定恰好两步计划").  Do not rely on exact phrases for
	// an execution constraint that must be enforced by the server.
	if strings.Contains(normalized, "计划") && containsAnyStrategyMarker(normalized,
		"制定", "生成", "创建", "拟定", "规划", "审批", "批准", "修改", "修订", "提交",
	) {
		return true
	}
	if containsAnyStrategyMarker(normalized, "两步", "三步", "多步", "逐步", "多个步骤", "两个阶段", "三个阶段", "分阶段", "多阶段") {
		return true
	}
	if strings.Contains(normalized, "plan") && containsAnyStrategyMarker(normalized,
		"create", "make", "draft", "generate", valueApproveFF07A766, valueRevise9EA811FD, "submit", "two-step", "multi-step", "multi-stage",
	) {
		return true
	}
	if containsAnyStrategyMarker(normalized, "two steps", "three steps", "multiple steps", "in stages") {
		return true
	}
	return false
}

func runExplicitDirectIntent(normalized string) bool {
	return containsAnyStrategyMarker(normalized,
		"不要制定", "不要生成", "不要创建", "不要规划", "无需制定", "无需生成", "不需要制定", "不需要生成", "不要分步骤", "无需分步骤",
		"do not create a plan", "don't create a plan", "without a plan", "do not plan", "answer directly", "reply directly",
	)
}

func containsAnyStrategyMarker(value string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func planMaxForNextPlanningCall(effective effectiveTextRunConfig, usedCalls int) (int, error) {
	remaining := effective.MaxLLMCalls - usedCalls
	if effective.MaxLLMCalls <= 0 || remaining < 2 {
		return 0, fmt.Errorf("%w: need a planning call and a final answer call; remaining=%d", errPlanBudgetExceeded, remaining)
	}
	planMax := remaining - 2
	if planMax > effective.PlanMaxSteps {
		planMax = effective.PlanMaxSteps
	}
	if planMax < 0 {
		planMax = 0
	}
	if planMax < 1 {
		return 0, fmt.Errorf("%w: planned execution needs routing, at least one step, and synthesis; remaining=%d", errPlanBudgetExceeded, remaining)
	}
	return planMax, nil
}

func buildPlannerRequest(runID, goal string, effective effectiveTextRunConfig, revision int, feedback string, repair bool, planMaxSteps int, contextMessages ...[]Message) GenerateInput {
	if planMaxSteps < 0 {
		planMaxSteps = 0
	}
	schema := map[string]interface{}{
		valueType5EE8C955: "object", valueAdditionalPropertiesC0172FF1: false,
		valueRequired26A4A382: []string{valueSummaryCE2A127F, valueSteps82EB3C5C},
		valueProperties3B4F6409: map[string]interface{}{
			valueSummaryCE2A127F: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0, valueMinLength8AE83CD1: 1},
			valueSteps82EB3C5C: map[string]interface{}{valueType5EE8C955: valueArrayE0D77340, "minItems": 1, "maxItems": planMaxSteps, valueItems52A20B8F: map[string]interface{}{
				valueType5EE8C955: "object", valueAdditionalPropertiesC0172FF1: false, valueRequired26A4A382: []string{valueKey0159BCC1, valueTitle90A9E177, valueDescription97BF8ECD, valueDependsOn21C7B963, valueApprovalRequired4260AFDA, valueExpectedTools5667305F, valueResourceRefs325F3483},
				valueProperties3B4F6409: map[string]interface{}{
					valueKey0159BCC1: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0, valueMinLength8AE83CD1: 1}, valueTitle90A9E177: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0, valueMinLength8AE83CD1: 1}, valueDescription97BF8ECD: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0, valueMinLength8AE83CD1: 1}, valueDependsOn21C7B963: map[string]interface{}{valueType5EE8C955: valueArrayE0D77340, valueItems52A20B8F: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0}}, valueApprovalRequired4260AFDA: map[string]interface{}{valueType5EE8C955: valueBoolean12977957}, valueExpectedTools5667305F: map[string]interface{}{valueType5EE8C955: valueArrayE0D77340, valueItems52A20B8F: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0}}, valueResourceRefs325F3483: map[string]interface{}{valueType5EE8C955: valueArrayE0D77340, valueItems52A20B8F: map[string]interface{}{valueType5EE8C955: valueStringCE0122C0}},
				},
			}},
		},
	}
	options := cloneRunOptions(effective.Options)
	options["response_format"] = map[string]interface{}{valueType5EE8C955: "json_schema", "json_schema": map[string]interface{}{valueName68D33990: "text_run_plan", "strict": true, "schema": schema}}
	allowedTools, allowedResources := runAllowedPlanScopes(effective)
	prompt := fmt.Sprintf("目标：%s\n生成第 %d 版执行计划，最多 %d 个步骤，依赖必须引用已定义 key 且为无环 DAG。每个步骤必须显式包含 key、title、description、dependsOn、approvalRequired、expectedTools、resourceRefs；空集合也必须写成 []，依赖字段只能叫 dependsOn，不能叫 dependencies。key、title、description 都必须是非空字符串；expectedTools 只能来自 %v，resourceRefs 只能来自 %v。步骤必须产生可复用的中间结果；不要把理解问题、建立表达式、格式化答案或输出最终结果拆成步骤。验证只有需要独立证据或工具时才是步骤，最终呈现由系统 synthesis 完成。\n只返回一个 JSON 对象，不要包含 mode、strategy、Markdown、解释或 reasoning。字段名必须严格使用英文。", goal, revision, planMaxSteps, allowedTools, allowedResources)
	if strings.TrimSpace(feedback) != "" {
		prompt += "\n用户反馈：" + feedback
	}
	if repair {
		prompt += "\n这是唯一一次修复或压缩机会。必须遵守当前步骤上限，只返回符合 schema 且可执行的 JSON。"
	}
	requestKind := "planner"
	if repair {
		requestKind = "planner-repair"
	}
	var baseMessages []Message
	if len(contextMessages) > 0 {
		baseMessages = contextMessages[0]
	}
	messages := cloneLLMMessages(baseMessages)
	messages = append(messages, Message{Role: valueUser19341906, Content: prompt})
	return GenerateInput{RequestID: fmt.Sprintf("%s:%s:%d", runID, requestKind, revision), Messages: messages, Instructions: strings.TrimSpace(effective.Instructions) + "\n你是统一文本 Runtime 的计划器。只输出结构化计划，不执行任务，不泄露内部推理。", DisableTools: true, Options: options}
}

func parseAndValidatePlan(raw string, maxSteps int) (planPayload, error) {
	raw = unwrapPlannerJSON(raw)
	var normalizeErr error
	raw, normalizeErr = normalizePlanCollectionFields(raw)
	if normalizeErr != nil {
		return planPayload{}, fmt.Errorf("normalize planner JSON: %w", normalizeErr)
	}
	var payload planPayload
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return payload, fmt.Errorf("invalid planner JSON: %w", err)
	}
	var shape struct {
		Steps []map[string]json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal([]byte(raw), &shape); err != nil {
		return payload, fmt.Errorf("inspect planner JSON: %w", err)
	}
	if err := validatePlanSummaryAndSize(raw, payload, maxSteps); err != nil {
		return payload, err
	}
	keys, err := normalizeAndValidatePlanSteps(payload.Steps, shape.Steps)
	if err != nil {
		return payload, err
	}
	if err = validatePlanDependencyGraph(payload.Steps, keys); err != nil {
		return payload, err
	}
	return payload, nil
}

func unwrapPlannerJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "```") {
		return raw
	}
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	return strings.TrimSuffix(strings.TrimSpace(raw), "```")
}

func validatePlanSummaryAndSize(raw string, payload planPayload, maxSteps int) error {
	if strings.TrimSpace(payload.Summary) == "" {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(raw), &fields)
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return withErrorMessage(errCategory377637EA92, fmt.Sprintf("plan summary is required (fields=%s, steps=%d)", strings.Join(keys, ","), len(payload.Steps)))
	}
	if len(payload.Steps) == 0 {
		return errCategory440497AF28
	}
	if len(payload.Steps) > maxSteps {
		return fmt.Errorf("%w: plan must contain at most %d steps; got %d", errPlanBudgetExceeded, maxSteps, len(payload.Steps))
	}
	return nil
}

func normalizeAndValidatePlanSteps(steps []planStepSpec, shapes []map[string]json.RawMessage) (map[string]struct{}, error) {
	keys := make(map[string]struct{}, len(steps))
	for index := range steps {
		if err := requirePlanStepFields(index, shapes[index]); err != nil {
			return nil, err
		}
		step := &steps[index]
		step.Key, step.Title, step.Description = strings.TrimSpace(step.Key), strings.TrimSpace(step.Title), strings.TrimSpace(step.Description)
		if step.Key == "" || step.Title == "" || step.Description == "" {
			return nil, withErrorMessage(errCategory51FBCA2215, fmt.Sprintf("step %d has empty required fields", index+1))
		}
		if _, exists := keys[step.Key]; exists {
			return nil, withErrorMessage(errCategory9303C78FF1, fmt.Sprintf("duplicate step key %q", step.Key))
		}
		keys[step.Key] = struct{}{}
		step.DependsOn = uniqueRuntimeStrings(step.DependsOn)
		step.ExpectedTools = uniqueRuntimeStrings(step.ExpectedTools)
		step.ResourceRefs = uniqueRuntimeStrings(step.ResourceRefs)
	}
	return keys, nil
}

func requirePlanStepFields(index int, shape map[string]json.RawMessage) error {
	for _, field := range []string{valueKey0159BCC1, valueTitle90A9E177, valueDescription97BF8ECD, valueDependsOn21C7B963, valueApprovalRequired4260AFDA, valueExpectedTools5667305F, valueResourceRefs325F3483} {
		if _, present := shape[field]; !present {
			return withErrorMessage(errCategory512384F21F, fmt.Sprintf("step %d is missing required field %q", index+1, field))
		}
	}
	return nil
}

func validatePlanDependencyGraph(steps []planStepSpec, keys map[string]struct{}) error {
	graph := make(map[string][]string, len(steps))
	for _, step := range steps {
		for _, dependency := range step.DependsOn {
			if _, exists := keys[dependency]; !exists || dependency == step.Key {
				return withErrorMessage(errCategory19575CB09B, fmt.Sprintf("step %q has invalid dependency %q", step.Key, dependency))
			}
		}
		graph[step.Key] = step.DependsOn
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	for key := range keys {
		if err := visitPlanDependency(key, graph, visiting, visited); err != nil {
			return err
		}
	}
	return nil
}

func visitPlanDependency(key string, graph map[string][]string, visiting, visited map[string]bool) error {
	if visiting[key] {
		return withErrorMessage(errCategoryEADA7D3E1E, fmt.Sprintf("plan dependency cycle at %q", key))
	}
	if visited[key] {
		return nil
	}
	visiting[key] = true
	for _, dependency := range graph[key] {
		if err := visitPlanDependency(dependency, graph, visiting, visited); err != nil {
			return err
		}
	}
	visiting[key], visited[key] = false, true
	return nil
}

// normalizePlanCollectionFields canonicalizes the narrow set of planner
// variations that have an unambiguous v2 representation. Omitted collection
// fields are empty, omitted approval is fail-safe true, and the observed
// dependencies alias maps to dependsOn. Ambiguous aliases and every other scalar
// contract field remain invalid so malformed plans still fail loudly.
func normalizePlanCollectionFields(raw string) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", err
	}
	changed, err := normalizePlanSummaryAlias(root)
	if err != nil {
		return "", err
	}
	stepsRaw, present := root[valueSteps82EB3C5C]
	if !present {
		if !changed {
			return raw, nil
		}
		normalized, marshalErr := json.Marshal(root)
		return string(normalized), marshalErr
	}
	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(stepsRaw, &steps); err != nil {
		return "", err
	}
	for i := range steps {
		changed = normalizePlanStepFields(steps[i]) || changed
	}
	if !changed {
		return raw, nil
	}
	normalizedSteps, err := json.Marshal(steps)
	if err != nil {
		return "", err
	}
	root[valueSteps82EB3C5C] = normalizedSteps
	normalized, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

func normalizePlanSummaryAlias(root map[string]json.RawMessage) (bool, error) {
	alias, hasAlias := root["planSummary"]
	if !hasAlias {
		return false, nil
	}
	var aliasValue string
	if err := json.Unmarshal(alias, &aliasValue); err != nil {
		return false, fmt.Errorf("planSummary must be a string: %w", err)
	}
	if summary, hasSummary := root["summary"]; hasSummary {
		var summaryValue string
		if err := json.Unmarshal(summary, &summaryValue); err != nil {
			return false, fmt.Errorf("summary must be a string: %w", err)
		}
		if strings.TrimSpace(summaryValue) != strings.TrimSpace(aliasValue) {
			return false, errors.New("summary conflicts with planSummary")
		}
	} else {
		root["summary"] = alias
	}
	delete(root, "planSummary")
	return true, nil
}

func normalizePlanStepFields(step map[string]json.RawMessage) bool {
	changed := false
	if _, hasDependsOn := step[valueDependsOn21C7B963]; !hasDependsOn {
		if dependencies, hasDependencies := step["dependencies"]; hasDependencies {
			step[valueDependsOn21C7B963] = dependencies
			delete(step, "dependencies")
			changed = true
		}
	}
	for _, field := range []string{valueDependsOn21C7B963, valueExpectedTools5667305F, valueResourceRefs325F3483} {
		if _, exists := step[field]; exists {
			continue
		}
		step[field] = json.RawMessage("[]")
		changed = true
	}
	if _, exists := step[valueApprovalRequired4260AFDA]; !exists {
		step[valueApprovalRequired4260AFDA] = json.RawMessage("true")
		changed = true
	}
	return changed
}

func normalizePlanFailure(err error) error {
	if errors.Is(err, errPlanBudgetExceeded) || errors.Is(err, errPlanInvalid) {
		return err
	}
	return fmt.Errorf("%w: %w", errPlanInvalid, err)
}

func cloneRunOptions(source map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(source)+2)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func runStepPayload(step model.Step) map[string]interface{} {
	var dependencies []string
	var expectedTools, resourceRefs []string
	_ = json.Unmarshal([]byte(step.DependsOnJSON), &dependencies)
	_ = json.Unmarshal([]byte(step.ExpectedToolsJSON), &expectedTools)
	_ = json.Unmarshal([]byte(step.ResourceRefsJSON), &resourceRefs)
	return map[string]interface{}{valueStepID23C5C586: step.StepID, valuePlanID320F2BB9: step.PlanID, "index": step.StepIndex, valueTitle90A9E177: step.Title, valueDescription97BF8ECD: step.Description, valueDependsOn21C7B963: dependencies, valueApprovalRequired4260AFDA: step.ApprovalRequired, valueExpectedTools5667305F: expectedTools, valueResourceRefs325F3483: resourceRefs}
}

func runAllowedPlanScopes(effective effectiveTextRunConfig) ([]string, []string) {
	tools := make([]string, 0, len(effective.ToolPolicies))
	for _, tool := range effective.ToolPolicies {
		tools = append(tools, tool.ModelName)
	}
	resources := append([]string(nil), effective.FileIDs...)
	resources = append(resources, effective.OutputIDs...)
	resources = append(resources, effective.EvidenceIDs...)
	return uniqueRuntimeStrings(tools), uniqueRuntimeStrings(resources)
}

func validatePlanResourceScope(payload planPayload, effective effectiveTextRunConfig) error {
	tools, resources := runAllowedPlanScopes(effective)
	toolSet, resourceSet := map[string]struct{}{}, map[string]struct{}{}
	for _, value := range tools {
		toolSet[value] = struct{}{}
	}
	for _, value := range resources {
		resourceSet[value] = struct{}{}
	}
	for _, step := range payload.Steps {
		for _, value := range step.ExpectedTools {
			if _, ok := toolSet[value]; !ok {
				return withErrorMessage(errCategory5687A8F7EC, fmt.Sprintf("step %q references unavailable tool %q", step.Key, value))
			}
		}
		for _, value := range step.ResourceRefs {
			if _, ok := resourceSet[value]; !ok {
				return withErrorMessage(errCategoryFCA4993A9B, fmt.Sprintf("step %q references unavailable resource %q", step.Key, value))
			}
		}
	}
	return nil
}

func newRunCheckpoint(run model.Run, stepID, kind string, state interface{}) *model.Checkpoint {
	encoded, err := json.Marshal(map[string]interface{}{
		"semanticVersion": RuntimeSnapshotVersion,
		"runID":           run.RunID,
		"state":           state,
	})
	if err != nil {
		encoded = []byte(fmt.Sprintf(`{"semanticVersion":%d,"runID":"","state":{"error":"checkpoint_state_encoding_failed"}}`, RuntimeSnapshotVersion))
	}
	return &model.Checkpoint{CheckpointID: "checkpoint_" + strings.ReplaceAll(uuid.NewString(), "-", ""), RunID: run.RunID, StepID: stepID, ContextHash: fmt.Sprintf("%x", sha256.Sum256([]byte(run.RunID+":"+stepID+":"+kind))), ManifestHash: fmt.Sprintf("%x", sha256.Sum256(encoded)), Kind: kind, Status: model.CheckpointReady, ResumeStateJSON: string(encoded)}
}

func newRunContinuationCheckpoint(run model.Run, stepID, kind string, continuation runContinuation) *model.Checkpoint {
	checkpoint := newRunCheckpoint(run, stepID, kind, map[string]interface{}{"continuation": continuation})
	return checkpoint
}

func deterministicRunCheckpointID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "checkpoint_" + fmt.Sprintf("%x", digest[:16])
}

func decodeRunContinuation(checkpoint model.Checkpoint) (runContinuation, error) {
	if checkpoint.ManifestHash == "" {
		return runContinuation{}, ErrRunSnapshotIncompatible
	}
	digest := sha256.Sum256([]byte(checkpoint.ResumeStateJSON))
	if checkpoint.ManifestHash != fmt.Sprintf("%x", digest) {
		return runContinuation{}, ErrRunSnapshotIncompatible
	}
	var manifest runCheckpointManifest
	if err := json.Unmarshal([]byte(checkpoint.ResumeStateJSON), &manifest); err != nil || manifest.SemanticVersion != RuntimeSnapshotVersion || manifest.RunID != checkpoint.RunID || manifest.State.Continuation == nil {
		return runContinuation{}, ErrRunSnapshotIncompatible
	}
	continuation := *manifest.State.Continuation
	if err := validateRunContinuation(continuation); err != nil {
		return runContinuation{}, err
	}
	return continuation, nil
}

func validateRunContinuation(continuation runContinuation) error {
	if !allRunContinuationConditions(continuation.SemanticVersion == RuntimeSnapshotVersion, strings.TrimSpace(continuation.SegmentKey) != "", strings.TrimSpace(continuation.StepID) != "") {
		return ErrRunSnapshotIncompatible
	}
	if invalidContinuationFrozenInteraction(continuation) {
		return ErrRunSnapshotIncompatible
	}
	switch continuation.Type {
	case runContinuationStartDirect:
		return validateStartDirectContinuation(continuation)
	case runContinuationStartPlanning:
		return validateStartPlanningContinuation(continuation)
	case runContinuationContinuePlan:
		return validateContinuePlanContinuation(continuation)
	case runContinuationReplan:
		return validateReplanContinuation(continuation)
	case runContinuationExecuteApprovedTool:
		return validateApprovedToolContinuation(continuation)
	case runContinuationRenewInteraction:
		return validateRenewInteractionContinuation(continuation)
	default:
		return ErrRunSnapshotIncompatible
	}
}

func invalidContinuationFrozenInteraction(value runContinuation) bool {
	return value.Type != runContinuationRenewInteraction && value.FrozenInteraction != nil
}

func allRunContinuationConditions(values ...bool) bool {
	for _, value := range values {
		if !value {
			return false
		}
	}
	return true
}

func continuationValidationResult(valid bool) error {
	if !valid {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func validateStartDirectContinuation(value runContinuation) error {
	return continuationValidationResult(allRunContinuationConditions(value.TargetStatus == model.RunStatusRunning, value.FrozenToolCall == nil, value.DurableToolResult == nil))
}

func validateStartPlanningContinuation(value runContinuation) error {
	return continuationValidationResult(allRunContinuationConditions(value.TargetStatus == model.RunStatusPreparing, value.NextRevision >= 1, value.FrozenToolCall == nil, value.DurableToolResult == nil))
}

func validateContinuePlanContinuation(value runContinuation) error {
	if !allRunContinuationConditions(value.TargetStatus == model.RunStatusRunning, value.FrozenToolCall == nil) {
		return ErrRunSnapshotIncompatible
	}
	result := value.DurableToolResult
	if result == nil {
		return nil
	}
	validEvent := result.EventType == valueToolCompleted8D0A12FD || result.EventType == valueToolFailedFB145984
	return continuationValidationResult(strings.TrimSpace(result.ToolCallID) != "" && validEvent)
}

func validateReplanContinuation(value runContinuation) error {
	valid := allRunContinuationConditions(value.TargetStatus == model.RunStatusPreparing, strings.TrimSpace(value.InteractionID) != "", strings.TrimSpace(value.PlanID) != "", strings.TrimSpace(value.SourceStepID) != "", value.NextRevision >= 2, strings.TrimSpace(value.Feedback) != "", len([]rune(value.Feedback)) <= 4000, value.FrozenToolCall == nil, value.DurableToolResult == nil)
	return continuationValidationResult(valid)
}

func validateApprovedToolContinuation(value runContinuation) error {
	call := value.FrozenToolCall
	if call == nil {
		return ErrRunSnapshotIncompatible
	}
	valid := allRunContinuationConditions(value.TargetStatus == model.RunStatusRunning, strings.TrimSpace(value.InteractionID) != "", strings.TrimSpace(call.ToolKey) != "", strings.TrimSpace(call.ToolName) != "", strings.TrimSpace(call.OriginalName) != "", strings.TrimSpace(call.ToolCallID) != "", len(call.Arguments) > 0, json.Valid(call.Arguments), strings.TrimSpace(call.Fingerprint) != "", value.DurableToolResult == nil)
	actual := fmt.Sprintf("%x", sha256.Sum256([]byte(call.ToolKey+"\x00"+call.ToolName+"\x00"+canonicalRunJSON(call.Arguments))))
	return continuationValidationResult(valid && actual == call.Fingerprint)
}

func validateRenewInteractionContinuation(value runContinuation) error {
	interaction := value.FrozenInteraction
	if interaction == nil {
		return ErrRunSnapshotIncompatible
	}
	valid := allRunContinuationConditions(value.TargetStatus == model.RunStatusWaitingInput, strings.TrimSpace(value.InteractionID) != "", interaction.InteractionID == value.InteractionID, strings.TrimSpace(interaction.Type) != "", strings.TrimSpace(interaction.StepID) != "", interaction.StepID == value.StepID, len(interaction.Request) > 0, json.Valid(interaction.Request), len(interaction.ResponseSchema) > 0, json.Valid(interaction.ResponseSchema), strings.TrimSpace(interaction.Fingerprint) != "", value.FrozenToolCall == nil, value.DurableToolResult == nil)
	return continuationValidationResult(valid && runInteractionSnapshotFingerprint(*interaction) == interaction.Fingerprint)
}

func (s *Engine) launchRunContinuation(task func()) {
	if s == nil || s.continuationScheduler == nil {
		return
	}
	s.lifecycleMu.Lock()
	closed := s.closed
	s.lifecycleMu.Unlock()
	if closed {
		return
	}
	if err := s.continuationScheduler.Schedule(context.Background(), func(context.Context) { task() }); err != nil && s.logger != nil {
		s.logger.Error("schedule_run_continuation_failed", Error(err))
	}
}

func newRunInteraction(run model.Run, stepID, kind string, request interface{}, ttlHours int) *model.Interaction {
	now := time.Now()
	expiresAt := now.Add(time.Duration(ttlHours) * time.Hour)
	encoded, err := json.Marshal(request)
	if err != nil {
		encoded = []byte(`{"error":"interaction_request_encoding_failed"}`)
	}
	return &model.Interaction{InteractionID: "interaction_" + strings.ReplaceAll(uuid.NewString(), "-", ""), RunID: run.RunID, StepID: stepID, Type: kind, Status: model.InteractionPending, RequestPayloadJSON: string(encoded), ResponseSchemaJSON: runInteractionResponseSchema(kind), RequestedAt: now, ExpiresAt: &expiresAt}
}

func newRunInteractionCheckpoint(run model.Run, interaction *model.Interaction, kind string) (*model.Checkpoint, error) {
	if interaction == nil || strings.TrimSpace(interaction.InteractionID) == "" || !json.Valid([]byte(interaction.RequestPayloadJSON)) || !json.Valid([]byte(interaction.ResponseSchemaJSON)) {
		return nil, ErrRunSnapshotIncompatible
	}
	frozen := &runFrozenInteraction{InteractionID: interaction.InteractionID, Type: interaction.Type, StepID: interaction.StepID, ToolCallID: interaction.ToolCallID, Request: json.RawMessage(interaction.RequestPayloadJSON), ResponseSchema: json.RawMessage(interaction.ResponseSchemaJSON)}
	frozen.Fingerprint = runInteractionSnapshotFingerprint(*frozen)
	continuation := runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: run.RunID + ":interaction:" + interaction.InteractionID, Type: runContinuationRenewInteraction, TargetStatus: model.RunStatusWaitingInput, InteractionID: interaction.InteractionID, StepID: interaction.StepID, FrozenInteraction: frozen}
	if err := validateRunContinuation(continuation); err != nil {
		return nil, err
	}
	checkpoint := newRunContinuationCheckpoint(run, interaction.StepID, kind, continuation)
	checkpoint.ToolCallID = interaction.ToolCallID
	return checkpoint, nil
}

func runInteractionSnapshotFingerprint(interaction runFrozenInteraction) string {
	value := strings.Join([]string{interaction.InteractionID, interaction.Type, interaction.StepID, interaction.ToolCallID, canonicalRunJSON(interaction.Request), canonicalRunJSON(interaction.ResponseSchema)}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func runInteractionResponseSchema(kind string) string {
	switch kind {
	case model.InteractionSubmitPlan:
		return `{"type":"object","required":["action"],"additionalProperties":false,"properties":{"action":{"type":"string","enum":["approve","revise","reject"]},"feedback":{"type":"string","maxLength":4000}}}`
	case model.InteractionApproveTool, model.InteractionApproveToolSet:
		return `{"type":"object","required":["approved"],"additionalProperties":false,"properties":{"approved":{"type":"boolean"}}}`
	case model.InteractionApproveStep:
		return `{"type":"object","required":["action"],"additionalProperties":false,"properties":{"action":{"type":"string","enum":["approve","revise"]},"feedback":{"type":"string","maxLength":4000}}}`
	default:
		return `{"type":"object","required":["answer"],"additionalProperties":false,"properties":{"answer":{"type":"string","minLength":1,"maxLength":20000}}}`
	}
}

func (s *Engine) executeDirectStrategy(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, _ *LLMRoute, initialUsage runUsage) {
	lifecycle := newRunSegmentLifecycle(s, ctx, run.RunID, reservation)
	defer lifecycle.abort()
	startedAt := time.Now()
	if effective.SemanticVersion != RuntimeSnapshotVersion || effective.Strategy != TextRunStrategyDirect {
		lifecycle.fail(run, effective, root.StepID, ErrRunSnapshotIncompatible)
		return
	}
	contextMessages, err := s.loadTextRunContextMessages(ctx, run)
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	finalText, route, usage, waiting, err := s.generateDirectRunAnswer(ctx, run, root, effective, contextMessages, initialUsage)
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	if err = s.settleRunSegment(context.WithoutCancel(ctx), run, effective, reservation, route, usage); err != nil {
		s.failTextRun(context.WithoutCancel(ctx), run, root.StepID, err)
		lifecycle.close()
		return
	}
	if waiting {
		lifecycle.close()
		return
	}
	if err = s.completeTextRun(context.WithoutCancel(ctx), run, root.StepID, effective, finalText); err != nil {
		s.failTextRun(context.WithoutCancel(ctx), run, root.StepID, err)
		s.logger.Error("finalize_text_runtime_direct_success_failed", String("run_id", run.RunID), Error(err))
		lifecycle.close()
		return
	}
	s.logger.Info("text_runtime_direct_completed", String("run_id", run.RunID), Int64("latency_ms", time.Since(startedAt).Milliseconds()))
	lifecycle.close()
}

func (s *Engine) generateDirectRunAnswer(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, contextMessages []Message, usage runUsage) (string, *LLMRoute, runUsage, bool, error) {
	if !hasLocalRunTools(effective) {
		finalUsage, route, finalText, err := s.streamRunAnswer(ctx, run, root.StepID, effective, "direct", "direct", contextMessages, strings.TrimSpace(effective.Instructions)+"\n直接、准确地回答用户目标。不要生成计划。", true)
		return finalText, route, addRunUsage(usage, runUsageFromUsage(finalUsage)), false, err
	}
	tools, err := s.resolveRunTools(ctx, run.Actor, effective)
	if err != nil {
		return "", nil, usage, false, err
	}
	finalText, stepUsage, waiting, err := s.executeRunStep(ctx, run, root, effective, tools, contextMessages, nil)
	usage = addRunUsage(usage, stepUsage)
	if err == nil && !waiting && strings.TrimSpace(finalText) != "" {
		err = s.appendRunEvent(context.WithoutCancel(ctx), &run, "message.delta", root.StepID, "", map[string]interface{}{valueDelta1F5E22EC: finalText}, nil)
	}
	return finalText, nil, usage, waiting, err
}

func (s *Engine) executePlan(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, initialRoute *LLMRoute, initialUsage runUsage) {
	lifecycle := newRunSegmentLifecycle(s, ctx, run.RunID, reservation)
	defer lifecycle.abort()
	state, err := s.preparePlanExecution(ctx, run, effective, initialUsage)
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	stepResult := s.executePreparedPlanSteps(ctx, run, effective, &state)
	if stepResult.waiting {
		if settleErr := s.settleRunSegment(context.WithoutCancel(ctx), run, effective, reservation, initialRoute, state.usage); settleErr != nil {
			s.failTextRun(context.WithoutCancel(ctx), run, stepResult.stepID, settleErr)
		}
		lifecycle.close()
		return
	}
	if stepResult.err != nil {
		lifecycle.fail(run, effective, stepResult.stepID, stepResult.err)
		return
	}
	finalUsage, route, finalText, err := s.synthesizeRun(ctx, run, root.StepID, effective, state.contextMessages, state.summaries)
	state.usage = addRunUsage(state.usage, runUsageFromUsage(finalUsage))
	if err != nil {
		lifecycle.fail(run, effective, root.StepID, err)
		return
	}
	if err = s.settleRunSegment(context.WithoutCancel(ctx), run, effective, reservation, route, state.usage); err != nil {
		s.failTextRun(context.WithoutCancel(ctx), run, root.StepID, err)
		lifecycle.close()
		return
	}
	if err = s.completeTextRun(context.WithoutCancel(ctx), run, root.StepID, effective, finalText); err != nil {
		s.failTextRun(context.WithoutCancel(ctx), run, root.StepID, err)
		s.logger.Error("finalize_text_runtime_success_failed", String("run_id", run.RunID), Error(err))
	}
	lifecycle.close()
}

type planExecutionState struct {
	contextMessages []Message
	steps           []model.Step
	tools           map[string]ResolvedTool
	usage           runUsage
	summaries       []string
}

type planStepExecutionResult struct {
	stepID  string
	waiting bool
	err     error
}

func (s *Engine) preparePlanExecution(ctx context.Context, run model.Run, effective effectiveTextRunConfig, initialUsage runUsage) (planExecutionState, error) {
	messages, err := s.loadTextRunContextMessages(ctx, run)
	if err != nil {
		return planExecutionState{}, err
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return planExecutionState{}, err
	}
	steps, err = topologicallySortRunSteps(activePlanSteps(steps))
	if err != nil {
		return planExecutionState{}, err
	}
	tools, err := s.resolveRunTools(ctx, run.Actor, effective)
	if err != nil {
		return planExecutionState{}, err
	}
	summaries := s.planExecutionSummaries(ctx, run, effective, len(steps))
	return planExecutionState{contextMessages: messages, steps: steps, tools: tools, usage: initialUsage, summaries: summaries}, nil
}

func activePlanSteps(steps []model.Step) []model.Step {
	result := make([]model.Step, 0, len(steps))
	for _, step := range steps {
		if step.PlanID != "" && step.Status != "skipped" {
			result = append(result, step)
		}
	}
	return result
}

func (s *Engine) planExecutionSummaries(ctx context.Context, run model.Run, effective effectiveTextRunConfig, capacity int) []string {
	summaries := make([]string, 0, capacity)
	outputIDs := make([]string, 0, len(effective.OutputRefs))
	for _, ref := range effective.OutputRefs {
		outputIDs = append(outputIDs, ref.OutputID)
	}
	if outputs, err := s.repo.GetOutputsByIDs(ctx, run.Actor, outputIDs); err == nil {
		for _, output := range outputs {
			summaries = append(summaries, "输入 Output "+output.Title+": "+output.Summary)
		}
	}
	if interactions, err := s.repo.ListRunInteractions(ctx, run.Actor, run.RunID); err == nil {
		summaries = append(summaries, resolvedInteractionSummaries(interactions)...)
	}
	return summaries
}

func resolvedInteractionSummaries(interactions []model.Interaction) []string {
	result := make([]string, 0)
	for _, interaction := range interactions {
		if interaction.Status != model.InteractionResolved || strings.TrimSpace(interaction.ResponseJSON) == "" {
			continue
		}
		if interaction.Type == model.InteractionAskUser {
			result = append(result, "用户补充输入: "+interaction.ResponseJSON)
		}
		if interaction.Type == model.InteractionApproveTool {
			result = append(result, "工具审批结果: "+interaction.ResponseJSON)
		}
	}
	return result
}

func (s *Engine) executePreparedPlanSteps(ctx context.Context, run model.Run, effective effectiveTextRunConfig, state *planExecutionState) planStepExecutionResult {
	for _, step := range state.steps {
		result := s.executePreparedPlanStep(ctx, run, step, effective, state)
		if result.waiting || result.err != nil {
			return result
		}
	}
	return planStepExecutionResult{}
}

func (s *Engine) executePreparedPlanStep(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, state *planExecutionState) planStepExecutionResult {
	if step.Status == model.RunStatusCompleted {
		state.summaries = append(state.summaries, step.Title+": "+step.ResultSummary)
		return planStepExecutionResult{}
	}
	execute, appendStarted := runStepExecutionMode(step, run.CurrentStepID)
	if !execute {
		return planStepExecutionResult{}
	}
	waiting, err := s.ensureRunStepApproval(ctx, run, step, effective)
	if err != nil || waiting {
		return planStepExecutionResult{stepID: step.StepID, waiting: waiting, err: err}
	}
	if appendStarted {
		if err = s.appendRunStepStarted(ctx, run, step); err != nil {
			return planStepExecutionResult{stepID: step.StepID, err: err}
		}
	}
	text, stepUsage, waiting, err := s.executeRunStep(ctx, run, step, effective, state.tools, state.contextMessages, state.summaries)
	state.usage = addRunUsage(state.usage, stepUsage)
	if err != nil || waiting {
		return planStepExecutionResult{stepID: step.StepID, waiting: waiting, err: err}
	}
	state.summaries = append(state.summaries, step.Title+": "+text)
	event := newRunEvent(run, "step.completed", step.StepID, text, map[string]interface{}{"resultSummary": text}, nil)
	return planStepExecutionResult{stepID: step.StepID, err: s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{event})}
}

func (s *Engine) appendRunStepStarted(ctx context.Context, run model.Run, step model.Step) error {
	started := newRunEvent(run, "step.started", step.StepID, step.Title, runStepPayload(step), nil)
	if step.Status == model.RunStatusSuspended {
		started.EventType = valueStepResumedF8C2AD47
	}
	return s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started})
}

func (s *Engine) ensureRunStepApproval(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig) (bool, error) {
	if !step.ApprovalRequired {
		return false, nil
	}
	interactions, err := s.repo.ListRunInteractions(ctx, run.Actor, run.RunID)
	if err != nil {
		return false, err
	}
	if found, waiting := runStepApprovalState(interactions, step.StepID); found {
		return waiting, nil
	}
	request := runStepPayload(step)
	interaction := newRunInteraction(run, step.StepID, model.InteractionApproveStep, request, effective.InteractionTTLHours)
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "approve_step")
	if err != nil {
		return false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", step.StepID, "Step approval checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueKindE5B2EFB3: checkpoint.Kind}, nil),
		newRunEvent(run, "interaction.created", step.StepID, "Step approval required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, valueStepB959B536: request}, nil),
		newRunEvent(run, "step.waiting_input", step.StepID, "Waiting for step approval", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil),
		newRunEvent(run, "run.waiting_input", step.StepID, "Waiting for step approval", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "approve_step"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, model.RunStatusRunning, interaction, checkpoint, events)
	if err != nil {
		return false, err
	}
	s.publishRunEvents(run.RunID, saved)
	return true, nil
}

func runStepApprovalState(interactions []model.Interaction, stepID string) (bool, bool) {
	for _, interaction := range interactions {
		if interaction.Type != model.InteractionApproveStep || interaction.StepID != stepID {
			continue
		}
		if interaction.Status == model.InteractionPending {
			return true, true
		}
		var response struct {
			Action string `json:"action"`
		}
		if interaction.Status == model.InteractionResolved && json.Unmarshal([]byte(interaction.ResponseJSON), &response) == nil && response.Action == valueApproveFF07A766 {
			return true, false
		}
	}
	return false, false
}

func runStepExecutionMode(step model.Step, currentStepID string) (execute bool, appendStarted bool) {
	switch step.Status {
	case model.RunStatusQueued, model.RunStatusWaitingInput, model.RunStatusSuspended:
		return true, true
	case model.RunStatusRunning:
		return step.StepID == currentStepID, false
	default:
		return false, false
	}
}

func topologicallySortRunSteps(steps []model.Step) ([]model.Step, error) {
	byID, indegree, dependents, err := buildRunStepDependencyGraph(steps)
	if err != nil {
		return nil, err
	}
	ready := readyRunSteps(steps, indegree)
	ordered := make([]model.Step, 0, len(steps))
	for len(ready) > 0 {
		step := ready[0]
		ready = ready[1:]
		ordered = append(ordered, step)
		ready = releaseDependentRunSteps(ready, step.StepID, byID, indegree, dependents)
	}
	if len(ordered) != len(steps) {
		return nil, errCategoryCD625F2DD4
	}
	return ordered, nil
}

func buildRunStepDependencyGraph(steps []model.Step) (map[string]model.Step, map[string]int, map[string][]string, error) {
	byID := make(map[string]model.Step, len(steps))
	indegree := make(map[string]int, len(steps))
	dependents := make(map[string][]string, len(steps))
	for _, step := range steps {
		byID[step.StepID] = step
		indegree[step.StepID] = 0
	}
	for _, step := range steps {
		var dependencies []string
		if strings.TrimSpace(step.DependsOnJSON) != "" {
			if err := json.Unmarshal([]byte(step.DependsOnJSON), &dependencies); err != nil {
				return nil, nil, nil, fmt.Errorf("step %s has invalid dependencies: %w", step.StepID, err)
			}
		}
		for _, dependency := range dependencies {
			if _, exists := byID[dependency]; !exists {
				return nil, nil, nil, withErrorMessage(errCategoryF588B464C3, fmt.Sprintf("step %s depends on unknown step %s", step.StepID, dependency))
			}
			indegree[step.StepID]++
			dependents[dependency] = append(dependents[dependency], step.StepID)
		}
	}
	return byID, indegree, dependents, nil
}

func readyRunSteps(steps []model.Step, indegree map[string]int) []model.Step {
	ready := make([]model.Step, 0, len(steps))
	for _, step := range steps {
		if indegree[step.StepID] == 0 {
			ready = append(ready, step)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].StepIndex < ready[j].StepIndex })
	return ready
}

func releaseDependentRunSteps(ready []model.Step, stepID string, byID map[string]model.Step, indegree map[string]int, dependents map[string][]string) []model.Step {
	for _, dependentID := range dependents[stepID] {
		indegree[dependentID]--
		if indegree[dependentID] == 0 {
			ready = append(ready, byID[dependentID])
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].StepIndex < ready[j].StepIndex })
	return ready
}

func (s *Engine) resolveRunTools(_ context.Context, _ model.ActorRef, effective effectiveTextRunConfig) (map[string]ResolvedTool, error) {
	result := map[string]ResolvedTool{}
	if effective.SemanticVersion != RuntimeSnapshotVersion {
		return nil, ErrRunSnapshotIncompatible
	}
	if len(effective.ToolKeys) == 0 {
		return result, nil
	}
	wanted := make(map[string]struct{}, len(effective.ToolKeys))
	for _, key := range effective.ToolKeys {
		wanted[key] = struct{}{}
	}
	for _, policy := range effective.ToolPolicies {
		if _, ok := wanted[policy.ToolKey]; !ok {
			continue
		}
		if err := addResolvedRunTool(result, policy); err != nil {
			return nil, err
		}
		delete(wanted, policy.ToolKey)
	}
	if len(wanted) != 0 {
		return nil, ErrRunSnapshotIncompatible
	}
	return result, nil
}

func addResolvedRunTool(result map[string]ResolvedTool, policy effectiveRunToolPolicy) error {
	if !validRunToolPolicySnapshot(policy) {
		return ErrRunSnapshotIncompatible
	}
	if policy.ExecutionMode == valueLocalDispatchC00F9A8D && len(policy.InputSchema) == 0 || policy.ExecutionMode == valueProviderHostedF3C237B6 && len(policy.HostedVariants) == 0 {
		return ErrRunSnapshotIncompatible
	}
	approvalMode := policy.ApprovalMode
	if policy.ApprovalCapability == valuePerCall065DDC2C && approvalMode != valueNeverF5C79F24 {
		approvalMode = valueAlwaysE613B9F9
	}
	if policy.ExecutionMode == valueLocalDispatchC00F9A8D {
		result[policy.ModelName] = ResolvedTool{ToolKey: policy.ToolKey, ProviderKind: policy.ProviderKind, ProviderKey: policy.ProviderKey, ModelName: policy.ModelName, OriginalName: policy.OriginalName, Description: policy.Description, DefinitionVersion: policy.DefinitionVersion, InputSchema: append(json.RawMessage(nil), policy.InputSchema...), ExecutionMode: policy.ExecutionMode, ApprovalCapability: policy.ApprovalCapability, ApprovalMode: approvalMode, RiskLevel: policy.RiskLevel, SideEffectLevel: policy.SideEffectLevel}
	}
	return nil
}

func validRunToolPolicySnapshot(policy effectiveRunToolPolicy) bool {
	required := []string{policy.ToolKey, policy.ProviderKind, policy.ProviderKey, policy.ModelName, policy.OriginalName, policy.DefinitionVersion, policy.Fingerprint}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return policy.RetryCount >= 0 && policy.Concurrency > 0 && fingerprintRunToolSnapshot(policy) == policy.Fingerprint
}

func hasLocalRunTools(effective effectiveTextRunConfig) bool {
	for _, policy := range effective.ToolPolicies {
		if policy.ExecutionMode == valueLocalDispatchC00F9A8D {
			return true
		}
	}
	return false
}

func hostedToolsForProtocol(effective effectiveTextRunConfig, protocol string) ([]HostedTool, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	result := make([]HostedTool, 0)
	for _, policy := range effective.ToolPolicies {
		if policy.ExecutionMode != valueProviderHostedF3C237B6 {
			continue
		}
		matched := false
		for _, variant := range policy.HostedVariants {
			if strings.ToLower(strings.TrimSpace(variant.Protocol)) != protocol {
				continue
			}
			result = append(result, HostedTool{ToolKey: policy.ToolKey, Protocol: protocol, Payload: variant.Payload})
			matched = true
			break
		}
		if !matched {
			return nil, ErrRunToolIncompatible
		}
	}
	return result, nil
}

func (s *Engine) executeRunStep(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, contextMessages []Message, summaries []string) (string, runUsage, bool, error) {
	prepared, err := s.prepareRunStepExecution(ctx, run, step, effective, tools, contextMessages, summaries)
	if err != nil {
		return "", runUsage{}, false, err
	}
	usage := runUsage{}
	for calls := 0; calls < effective.MaxLLMCalls; calls++ {
		output, err := s.generateRunStepTurn(ctx, run, step, effective, prepared, calls+1)
		if err != nil {
			return "", usage, false, err
		}
		usage = addRunUsage(usage, runUsageFromUsage(output.Usage))
		if len(output.ToolCalls) == 0 {
			text, retry, noToolErr := s.finishRunStepWithoutTools(ctx, run, step, effective, prepared, calls+1, output.Text)
			if retry {
				continue
			}
			return text, usage, false, noToolErr
		}
		finalText, terminal, waiting, err := s.advanceRunStepWithTools(ctx, run, step, effective, tools, prepared, calls+1, output)
		if err != nil || waiting || terminal {
			return finalText, usage, waiting, err
		}
	}
	return "", usage, false, errCategory8A92970CAF
}

func (s *Engine) finishRunStepWithoutTools(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, prepared *preparedRunStepExecution, callNumber int, text string) (string, bool, error) {
	class := classifyModelText(text)
	if class == ModelTextEmpty {
		return "", false, errCategoryDE42830626
	}
	if class != ModelTextToolProtocol && !requiresWorkspaceArtifact(effective) {
		return truncateRunResult(text), false, nil
	}
	if callNumber >= effective.MaxLLMCalls {
		return "", false, errRequiredToolCallNotProduced
	}
	reason := "required_tool_missing"
	if class == ModelTextToolProtocol {
		reason = string(ModelTextToolProtocol)
	}
	if err := s.appendToolProtocolRejectedEvent(context.WithoutCancel(ctx), run, step.StepID, callNumber, reason); err != nil {
		return "", false, err
	}
	prepared.forceToolChoiceRequired = true
	prepared.messages = append(prepared.messages, Message{Role: valueUser19341906, Content: malformedToolAttemptCorrectionPrompt(effective)})
	return "", true, nil
}

func (s *Engine) advanceRunStepWithTools(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, prepared *preparedRunStepExecution, callNumber int, output *GenerateOutput) (string, bool, bool, error) {
	if err := s.appendRunProgress(context.WithoutCancel(ctx), run, step.StepID, publicRunProgressFromModelText(output.Text, true)); err != nil {
		return "", false, false, err
	}
	output.ToolCalls = normalizeRunToolCalls(run.RunID, step.StepID, callNumber, output.ToolCalls)
	output.ToolCalls = backfillToolCallThoughtSignatures(output.ToolCalls, output.Reasoning)
	if prepared.route != nil && shouldSerializeWorkspaceToolCalls(effective, prepared.route.Protocol) {
		output.ToolCalls = serializeWorkspaceToolCalls(output.ToolCalls)
	}
	assistantText := output.Text
	if classifyModelText(assistantText) == ModelTextToolProtocol {
		assistantText = ""
	}
	prepared.messages = append(prepared.messages, Message{Role: valueAssistantCE8D479A, Content: assistantText, ToolCalls: output.ToolCalls})
	results, waiting, err := s.executeRunStepToolCalls(ctx, run, step, effective, tools, prepared.committed, output.ToolCalls)
	if err != nil || waiting {
		return "", false, waiting, err
	}
	if finalText, terminal := terminalWorkspaceArtifactResult(effective, results); terminal {
		return finalText, true, false, nil
	}
	prepared.messages = append(prepared.messages, Message{Role: valueToolCCF14517, ToolResults: results})
	return "", false, false, nil
}

type preparedRunStepExecution struct {
	route                   *LLMRoute
	hosted                  []HostedTool
	tools                   []ToolDefinition
	committed               map[string]ToolResult
	messages                []Message
	forceToolChoiceRequired bool
}

// toolChoiceForRunStep selects provider-neutral tool calling mode.
// Explicit change_set/review contracts require native tool calls until an
// artifact is published; callers may force required after a malformed attempt.
func toolChoiceForRunStep(effective effectiveTextRunConfig, forceRequired bool) ToolChoice {
	if forceRequired || requiresWorkspaceArtifact(effective) {
		return ToolChoice{Mode: ToolChoiceRequired}
	}
	return ToolChoice{Mode: ToolChoiceAuto}
}

func (s *Engine) prepareRunStepExecution(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, contextMessages []Message, summaries []string) (*preparedRunStepExecution, error) {
	if s.llmGateway == nil {
		return nil, ErrModelRouteNotConfigured
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID + ":" + step.StepID})
	if err != nil {
		return nil, err
	}
	hostedTools, err := hostedToolsForProtocol(effective, route.Protocol)
	if err != nil {
		return nil, err
	}
	definitions := runStepToolDefinitions(tools, effective)
	if err := s.validateWorkspaceRoute(ctx, effective, definitions, route.ModelCapabilitiesJSON, route.Protocol); err != nil {
		return nil, err
	}
	committed, committedSummaries, err := s.loadCommittedToolResults(ctx, run, step.StepID)
	if err != nil {
		return nil, err
	}
	startedCalls, err := s.loadStartedToolCalls(ctx, run, step.StepID)
	if err != nil {
		return nil, err
	}
	forceRequired, err := s.loadForceToolChoiceRequired(ctx, run, step.StepID)
	if err != nil {
		return nil, err
	}
	stepContext := append(append([]string{}, summaries...), committedSummaries...)
	messages := cloneLLMMessages(contextMessages)
	messages = append(messages, Message{Role: valueUser19341906, Content: fmt.Sprintf("执行当前计划步骤。\n当前步骤：%s\n步骤说明：%s\n已完成结果：\n%s\n已提交的工具调用不得重复执行，必须直接使用其耐久结果。", step.Title, step.Description, strings.Join(stepContext, "\n"))})
	messages = appendCommittedRunToolResults(messages, committed, startedCalls)
	if forceRequired {
		// Re-inject correction after resume rebuilds the transcript without in-memory state.
		messages = append(messages, Message{Role: valueUser19341906, Content: malformedToolAttemptCorrectionPrompt(effective)})
	}
	return &preparedRunStepExecution{
		route:                   route,
		hosted:                  hostedTools,
		tools:                   definitions,
		committed:               committed,
		messages:                messages,
		forceToolChoiceRequired: forceRequired,
	}, nil
}

func (s *Engine) appendToolProtocolRejectedEvent(ctx context.Context, run model.Run, stepID string, retryCount int, reason string) error {
	payload := toolProtocolRejectedPayload(retryCount, reason)
	return s.appendRunEvent(ctx, &run, valueModelToolProtocolRejected, stepID, "Model tool protocol rejected; next tool choice required", payload, nil)
}

func toolProtocolRejectedPayload(retryCount int, reason string) map[string]interface{} {
	if retryCount < 1 {
		retryCount = 1
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = string(ModelTextToolProtocol)
	}
	return map[string]interface{}{
		valueRetryCount:     retryCount,
		valueNextToolChoice: string(ToolChoiceRequired),
		valueReasonB5B063AA: reason,
	}
}

func forceToolChoiceRequiredFromEvents(events []model.Event, stepID string) bool {
	stepID = strings.TrimSpace(stepID)
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.EventType != valueModelToolProtocolRejected {
			continue
		}
		if stepID != "" && strings.TrimSpace(event.StepID) != stepID {
			continue
		}
		if eventForcesToolChoiceRequired(event.PayloadJSON) {
			return true
		}
	}
	return false
}

func eventForcesToolChoiceRequired(payloadJSON string) bool {
	var payload map[string]interface{}
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil || payload == nil {
		return false
	}
	next, _ := payload[valueNextToolChoice].(string)
	return strings.EqualFold(strings.TrimSpace(next), string(ToolChoiceRequired))
}

func (s *Engine) loadForceToolChoiceRequired(ctx context.Context, run model.Run, stepID string) (bool, error) {
	if s.repo == nil {
		return false, nil
	}
	var cursor int64
	var matched []model.Event
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			cursor = int64(event.Seq)
			if event.EventType == valueModelToolProtocolRejected {
				matched = append(matched, event)
			}
		}
		if len(events) < 1000 {
			break
		}
	}
	return forceToolChoiceRequiredFromEvents(matched, stepID), nil
}

func (s *Engine) validateWorkspaceRoute(ctx context.Context, effective effectiveTextRunConfig, tools []ToolDefinition, capabilitiesJSON string, protocol string) error {
	if effective.Workspace == nil || s.workspaces == nil {
		return nil
	}
	provider, ok := s.workspaces.ResolveWorkspace(effective.Workspace.Request.Type)
	if !ok {
		return ErrRunSnapshotIncompatible
	}
	validator, ok := provider.(WorkspaceRouteValidator)
	if !ok {
		return nil
	}
	payloadBytes, payloadObserved := measureProviderPayloadBytesIfProtocol(protocol, tools)
	err := validator.ValidateWorkspaceRoute(ctx, WorkspaceRouteValidation{
		ModelCapabilitiesJSON:    capabilitiesJSON,
		ProviderProtocol:         protocol,
		ToolCount:                len(tools),
		ProviderToolPayloadBytes: payloadBytes,
		ProviderPayloadObserved:  payloadObserved,
	})
	return classifyWorkspaceProviderError(provider, err)
}

// runtimeControlToolDefinitionsFor returns the generic run-control tools
// allowed by the immutable workspace policy.
func runtimeControlToolDefinitionsFor(policy WorkspacePolicy) []ToolDefinition {
	askUser := ToolDefinition{Name: runControlAskUser, Description: "Ask the user for information required to continue.", InputSchema: json.RawMessage(`{"type":"object","required":["question"],"properties":{"question":{"type":"string"}}}`)}
	publishOutput := ToolDefinition{Name: runControlPublishOutput, Description: "Publish or version a durable named output with a concise summary. A file output must name either a frozen context file or the durable tool call that produced it.", InputSchema: json.RawMessage(`{"type":"object","required":["title","summary"],"additionalProperties":false,"properties":{"outputID":{"type":"string"},"title":{"type":"string"},"summary":{"type":"string"},"fileID":{"type":"string"},"sourceToolCallID":{"type":"string"}}}`)}
	controls := make([]ToolDefinition, 0, 2)
	if policy.AllowAskUser {
		controls = append(controls, askUser)
	}
	if policy.AllowPublishOutput {
		controls = append(controls, publishOutput)
	}
	return controls
}

func expectedArtifactFromEffective(effective effectiveTextRunConfig) string {
	if effective.Workspace == nil {
		return ""
	}
	if wanted := strings.TrimSpace(effective.Workspace.ExpectedArtifact); wanted != "" {
		return wanted
	}
	return strings.TrimSpace(effective.Workspace.Request.ArtifactContract)
}

func withRuntimeControlTools(tools []ToolDefinition, effective effectiveTextRunConfig) []ToolDefinition {
	policy := WorkspacePolicy{AllowAskUser: true, AllowPublishOutput: true}
	if effective.Workspace != nil {
		policy = effective.Workspace.Policy
	}
	controls := runtimeControlToolDefinitionsFor(policy)
	if len(controls) == 0 {
		return tools
	}
	out := make([]ToolDefinition, 0, len(tools)+len(controls))
	out = append(out, tools...)
	out = append(out, controls...)
	return out
}

func runStepToolDefinitions(tools map[string]ResolvedTool, effective effectiveTextRunConfig) []ToolDefinition {
	definitions := make([]ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		definitions = append(definitions, ToolDefinition{Name: tool.ModelName, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return withRuntimeControlTools(definitions, effective)
}

func appendCommittedRunToolResults(messages []Message, committed map[string]ToolResult, started map[string]ToolCall) []Message {
	if len(committed) == 0 {
		return messages
	}
	callIDs := make([]string, 0, len(committed))
	for callID := range committed {
		callIDs = append(callIDs, callID)
	}
	sort.Strings(callIDs)
	calls := make([]ToolCall, 0, len(callIDs))
	results := make([]ToolResult, 0, len(callIDs))
	for _, callID := range callIDs {
		result := committed[callID]
		call := ToolCall{ToolCallID: result.ToolCallID, ToolName: result.ToolName, ArgumentsJSON: `{}`}
		if startedCall, ok := started[callID]; ok {
			if name := strings.TrimSpace(startedCall.ToolName); name != "" {
				call.ToolName = name
			}
			if args := strings.TrimSpace(startedCall.ArgumentsJSON); args != "" {
				call.ArgumentsJSON = args
			}
			call.ThoughtSignature = strings.TrimSpace(startedCall.ThoughtSignature)
		}
		calls = append(calls, call)
		results = append(results, result)
	}
	return append(messages, Message{Role: valueAssistantCE8D479A, ToolCalls: calls}, Message{Role: valueToolCCF14517, ToolResults: results})
}

func (s *Engine) generateRunStepTurn(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, prepared *preparedRunStepExecution, callNumber int) (*GenerateOutput, error) {
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, true, 1); err != nil {
		return nil, err
	}
	output, err := s.llmGateway.GenerateText(ctx, prepared.route, GenerateInput{
		RequestID:    fmt.Sprintf("%s:step:%s:%d", run.RunID, step.StepID, callNumber),
		Thread:       run.Thread,
		Messages:     prepared.messages,
		Instructions: strings.TrimSpace(effective.Instructions + "\n" + runPublicProgressInstruction),
		Tools:        prepared.tools,
		HostedTools:  prepared.hosted,
		DisableTools: false,
		ToolChoice:   toolChoiceForRunStep(effective, prepared.forceToolChoiceRequired),
		Options:      effective.Options,
	})
	if err != nil {
		return nil, err
	}
	if err = s.recordRunLLMUsageForStep(context.WithoutCancel(ctx), run, step.StepID, valueStepB959B536, prepared.route, output); err != nil {
		return nil, err
	}
	return output, nil
}

func (s *Engine) appendRunProgress(ctx context.Context, run model.Run, stepID, content string) error {
	content = truncatePublicRunProgress(content)
	if content == "" {
		return nil
	}
	event := newRunEvent(run, "progress.created", stepID, content, map[string]interface{}{"contentMarkdown": content}, nil)
	event.EventID = publicRunProgressEventID(run.RunID, content)
	event.Status = model.RunStatusCompleted
	saved, created, err := s.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		s.PublishRunNotification(run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func publicRunProgressEventID(runID, content string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(content)))
	return "evt_progress_" + fmt.Sprintf("%x", digest[:16])
}

func truncatePublicRunProgress(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 320 {
		return string(runes[:319]) + "…"
	}
	return string(runes)
}

// ModelTextClassification separates empty, public natural language, and tool
// protocol markup that must never become user-visible terminal content.
type ModelTextClassification string

const (
	ModelTextEmpty        ModelTextClassification = "empty"
	ModelTextPublic       ModelTextClassification = "public"
	ModelTextToolProtocol ModelTextClassification = "tool_protocol"
)

// classifyModelText is the single application-layer boundary for model text
// safety. Progress, assistant finals, and stream completion all use it.
func classifyModelText(text string) ModelTextClassification {
	if strings.TrimSpace(text) == "" {
		return ModelTextEmpty
	}
	if looksLikeToolProtocolText(text) {
		return ModelTextToolProtocol
	}
	return ModelTextPublic
}

// publicRunProgressFromModelText selects text safe for user-visible run progress.
// When the model turn also issued tool calls, associated Text is treated as
// untrusted protocol/commentary and is not published as progress.created.
func publicRunProgressFromModelText(text string, hasToolCalls bool) string {
	if hasToolCalls {
		return ""
	}
	if classifyModelText(text) != ModelTextPublic {
		return ""
	}
	return truncatePublicRunProgress(text)
}

// toolProtocolMarkers is the single source of tool-call protocol markup tokens.
// looksLikeToolProtocolText and streaming incomplete-suffix holds reuse it.
func toolProtocolMarkers() []string {
	return []string{
		"<tool_call", "</tool_call",
		"<tool_calls", "</tool_calls",
		"<function_call", "</function_call",
		"<|tool_call", "<|function",
		"<|dsml|", "</|dsml|",
		"｜dsml｜", "||dsml||", "|dsml|",
		"dsml|tool_calls", "dsml|invoke", "dsml|parameter",
		`"tool_calls"`,
		`"function_call"`,
		`"functioncall"`,
	}
}

// looksLikeToolProtocolText detects known tool-call protocol markup.
func looksLikeToolProtocolText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range toolProtocolMarkers() {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// hasIncompleteProtocolMarkerSuffix reports whether the end of text could still
// complete into a known protocol marker once more bytes arrive. Minimum prefix
// length is 2 so a lone '<' (e.g. HTML prose) does not block streaming forever.
func hasIncompleteProtocolMarkerSuffix(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	const minPrefix = 2
	// Only the trailing window can still be completing a marker.
	window := lower
	if maxMarker := longestToolProtocolMarkerLen(); len(window) > maxMarker {
		window = window[len(window)-maxMarker:]
	}
	for _, marker := range toolProtocolMarkers() {
		limit := len(marker)
		if limit <= minPrefix {
			continue
		}
		// True prefix only: full marker is already handled by looksLikeToolProtocolText.
		for n := minPrefix; n < limit; n++ {
			if strings.HasSuffix(window, marker[:n]) || strings.HasSuffix(lower, marker[:n]) {
				return true
			}
		}
	}
	return false
}

func longestToolProtocolMarkerLen() int {
	maxLen := 0
	for _, marker := range toolProtocolMarkers() {
		if len(marker) > maxLen {
			maxLen = len(marker)
		}
	}
	if maxLen < 2 {
		return 2
	}
	return maxLen
}

func malformedToolAttemptCorrectionPrompt(effective effectiveTextRunConfig) string {
	base := "The previous response encoded a tool call as plain text. Do not emit DSML, XML, JSON envelopes, or tool-call markup. Use the provider-native function/tool call mechanism."
	if requiresWorkspaceArtifact(effective) {
		if effective.Workspace != nil {
			if prompt := strings.TrimSpace(effective.Workspace.Policy.Failure.CorrectionPrompt); prompt != "" {
				return base + " " + prompt
			}
		}
		return base + " The required workspace artifact has not been published. Continue by calling the required tools with a complete schema-valid payload; a plain-text answer cannot complete this run."
	}
	return base + " If a tool is needed, call it natively; otherwise answer in plain natural language without protocol markup."
}

func (s *Engine) executeRunStepToolCalls(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, committed map[string]ToolResult, calls []ToolCall) ([]ToolResult, bool, error) {
	results := make([]ToolResult, 0, len(calls))
	for _, call := range calls {
		if result, exists := committed[call.ToolCallID]; exists {
			results = append(results, result)
			continue
		}
		result, waiting, err := s.handleRunToolCall(ctx, run, step, effective, tools, call)
		if waiting || err != nil {
			return nil, waiting, err
		}
		results = append(results, result)
	}
	return results, false, nil
}

func normalizeRunToolCalls(runID, stepID string, callNumber int, calls []ToolCall) []ToolCall {
	result := make([]ToolCall, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for index, call := range calls {
		call.ToolCallID = strings.TrimSpace(call.ToolCallID)
		if _, duplicate := seen[call.ToolCallID]; call.ToolCallID == "" || duplicate {
			seed := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s\x00%s", runID, stepID, callNumber, index, call.ToolName, canonicalRunJSON(json.RawMessage(call.ArgumentsJSON)))
			digest := sha256.Sum256([]byte(seed))
			call.ToolCallID = "tool_" + fmt.Sprintf("%x", digest[:16])
		}
		seen[call.ToolCallID] = struct{}{}
		result[index] = call
	}
	return result
}

// backfillToolCallThoughtSignatures copies a reasoning-part signature onto tool
// calls that arrived without one (common on Gemini Thinking multi-tool turns).
func backfillToolCallThoughtSignatures(calls []ToolCall, reasoning *ReasoningOutput) []ToolCall {
	if len(calls) == 0 || reasoning == nil {
		return calls
	}
	signature := strings.TrimSpace(reasoning.Signature)
	if signature == "" {
		return calls
	}
	for i := range calls {
		if strings.TrimSpace(calls[i].ThoughtSignature) == "" {
			calls[i].ThoughtSignature = signature
		}
	}
	return calls
}

// shouldSerializeWorkspaceToolCalls applies the immutable provider policy
// captured in the workspace snapshot.
func shouldSerializeWorkspaceToolCalls(effective effectiveTextRunConfig, protocol string) bool {
	if effective.Workspace == nil {
		return false
	}
	for _, candidate := range effective.Workspace.Policy.SerializeToolProtocols {
		if strings.EqualFold(strings.TrimSpace(protocol), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

// serializeWorkspaceToolCalls keeps the first tool call only so assistant
// transcript tool_calls and tool results stay 1:1 for the next provider turn.
func serializeWorkspaceToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) <= 1 {
		return calls
	}
	return calls[:1]
}

func (s *Engine) loadCommittedToolResults(ctx context.Context, run model.Run, stepID string) (map[string]ToolResult, []string, error) {
	results := make(map[string]ToolResult)
	summaries := make([]string, 0)
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return nil, nil, err
		}
		for _, event := range events {
			cursor = int64(event.Seq)
			if event.StepID != stepID || event.ToolCallID == "" || (event.EventType != valueToolCompleted8D0A12FD && event.EventType != valueToolFailedFB145984) {
				continue
			}
			result := committedToolResult(event)
			results[event.ToolCallID] = result
			summaries = append(summaries, fmt.Sprintf("已提交工具调用 %s (%s)，状态 %s，结果：%s", event.ToolCallID, event.ToolName, result.Status, truncateRunResult(result.OutputJSON)))
		}
		if len(events) < 1000 {
			break
		}
	}
	return results, summaries, nil
}

func committedToolResult(event model.Event) ToolResult {
	result := ToolResult{ToolCallID: event.ToolCallID, ToolName: event.ToolName, Status: valueSuccess4D886D19, OutputJSON: event.OutputJSON}
	if event.EventType != valueToolFailedFB145984 {
		return result
	}
	result.Status, result.OutputJSON = valueErrorA8DE48C2, event.ErrorJSON
	var payload map[string]interface{}
	if json.Unmarshal([]byte(event.ErrorJSON), &payload) == nil {
		result.Error, _ = payload[valueErrorA8DE48C2].(string)
	}
	if result.Error == "" {
		result.Error = "tool_failed"
	}
	return result
}

func (s *Engine) handleRunToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, call ToolCall) (ToolResult, bool, error) {
	if call.ToolName == runControlAskUser {
		return s.handleAskUserToolCall(ctx, run, step, effective, call)
	}
	if call.ToolName == runControlPublishOutput {
		return s.handlePublishOutputToolCall(ctx, run, step, effective, call)
	}
	return s.handleResolvedRunToolCall(ctx, run, step, effective, tools, call)
}

func (s *Engine) handleAskUserToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, call ToolCall) (ToolResult, bool, error) {
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return ToolResult{}, false, err
	}
	started := newRunEvent(run, valueToolStartedB113F313, step.StepID, call.ToolName, map[string]interface{}{valueToolCallID64CA70DB: call.ToolCallID, valueToolName4234B607: call.ToolName}, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, call.ToolName, call.ArgumentsJSON
	if err := s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started}); err != nil {
		return ToolResult{}, false, err
	}
	var request map[string]interface{}
	_ = json.Unmarshal([]byte(call.ArgumentsJSON), &request)
	interaction := newRunInteraction(run, step.StepID, model.InteractionAskUser, request, effective.InteractionTTLHours)
	interaction.ToolCallID = call.ToolCallID
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "ask_user")
	if err != nil {
		return ToolResult{}, false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", step.StepID, "Waiting checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID}, nil),
		newRunEvent(run, "interaction.created", step.StepID, "User input required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, valueRequest91B6AFF3: request}, nil),
		newRunEvent(run, "step.waiting_input", step.StepID, "Waiting for user input", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil),
		newRunEvent(run, "run.waiting_input", step.StepID, "Waiting for user input", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "ask_user"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, model.RunStatusRunning, interaction, checkpoint, events)
	if err == nil {
		s.publishRunEvents(run.RunID, saved)
	}
	return ToolResult{}, true, err
}

func (s *Engine) handlePublishOutputToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, call ToolCall) (ToolResult, bool, error) {
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return ToolResult{}, false, err
	}
	started := newRunEvent(run, valueToolStartedB113F313, step.StepID, call.ToolName, map[string]interface{}{valueToolCallID64CA70DB: call.ToolCallID, valueToolName4234B607: call.ToolName}, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, call.ToolName, call.ArgumentsJSON
	if err := s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started}); err != nil {
		return ToolResult{}, false, err
	}
	var request struct {
		OutputID, Title, Summary, FileID, SourceToolCallID string
	}
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &request); err != nil {
		return ToolResult{}, false, err
	}
	output, outputEvent, err := s.prepareOutput(ctx, run, step.StepID, call.ToolCallID, request.OutputID, request.Title, request.Summary, request.FileID, request.SourceToolCallID, 0)
	if err != nil {
		return ToolResult{}, false, err
	}
	completed := newRunEvent(run, valueToolCompleted8D0A12FD, step.StepID, call.ToolName, map[string]interface{}{valueToolCallID64CA70DB: call.ToolCallID, valueToolName4234B607: call.ToolName, valueOutputID7E64D749: output.OutputID}, nil)
	completed.ToolCallID, completed.ToolName, completed.InputJSON, completed.OutputJSON = call.ToolCallID, call.ToolName, call.ArgumentsJSON, mustRunJSON(map[string]interface{}{valueOutputID7E64D749: output.OutputID})
	checkpoint := newRunContinuationCheckpoint(run, step.StepID, "tool_result", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: runSegmentKey(ctx, run), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, StepID: step.StepID, DurableToolResult: &runDurableToolResult{ToolCallID: call.ToolCallID, EventType: valueToolCompleted8D0A12FD}})
	checkpoint.ToolCallID = call.ToolCallID
	checkpointEvent := newRunEvent(run, "checkpoint.created", step.StepID, "Published output checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueToolCallID64CA70DB: call.ToolCallID}, nil)
	savedOutput, saved, _, commitErr := s.repo.CommitRunToolResultBundle(context.WithoutCancel(ctx), checkpoint, output, []model.Event{outputEvent, completed, checkpointEvent})
	if commitErr != nil {
		return ToolResult{}, false, commitErr
	}
	s.publishRunEvents(run.RunID, saved)
	if savedOutput == nil {
		return ToolResult{}, false, ErrRunToolConflict
	}
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Status: valueSuccess4D886D19, OutputJSON: mustRunJSON(map[string]interface{}{valueOutputID7E64D749: savedOutput.OutputID})}, false, nil
}

func (s *Engine) handleResolvedRunToolCall(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tools map[string]ResolvedTool, call ToolCall) (ToolResult, bool, error) {
	tool, ok := tools[call.ToolName]
	if !ok {
		return ToolResult{}, false, withErrorMessage(errCategory00919D2AA2, fmt.Sprintf("tool %s is not available in the run snapshot", call.ToolName))
	}
	if tool.ApprovalMode != valueNeverF5C79F24 {
		return s.requestRunToolApproval(ctx, run, step, effective, tool, call)
	}
	if err := s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return ToolResult{}, false, err
	}
	return s.executeFrozenRunTool(ctx, run, step.StepID, effective, tool, call)
}

func (s *Engine) requestRunToolApproval(ctx context.Context, run model.Run, step model.Step, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall) (ToolResult, bool, error) {
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(tool.ToolKey+"\x00"+tool.ModelName+"\x00"+canonicalRunJSON(json.RawMessage(call.ArgumentsJSON)))))
	request := map[string]interface{}{valueToolKey560014C9: tool.ToolKey, valueToolName4234B607: tool.ModelName, "originalName": tool.OriginalName, valueToolCallID64CA70DB: call.ToolCallID, "arguments": json.RawMessage(call.ArgumentsJSON), "fingerprint": fingerprint, "sideEffectLevel": tool.SideEffectLevel}
	interaction := newRunInteraction(run, step.StepID, model.InteractionApproveTool, request, effective.InteractionTTLHours)
	interaction.ToolCallID = call.ToolCallID
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "approve_tool")
	if err != nil {
		return ToolResult{}, false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", step.StepID, "Tool approval checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID}, nil),
		newRunEvent(run, "interaction.created", step.StepID, "Tool approval required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, valueToolName4234B607: tool.ModelName}, nil),
		newRunEvent(run, "step.waiting_input", step.StepID, "Waiting for tool approval", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil),
		newRunEvent(run, "run.waiting_input", step.StepID, "Waiting for tool approval", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "approve_tool"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, model.RunStatusRunning, interaction, checkpoint, events)
	if err == nil {
		s.publishRunEvents(run.RunID, saved)
	}
	return ToolResult{}, true, err
}

func (s *Engine) executeFrozenRunTool(ctx context.Context, run model.Run, stepID string, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall) (ToolResult, bool, error) {
	if err := s.appendFrozenToolStarted(ctx, run, stepID, tool, call); err != nil {
		return ToolResult{}, false, err
	}
	policy, ok := frozenRunToolPolicy(effective, tool.ToolKey)
	if !ok {
		return ToolResult{}, false, ErrRunSnapshotIncompatible
	}
	workspaceTool := effective.Workspace != nil && tool.ProviderKind == strings.TrimSpace(effective.Workspace.Request.Type)
	if !workspaceTool && tool.ProviderKind != valueMcpCE1A7808 || workspaceTool && s.workspaces == nil {
		return ToolResult{}, false, ErrRunSnapshotIncompatible
	}
	limits := &TextRunExecutionLimits{MaxLLMCalls: effective.MaxLLMCalls, MaxToolCalls: effective.MaxToolCalls, ToolRetryCount: policy.RetryCount, ToolConcurrency: policy.Concurrency}
	output, err := s.executeFrozenToolProvider(ctx, run, stepID, effective, tool, call, limits)
	workspaceResultTokens, output, err := s.enforceFrozenWorkspaceBudget(ctx, run, effective, tool, output, err)
	return s.commitFrozenToolResult(ctx, run, stepID, effective, tool, call, output, workspaceResultTokens, err)
}

func (s *Engine) appendFrozenToolStarted(ctx context.Context, run model.Run, stepID string, tool ResolvedTool, call ToolCall) error {
	payload := map[string]interface{}{
		valueSegmentKeyB3442EFB:   runSegmentKey(ctx, run),
		valueToolCallID64CA70DB:   call.ToolCallID,
		valueToolKey560014C9:      tool.ToolKey,
		valueToolName4234B607:     tool.ModelName,
		valueProviderKind7144A4D9: tool.ProviderKind,
	}
	if signature := strings.TrimSpace(call.ThoughtSignature); signature != "" {
		payload["thoughtSignature"] = signature
	}
	started := newRunEvent(run, valueToolStartedB113F313, stepID, tool.ModelName, payload, nil)
	started.ToolCallID, started.ToolName, started.InputJSON = call.ToolCallID, tool.ModelName, call.ArgumentsJSON
	return s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{started})
}

func (s *Engine) loadStartedToolCalls(ctx context.Context, run model.Run, stepID string) (map[string]ToolCall, error) {
	started := make(map[string]ToolCall)
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			cursor = int64(event.Seq)
			if event.StepID != stepID || event.EventType != valueToolStartedB113F313 || strings.TrimSpace(event.ToolCallID) == "" {
				continue
			}
			call := ToolCall{
				ToolCallID:    event.ToolCallID,
				ToolName:      event.ToolName,
				ArgumentsJSON: event.InputJSON,
			}
			var payload struct {
				ThoughtSignature string `json:"thoughtSignature"`
			}
			if json.Unmarshal([]byte(event.PayloadJSON), &payload) == nil {
				call.ThoughtSignature = strings.TrimSpace(payload.ThoughtSignature)
			}
			started[event.ToolCallID] = call
		}
		if len(events) < 1000 {
			break
		}
	}
	return started, nil
}

func (s *Engine) executeFrozenToolProvider(ctx context.Context, run model.Run, stepID string, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall, limits *TextRunExecutionLimits) (string, error) {
	switch tool.ProviderKind {
	case workspaceProviderKind(effective):
		if s.workspaces == nil || effective.Workspace == nil {
			return "", ErrRunSnapshotIncompatible
		}
		provider, ok := s.workspaces.ResolveWorkspace(effective.Workspace.Request.Type)
		if !ok {
			return "", ErrRunSnapshotIncompatible
		}
		output, err := provider.ExecuteWorkspaceTool(ctx, WorkspaceToolExecution{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, ToolName: tool.OriginalName, ArgumentsJSON: call.ArgumentsJSON, Snapshot: *effective.Workspace})
		return output, classifyWorkspaceProviderError(provider, err)
	case valueMcpCE1A7808:
		return s.executeToolCall(ctx, ExecuteToolInput{Actor: run.Actor, Thread: run.Thread, RequestID: run.RunID + ":tool:" + call.ToolCallID, ToolKey: tool.ToolKey, ProviderKind: tool.ProviderKind, ProviderKey: tool.ProviderKey, ToolName: tool.OriginalName, ArgumentsJSON: call.ArgumentsJSON, ExecutionLimits: limits, OnAttemptFailed: func(attempt, maxAttempts int, attemptErr error) error {
			return s.appendFrozenToolAttemptFailure(ctx, run, stepID, tool, call, attempt, maxAttempts, attemptErr)
		}})
	default:
		return "", ErrRunSnapshotIncompatible
	}
}

func workspaceProviderKind(effective effectiveTextRunConfig) string {
	if effective.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(effective.Workspace.Request.Type)
}

func isWorkspaceProviderTool(effective effectiveTextRunConfig, tool ResolvedTool) bool {
	kind := workspaceProviderKind(effective)
	return kind != "" && strings.TrimSpace(tool.ProviderKind) == kind
}

func (s *Engine) appendFrozenToolAttemptFailure(ctx context.Context, run model.Run, stepID string, tool ResolvedTool, call ToolCall, attempt, maxAttempts int, attemptErr error) error {
	event := newRunEvent(run, "tool.attempt_failed", stepID, tool.ModelName, map[string]interface{}{valueSegmentKeyB3442EFB: runSegmentKey(ctx, run), valueToolCallID64CA70DB: call.ToolCallID, valueToolKey560014C9: tool.ToolKey, valueToolName4234B607: tool.ModelName, valueProviderKind7144A4D9: tool.ProviderKind, "attempt": attempt, "maxAttempts": maxAttempts}, nil)
	event.ToolCallID, event.ToolName = call.ToolCallID, tool.ModelName
	event.ErrorJSON = mustRunJSON(map[string]interface{}{valueErrorA8DE48C2: attemptErr.Error()})
	return s.appendRunEvents(context.WithoutCancel(ctx), run.RunID, []model.Event{event})
}

func (s *Engine) enforceFrozenWorkspaceBudget(ctx context.Context, run model.Run, effective effectiveTextRunConfig, tool ResolvedTool, output string, executionErr error) (int64, string, error) {
	if executionErr != nil || !isWorkspaceProviderTool(effective, tool) {
		return 0, output, executionErr
	}
	tokens := estimateTokens(output)
	if err := s.ensureWorkspaceToolResultBudget(ctx, run, effective, tokens); err != nil {
		return tokens, "", err
	}
	return tokens, output, nil
}

func (s *Engine) commitFrozenToolResult(ctx context.Context, run model.Run, stepID string, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall, output string, workspaceResultTokens int64, executionErr error) (ToolResult, bool, error) {
	eventType, status := valueToolCompleted8D0A12FD, valueSuccess4D886D19
	if executionErr != nil {
		eventType, status = valueToolFailedFB145984, valueErrorA8DE48C2
	}
	completedPayload := map[string]interface{}{valueSegmentKeyB3442EFB: runSegmentKey(ctx, run), valueToolCallID64CA70DB: call.ToolCallID, valueToolKey560014C9: tool.ToolKey, valueToolName4234B607: tool.ModelName, valueProviderKind7144A4D9: tool.ProviderKind, valueStatus327C4193: status}
	repeatCount, countErr := s.addDeterministicFailureMetadata(ctx, run, effective, tool, call, executionErr, completedPayload)
	if countErr != nil {
		return ToolResult{}, false, countErr
	}
	if isWorkspaceProviderTool(effective, tool) {
		completedPayload["workspaceToolResultTokenEstimate"] = workspaceResultTokens
	}
	completed := newRunEvent(run, eventType, stepID, tool.ModelName, completedPayload, nil)
	completed.ToolCallID, completed.ToolName, completed.InputJSON = call.ToolCallID, tool.ModelName, call.ArgumentsJSON
	if executionErr != nil {
		completed.ErrorJSON = mustRunJSON(map[string]interface{}{valueErrorA8DE48C2: executionErr.Error()})
	} else {
		completed.OutputJSON = output
	}
	checkpoint := newRunContinuationCheckpoint(run, stepID, "tool_result", runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: runSegmentKey(ctx, run), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, StepID: stepID, DurableToolResult: &runDurableToolResult{ToolCallID: call.ToolCallID, EventType: eventType}})
	checkpoint.ToolCallID = call.ToolCallID
	event := newRunEvent(run, "checkpoint.created", stepID, "Tool result checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueToolCallID64CA70DB: call.ToolCallID}, nil)
	outputRef, events, outputErr := s.prepareFrozenToolProjection(ctx, run, stepID, call, output, executionErr != nil, completed, event)
	if outputErr != nil {
		return ToolResult{}, false, outputErr
	}
	_, saved, _, commitErr := s.repo.CommitRunToolResultBundle(context.WithoutCancel(ctx), checkpoint, outputRef, events)
	if commitErr != nil {
		return ToolResult{}, false, commitErr
	}
	s.publishRunEvents(run.RunID, saved)
	if executionErr != nil {
		result := failedFrozenToolResult(call, tool, status, completed, executionErr)
		if repeatCount >= maxIdenticalDeterministicToolFailures {
			// Keep the typed cause so provider error classification survives the
			// generic repeat guard.
			return result, false, fmt.Errorf("%w: %w", errRepeatedDeterministicWorkspaceToolFailure, executionErr)
		}
		return result, false, nil
	}
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: tool.ModelName, Status: status, OutputJSON: output}, false, nil
}

func (s *Engine) addDeterministicFailureMetadata(ctx context.Context, run model.Run, effective effectiveTextRunConfig, tool ResolvedTool, call ToolCall, executionErr error, payload map[string]interface{}) (int, error) {
	if !deterministicWorkspaceToolFailure(executionErr) {
		return 0, nil
	}
	fingerprint := deterministicToolFailureFingerprint(tool, call, executionErr, effective)
	prior, err := s.countDurableToolFailures(ctx, run, fingerprint)
	if err != nil {
		return 0, err
	}
	repeatCount := prior + 1
	payload["failureFingerprint"] = fingerprint
	payload["repeatCount"] = repeatCount
	payload["retryable"] = repeatCount < maxIdenticalDeterministicToolFailures
	return repeatCount, nil
}

func (s *Engine) prepareFrozenToolProjection(ctx context.Context, run model.Run, stepID string, call ToolCall, output string, executionFailed bool, completed, checkpointEvent model.Event) (*model.OutputRef, []model.Event, error) {
	events := []model.Event{completed, checkpointEvent}
	if executionFailed {
		return nil, events, nil
	}
	projection, projected := toolOutputProjection(output)
	if !projected {
		return nil, events, nil
	}
	created, outputEvent, err := s.prepareOutput(ctx, run, stepID, call.ToolCallID, "", projection.Title, projection.Summary, "", "", 0)
	if err != nil {
		return nil, nil, err
	}
	created.Kind, created.PreviewJSON = projection.Kind, string(projection.Preview)
	var artifact map[string]interface{}
	_ = json.Unmarshal(projection.Preview, &artifact)
	outputEvent.PayloadJSON = mustRunJSON(map[string]interface{}{valueOutputID7E64D749: created.OutputID, valueKindE5B2EFB3: created.Kind, valueTitle90A9E177: created.Title, valueSummaryCE2A127F: created.Summary, valueStatus327C4193: created.Status, "artifact": artifact})
	return created, []model.Event{outputEvent, completed, checkpointEvent}, nil
}

type deterministicToolFailureMarker interface {
	DeterministicToolFailure() bool
}

func deterministicWorkspaceToolFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var marker deterministicToolFailureMarker
	return errors.As(err, &marker) && marker.DeterministicToolFailure()
}

func deterministicToolFailureFingerprint(tool ResolvedTool, call ToolCall, err error, effective effectiveTextRunConfig) string {
	artifactContract := ""
	if effective.Workspace != nil {
		artifactContract = strings.TrimSpace(effective.Workspace.ExpectedArtifact)
		if artifactContract == "" {
			artifactContract = strings.TrimSpace(effective.Workspace.Request.ArtifactContract)
		}
	}
	payload := struct {
		ProviderKind, ProviderKey, ToolKey, ModelName, OriginalName string
		Arguments, Error, ArtifactContract                          string
	}{
		ProviderKind: tool.ProviderKind, ProviderKey: tool.ProviderKey, ToolKey: tool.ToolKey,
		ModelName: tool.ModelName, OriginalName: tool.OriginalName,
		Arguments: canonicalRunJSON(json.RawMessage(call.ArgumentsJSON)),
		Error:     normalizeDeterministicToolError(err), ArtifactContract: artifactContract,
	}
	raw := []byte(mustRunJSON(payload))
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:])
}

func normalizeDeterministicToolError(err error) string {
	if err == nil {
		return ""
	}
	return strings.Join(strings.Fields(err.Error()), " ")
}

func (s *Engine) countDurableToolFailures(ctx context.Context, run model.Run, fingerprint string) (int, error) {
	count := 0
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return 0, err
		}
		count = advanceConsecutiveFailureCount(events, fingerprint, count)
		if len(events) == 0 || len(events) < 1000 {
			return count, nil
		}
		cursor = int64(events[len(events)-1].Seq)
	}
}

func advanceConsecutiveFailureCount(events []model.Event, fingerprint string, count int) int {
	for _, event := range events {
		if event.EventType == valueToolCompleted8D0A12FD {
			count = 0
			continue
		}
		if event.EventType != valueToolFailedFB145984 {
			continue
		}
		var payload struct {
			FailureFingerprint string `json:"failureFingerprint"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.FailureFingerprint != fingerprint {
			count = 0
			continue
		}
		count++
	}
	return count
}

func failedFrozenToolResult(call ToolCall, tool ResolvedTool, status string, completed model.Event, executionErr error) ToolResult {
	return ToolResult{ToolCallID: call.ToolCallID, ToolName: tool.ModelName, Status: status, Error: executionErr.Error(), OutputJSON: completed.ErrorJSON}
}

func (s *Engine) ensureWorkspaceToolResultBudget(ctx context.Context, run model.Run, effective effectiveTextRunConfig, next int64) error {
	if effective.Workspace == nil || next <= 0 {
		return nil
	}
	limit := int64(effective.Workspace.ContextBudget * 55 / 100)
	if limit <= 0 {
		return nil
	}
	committed, err := s.committedWorkspaceToolResultTokens(ctx, run)
	if err != nil {
		return err
	}
	total := effective.Workspace.TokenEstimate + committed
	if total+next <= limit {
		return nil
	}
	message := fmt.Sprintf("workspace tool result budget exceeded: estimated=%d limit=%d", total+next, limit)
	if guidance := strings.TrimSpace(effective.Workspace.Policy.Failure.ToolResultBudgetGuidance); guidance != "" {
		message += "; " + guidance
	}
	return withErrorMessage(errCategoryD8EDA1A858, message)
}

func (s *Engine) committedWorkspaceToolResultTokens(ctx context.Context, run model.Run) (int64, error) {
	events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, 0, 1000)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, event := range events {
		if event.EventType != valueToolCompleted8D0A12FD || strings.TrimSpace(event.PayloadJSON) == "" {
			continue
		}
		var payload struct {
			TokenEstimate int64 `json:"workspaceToolResultTokenEstimate"`
		}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) == nil {
			total += payload.TokenEstimate
		}
	}
	return total, nil
}

func frozenRunToolPolicy(effective effectiveTextRunConfig, toolKey string) (effectiveRunToolPolicy, bool) {
	for _, policy := range effective.ToolPolicies {
		if policy.ToolKey == toolKey && policy.Fingerprint != "" && fingerprintRunToolSnapshot(policy) == policy.Fingerprint {
			return policy, true
		}
	}
	return effectiveRunToolPolicy{}, false
}

type toolProjectionInstruction struct {
	Kind    string          `json:"kind"`
	Title   string          `json:"title"`
	Summary string          `json:"summary"`
	Preview json.RawMessage `json:"preview"`
}

func toolOutputProjection(output string) (toolProjectionInstruction, bool) {
	var envelope struct {
		Projection *toolProjectionInstruction `json:"projection"`
	}
	if json.Unmarshal([]byte(output), &envelope) != nil || envelope.Projection == nil {
		return toolProjectionInstruction{}, false
	}
	projection := *envelope.Projection
	projection.Kind = strings.TrimSpace(projection.Kind)
	projection.Title = strings.TrimSpace(projection.Title)
	projection.Summary = strings.TrimSpace(projection.Summary)
	if projection.Kind == "" || projection.Title == "" || projection.Summary == "" || len(projection.Preview) == 0 || !json.Valid(projection.Preview) {
		return toolProjectionInstruction{}, false
	}
	return projection, true
}

func terminalWorkspaceArtifactResult(effective effectiveTextRunConfig, results []ToolResult) (string, bool) {
	if effective.Workspace == nil {
		return "", false
	}
	policy := effective.Workspace.Policy
	if len(policy.TerminalArtifactTypes) == 0 {
		return "", false
	}
	resourceID := strings.TrimSpace(effective.Workspace.Request.ResourceID)
	for _, result := range results {
		projection, ok := toolOutputProjection(result.OutputJSON)
		if !ok {
			continue
		}
		var preview map[string]interface{}
		if json.Unmarshal(projection.Preview, &preview) != nil || !containsRuntimeString(policy.TerminalArtifactTypes, strings.TrimSpace(fmt.Sprint(preview["artifactType"]))) {
			continue
		}
		if field := strings.TrimSpace(policy.ArtifactResourceField); field != "" && strings.TrimSpace(fmt.Sprint(preview[field])) != resourceID {
			continue
		}
		return truncateRunResult(projection.Title + "\n\n" + projection.Summary), true
	}
	return "", false
}

func requiresWorkspaceArtifact(effective effectiveTextRunConfig) bool {
	return effective.Workspace != nil && effective.Workspace.Policy.RequiredArtifact
}

func (s *Engine) synthesizeRun(ctx context.Context, run model.Run, orchestrationStepID string, effective effectiveTextRunConfig, contextMessages []Message, summaries []string) (Usage, *LLMRoute, string, error) {
	messages := cloneLLMMessages(contextMessages)
	messages = append(messages, Message{Role: valueUser19341906, Content: "基于以下已完成步骤结果合成最终回答：\n" + strings.Join(summaries, "\n")})
	return s.streamRunAnswer(ctx, run, orchestrationStepID, effective, "synthesis", "synthesis", messages, strings.TrimSpace(effective.Instructions)+"\n基于已完成步骤合成最终回答。", false)
}

func (s *Engine) streamRunAnswer(ctx context.Context, run model.Run, orchestrationStepID string, effective effectiveTextRunConfig, requestKind, phase string, promptMessages []Message, instructions string, enableHostedTools bool) (Usage, *LLMRoute, string, error) {
	route, _, hostedTools, err := s.prepareStreamRun(ctx, run, effective, requestKind, enableHostedTools)
	if err != nil {
		return Usage{}, route, "", err
	}
	collector := runDeltaCollector{service: s, ctx: ctx, run: run, stepID: orchestrationStepID, projection: run.OutputProjection, lastFlush: time.Now()}
	output, err := s.llmGateway.GenerateTextStream(ctx, route, GenerateInput{RequestID: run.RunID + ":" + requestKind, Messages: promptMessages, Instructions: instructions, HostedTools: hostedTools, DisableTools: len(hostedTools) == 0, Options: effective.Options}, collector.accept)
	if err != nil {
		return Usage{}, route, "", err
	}
	usage, finalText, err := s.finishStreamRun(ctx, run, orchestrationStepID, phase, route, output, &collector)
	return usage, route, finalText, err
}

func (s *Engine) prepareStreamRun(ctx context.Context, run model.Run, effective effectiveTextRunConfig, requestKind string, enableHostedTools bool) (*LLMRoute, model.ProjectionRef, []HostedTool, error) {
	if s.llmGateway == nil {
		return nil, model.ProjectionRef{}, nil, ErrModelRouteNotConfigured
	}
	route, err := s.llmGateway.PrepareTextRoute(ctx, LLMRouteInput{PlatformModelName: effective.PlatformModelName, TaskType: LLMTaskTypeText, Scope: LLMRouteScopeUser, Actor: run.Actor, Thread: run.Thread, RequestID: run.RequestID + ":" + requestKind})
	if err != nil {
		return nil, model.ProjectionRef{}, nil, err
	}
	if err = s.ensureRunCallBudgetWithReserve(ctx, run, effective, true, 0); err != nil {
		return route, model.ProjectionRef{}, nil, err
	}
	hostedTools, err := runHostedTools(effective, route.Protocol, enableHostedTools)
	return route, run.OutputProjection, hostedTools, err
}

func (s *Engine) finishStreamRun(ctx context.Context, run model.Run, stepID, phase string, route *LLMRoute, output *GenerateOutput, collector *runDeltaCollector) (Usage, string, error) {
	// Final flush must not leave incomplete-prefix holds unpublished when the
	// stream ended as public text; it must still refuse protocol buffers.
	if err := collector.flushFinal(); err != nil {
		return Usage{}, "", err
	}
	if err := s.recordStreamRunUsage(ctx, run, stepID, phase, route, output); err != nil {
		return usageFromGenerateOutput(output), "", err
	}
	finalText, err := finalizeStreamCollectorText(collector, output, phase)
	if err != nil {
		return usageFromGenerateOutput(output), "", err
	}
	return usageFromGenerateOutput(output), finalText, nil
}

// finalizeStreamCollectorText applies the public-text gate after streaming ends.
// Protocol markup (including streams suppressed mid-flight) never becomes final text.
func finalizeStreamCollectorText(collector *runDeltaCollector, output *GenerateOutput, phase string) (string, error) {
	finalText := ""
	if collector != nil {
		finalText = collector.content.String()
	}
	if strings.TrimSpace(finalText) == "" && output != nil {
		finalText = output.Text
	}
	if output == nil || strings.TrimSpace(finalText) == "" {
		return "", withErrorMessage(errCategoryDD926A6DAE, fmt.Sprintf("text run %s returned no result", phase))
	}
	if (collector != nil && collector.suppressed) || classifyModelText(finalText) == ModelTextToolProtocol {
		return "", errRequiredToolCallNotProduced
	}
	return finalText, nil
}

func runHostedTools(effective effectiveTextRunConfig, protocol string, enabled bool) ([]HostedTool, error) {
	if !enabled {
		return nil, nil
	}
	return hostedToolsForProtocol(effective, protocol)
}

type runDeltaCollector struct {
	service    *Engine
	ctx        context.Context
	run        model.Run
	stepID     string
	projection model.ProjectionRef
	content    strings.Builder
	buffer     strings.Builder
	lastFlush  time.Time
	// suppressed becomes true once tool-protocol markup is observed. Further
	// deltas stay in content for diagnostics but never become message.delta.
	suppressed bool
	// publishDelta overrides message.delta persistence (tests). Production uses
	// appendRunEvent when nil.
	publishDelta func(delta string) error
}

func (collector *runDeltaCollector) accept(event GenerateStreamEvent) error {
	if event.Delta == "" {
		return nil
	}
	if collector.suppressed {
		// Keep full text for finishStreamRun classification without publishing.
		collector.content.WriteString(event.Delta)
		return nil
	}
	collector.content.WriteString(event.Delta)
	if looksLikeToolProtocolText(collector.content.String()) {
		collector.suppressProtocolBuffer()
		return nil
	}
	collector.buffer.WriteString(event.Delta)
	if collector.buffer.Len() >= 2048 || time.Since(collector.lastFlush) >= 250*time.Millisecond {
		return collector.flush()
	}
	return nil
}

func (collector *runDeltaCollector) suppressProtocolBuffer() {
	collector.suppressed = true
	// Drop unflushed bytes so partial protocol markers never become deltas.
	collector.buffer.Reset()
}

func (collector *runDeltaCollector) flush() error {
	return collector.flushInternal(false)
}

func (collector *runDeltaCollector) flushFinal() error {
	return collector.flushInternal(true)
}

func (collector *runDeltaCollector) flushInternal(final bool) error {
	if collector.buffer.Len() == 0 {
		return nil
	}
	if collector.suppressed || looksLikeToolProtocolText(collector.content.String()) {
		collector.suppressProtocolBuffer()
		return nil
	}
	// Hold incomplete marker suffixes mid-stream; at end-of-stream release as public.
	if !final && hasIncompleteProtocolMarkerSuffix(collector.content.String()) {
		return nil
	}
	delta := collector.buffer.String()
	if err := collector.emitDelta(delta); err != nil {
		return err
	}
	collector.buffer.Reset()
	collector.lastFlush = time.Now()
	return nil
}

func (collector *runDeltaCollector) emitDelta(delta string) error {
	if collector.publishDelta != nil {
		return collector.publishDelta(delta)
	}
	if collector.service == nil {
		return nil
	}
	return collector.service.appendRunEvent(context.WithoutCancel(collector.ctx), &collector.run, "message.delta", collector.stepID, "", map[string]interface{}{valueDelta1F5E22EC: delta}, &collector.projection)
}

func (s *Engine) recordStreamRunUsage(ctx context.Context, run model.Run, stepID, phase string, route *LLMRoute, output *GenerateOutput) error {
	if output == nil {
		return nil
	}
	return s.recordRunLLMUsageForStep(context.WithoutCancel(ctx), run, stepID, phase, route, output)
}

func usageFromGenerateOutput(output *GenerateOutput) Usage {
	if output == nil {
		return Usage{}
	}
	return output.Usage
}

func (s *Engine) prepareOutput(ctx context.Context, run model.Run, stepID, toolCallID, outputID, title, summary, fileID, sourceToolCallID string, _ uint) (*model.OutputRef, model.Event, error) {
	current, err := s.repo.ListOutputs(ctx, run.Actor, run.RunID)
	if err != nil {
		return nil, model.Event{}, err
	}
	var effective effectiveTextRunConfig
	_ = json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective)
	outputID = strings.TrimSpace(outputID)
	if outputID == "" {
		sum := sha256.Sum256([]byte(run.RunID + "\x00" + toolCallID))
		outputID = "output_" + fmt.Sprintf("%x", sum[:16])
	}
	maxOutputs := boundedTextRunConfig(effective.OutputMaxPerRun, 50, 500)
	parentRefID, eventType := outputParentAndEvent(current, effective.OutputRefs, outputID)
	if err := enforceOutputLimit(current, outputID, maxOutputs); err != nil {
		return nil, model.Event{}, err
	}
	output := &model.OutputRef{OutputID: outputID, RunID: run.RunID, StepID: stepID, ToolCallID: toolCallID, Kind: "artifact", Title: truncateRunTitle(title), Summary: truncateRunResult(summary), FileID: strings.TrimSpace(fileID), Projection: run.OutputProjection, ParentOutputID: parentRefID, Status: model.OutputDraft}
	if output.FileID != "" {
		if err := s.validateOutputFileLineage(ctx, run, output, strings.TrimSpace(sourceToolCallID)); err != nil {
			return nil, model.Event{}, err
		}
	}
	event := newRunEvent(run, eventType, stepID, output.Title, map[string]interface{}{valueOutputID7E64D749: output.OutputID, valueKindE5B2EFB3: output.Kind, valueTitle90A9E177: output.Title, valueSummaryCE2A127F: output.Summary, "fileID": output.FileID, valueStatus327C4193: output.Status}, nil)
	if output.SourceEventID == "" {
		output.SourceEventID = event.EventID
	}
	return output, event, nil
}

func outputParentAndEvent(current []model.OutputRef, refs []effectiveRunOutputRef, outputID string) (string, string) {
	eventType := "output.created"
	for _, existing := range current {
		if existing.OutputID == outputID {
			eventType = "output.updated"
			break
		}
	}
	for _, ref := range refs {
		if ref.OutputID == outputID {
			return ref.OutputID, "output.updated"
		}
	}
	return "", eventType
}

func enforceOutputLimit(current []model.OutputRef, outputID string, maxOutputs int) error {
	for _, existing := range current {
		if existing.OutputID == outputID {
			return nil
		}
	}
	if len(current) >= maxOutputs {
		return errCategoryB59D448B11
	}
	return nil
}

func (s *Engine) validateOutputFileLineage(ctx context.Context, run model.Run, output *model.OutputRef, sourceToolCallID string) error {
	if s.attachments == nil || output == nil {
		return ErrOutputLineageInvalid
	}
	resolved, err := s.attachments.ResolveAttachments(ctx, ResolveAttachmentsRequest{
		Actor:      run.Actor,
		References: []model.ResourceRef{{Kind: valueFileBE372696, ID: output.FileID}},
	})
	if err != nil || len(resolved.Attachments) != 1 || !validOutputLineageAttachment(resolved.Attachments[0], output.FileID) {
		return ErrOutputLineageInvalid
	}
	asset := resolved.Attachments[0]
	snapshot, err := s.repo.GetRunContextSnapshot(ctx, run.Actor, run.RunID)
	if err != nil {
		return ErrRunSnapshotIncompatible
	}
	var payload textRunContextSnapshotPayload
	if json.Unmarshal([]byte(snapshot.ContentJSON), &payload) != nil || payload.SemanticVersion != RuntimeSnapshotVersion {
		return ErrRunSnapshotIncompatible
	}
	fromContext := outputComesFromRunContext(output, payload.Files, asset.SHA256, snapshot.SnapshotID)
	if err = s.applyOutputToolLineage(ctx, run, output, sourceToolCallID, fromContext); err != nil {
		return err
	}
	output.FileSHA256 = strings.ToLower(strings.TrimSpace(asset.SHA256))
	output.FileMIMEType = firstNonEmptyString(asset.DetectedMediaType, asset.MediaType)
	output.Kind = firstNonEmptyString(asset.Category, outputKindFromMIME(output.FileMIMEType), valueFileA5BAA909)
	return nil
}

func validOutputLineageAttachment(asset ResolvedAttachment, fileID string) bool {
	return asset.Ref.ID == fileID && strings.TrimSpace(asset.SHA256) != ""
}

func outputComesFromRunContext(output *model.OutputRef, files []textRunContextFileRef, sha256Value, snapshotID string) bool {
	for _, file := range files {
		if file.FileID == output.FileID && strings.EqualFold(file.SHA256, sha256Value) {
			output.SourceSnapshotID = snapshotID
			return true
		}
	}
	return false
}

func (s *Engine) applyOutputToolLineage(ctx context.Context, run model.Run, output *model.OutputRef, sourceToolCallID string, fromContext bool) error {
	if sourceToolCallID == "" {
		if !fromContext {
			return ErrOutputLineageInvalid
		}
		return nil
	}
	result, err := s.repo.GetRunToolResult(ctx, run.Actor, run.RunID, sourceToolCallID)
	if err != nil || result == nil || !outputJSONContainsString(result.OutputJSON, output.FileID, 0) {
		return ErrOutputLineageInvalid
	}
	output.SourceToolCallID = sourceToolCallID
	output.SourceEventID = result.EventID
	return nil
}

func outputJSONContainsString(raw, target string, depth int) bool {
	if depth > 16 || strings.TrimSpace(target) == "" {
		return false
	}
	var value interface{}
	if json.Unmarshal([]byte(raw), &value) != nil {
		return false
	}
	return outputValueContainsString(value, target, depth)
}

func outputValueContainsString(value interface{}, target string, depth int) bool {
	if depth > 16 {
		return false
	}
	switch typed := value.(type) {
	case string:
		return typed == target
	case []interface{}:
		for _, child := range typed {
			if outputValueContainsString(child, target, depth+1) {
				return true
			}
		}
	case map[string]interface{}:
		for _, child := range typed {
			if outputValueContainsString(child, target, depth+1) {
				return true
			}
		}
	}
	return false
}

func outputKindFromMIME(mimeType string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(mimeType), "image/"):
		return valueImageB8C50585
	case strings.HasPrefix(strings.ToLower(mimeType), "audio/"):
		return "audio"
	case strings.HasPrefix(strings.ToLower(mimeType), "video/"):
		return "video"
	default:
		return valueFileA5BAA909
	}
}

func (s *Engine) failTextRun(ctx context.Context, run model.Run, stepID string, err error) {
	if err == nil {
		return
	}
	if s.isRunCanceled(ctx, run.RunID) || errors.Is(err, context.Canceled) {
		_ = s.cancelTextRun(ctx, run, stepID, ErrRunCanceled.Error())
		return
	}
	errorCode := runFailureCode(err)
	assistantContent := s.failedAssistantContent(ctx, run, err)
	diagnosticJSON := upstreamFailureDiagnosticJSON(err, run)
	events, _, finalizeErr := s.finalizeRunWithProjection(ctx, run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalFailed, CurrentStepID: stepID, Summary: err.Error(), ErrorCode: errorCode, ErrorMessage: err.Error(), DiagnosticJSON: diagnosticJSON}, assistantContent)
	if finalizeErr != nil {
		s.logger.Error("finalize_text_runtime_failure_failed", String("run_id", run.RunID), Error(finalizeErr))
		return
	}
	s.publishRunEvents(run.RunID, events)
}

// upstreamFailureDiagnosticJSON captures sanitized upstream rejection detail for
// admin/workbench inspection without changing end-user assistant copy.
func upstreamFailureDiagnosticJSON(err error, run model.Run) string {
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream == nil {
		return ""
	}
	payload := map[string]interface{}{"upstreamStatusCode": upstream.StatusCode}
	setTrimmed(payload, "upstreamErrorType", upstream.ErrorType)
	setTrimmed(payload, "providerRequestID", upstream.RequestID)
	setTrimmed(payload, "upstreamBody", upstream.Body)
	setTrimmed(payload, "protocol", run.ProviderProtocol)
	setTrimmed(payload, "platformModelName", run.PlatformModelName)
	attachRunConfigDiagnostic(payload, run.RunConfigSnapshotJSON)
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return ""
	}
	return string(raw)
}

func setTrimmed(payload map[string]interface{}, key, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		payload[key] = trimmed
	}
}

func attachRunConfigDiagnostic(payload map[string]interface{}, snapshotJSON string) {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(snapshotJSON), &effective) != nil {
		return
	}
	if effective.Workspace != nil && effective.Workspace.Request.Directive != nil {
		setTrimmed(payload, "workspaceActionID", effective.Workspace.Request.Directive.ActionID)
	}
	if names := providerToolNamesFromEffective(effective); len(names) > 0 {
		payload["providerToolNames"] = names
	}
}

func providerToolNamesFromEffective(effective effectiveTextRunConfig) []string {
	names := make([]string, 0, len(effective.ToolPolicies)+2)
	for _, policy := range effective.ToolPolicies {
		name := strings.TrimSpace(policy.ModelName)
		if name == "" {
			name = strings.TrimSpace(policy.OriginalName)
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return append(names, runControlAskUser, runControlPublishOutput)
}

func runFailureCode(err error) string {
	var workspaceErr *WorkspaceError
	if errors.As(err, &workspaceErr) && workspaceErr.Code() != "" {
		return workspaceErr.Code()
	}
	switch {
	case errors.Is(err, errPlanBudgetExceeded):
		return "plan_budget_exceeded"
	case errors.Is(err, errPlanInvalid):
		return "plan_invalid"
	case errors.Is(err, ErrWorkspaceArtifactMissing):
		return errorCodeWorkspaceArtifactMissing
	case errors.Is(err, errRepeatedDeterministicWorkspaceToolFailure):
		return "repeated_deterministic_tool_failure"
	case errors.Is(err, errRequiredToolCallNotProduced):
		return "required_tool_call_not_parsed"
	default:
		return "run_execution_failed"
	}
}

func (s *Engine) failedAssistantContent(ctx context.Context, run model.Run, err error) string {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) != nil || effective.Workspace == nil {
		if errors.Is(err, errRequiredToolCallNotProduced) {
			return "未能完成本次操作：模型返回的工具调用格式无法被系统识别。"
		}
		return "本次操作未完成，请稍后重试。"
	}
	if errors.Is(err, errRequiredToolCallNotProduced) {
		if content := strings.TrimSpace(effective.Workspace.Policy.Failure.RequiredToolCallAssistantContent); content != "" {
			return content
		}
		return "未能完成本次工作区操作：模型返回的工具调用格式无法被系统识别，且没有发布所需产物。"
	}
	var workspaceErr *WorkspaceError
	if errors.As(err, &workspaceErr) {
		if content := workspaceErr.AssistantContent(errors.Is(err, errRepeatedDeterministicWorkspaceToolFailure)); content != "" {
			return content
		}
	}
	if content := strings.TrimSpace(effective.Workspace.Policy.Failure.DefaultAssistantContent); content != "" {
		return content
	}
	return "本次工作区操作未完成，未发布所需产物。"
}

func (s *Engine) cancelTextRun(ctx context.Context, run model.Run, stepID, reason string) error {
	events, _, err := s.finalizeRunWithProjection(ctx, run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalCancelled, CurrentStepID: stepID, Summary: reason, ErrorCode: "run_cancelled", ErrorMessage: reason}, "")
	if err != nil {
		s.logger.Error("finalize_text_runtime_cancel_failed", String("run_id", run.RunID), Error(err))
		return err
	}
	s.publishRunEvents(run.RunID, events)
	return nil
}

// RetireTextRun deliberately abandons recovery for a suspended run. It does
// not inspect the checkpoint manifest, so a corrupt checkpoint cannot trap a
// conversation queue forever.
func (s *Engine) RetireTextRun(ctx context.Context, actor model.ActorRef, runID string) (*model.Run, bool, error) {
	run, err := s.repo.GetRun(ctx, actor, normalizeRunID(runID))
	if err != nil {
		return nil, false, err
	}
	if run.Status == model.RunStatusCancelled {
		return run, true, nil
	}
	if run.Status != model.RunStatusSuspended {
		return nil, false, ErrRunRetireConflict
	}
	events, applied, err := s.finalizeRunWithProjection(ctx, *run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalCancelled, CurrentStepID: run.CurrentStepID, Summary: "Suspended text run retired", ErrorCode: "text_run_retired", ErrorMessage: "Text run recovery was abandoned by the user", Retire: true}, "")
	if err != nil {
		return nil, false, err
	}
	s.publishRunEvents(run.RunID, events)
	s.FinishRunNotifications(run.RunID)
	updated, err := s.repo.GetRun(ctx, actor, run.RunID)
	if err != nil {
		return nil, false, err
	}
	return updated, !applied, nil
}

func (s *Engine) completeTextRun(ctx context.Context, run model.Run, rootStepID string, effective effectiveTextRunConfig, finalText string) error {
	if err := s.validateRequiredWorkspaceArtifact(ctx, run, effective); err != nil {
		return err
	}
	events, _, err := s.finalizeRunWithProjection(ctx, run, model.TerminalIntent{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, Outcome: model.TerminalCompleted, CurrentStepID: rootStepID, Summary: "Text run completed"}, finalText)
	if err != nil {
		return err
	}
	s.publishRunEvents(run.RunID, events)
	return nil
}

func (s *Engine) finalizeRunWithProjection(ctx context.Context, run model.Run, intent model.TerminalIntent, content string) ([]model.Event, bool, error) {
	if s.unitOfWork == nil || s.turnProjections == nil {
		return nil, false, ErrHostProjectionUnavailable
	}
	intent.Actor, intent.Thread, intent.RunID = run.Actor, run.Thread, run.RunID
	var events []model.Event
	var applied bool
	err := s.unitOfWork.Within(ctx, func(txCtx context.Context) error {
		var err error
		_, events, applied, err = s.repo.FinalizeRun(txCtx, intent)
		if err != nil || !applied {
			return err
		}
		persisted, err := s.repo.GetRun(txCtx, run.Actor, run.RunID)
		if err != nil {
			return err
		}
		projection := TurnProjection{Input: persisted.InputProjection, Output: persisted.OutputProjection}
		usage := TurnUsage{InputTokens: persisted.InputTokens, OutputTokens: persisted.OutputTokens, CacheReadTokens: persisted.CacheReadTokens, CacheWriteTokens: persisted.CacheWriteTokens, ReasoningTokens: persisted.ReasoningTokens, LatencyMS: persisted.TotalLatencyMS, BilledCurrency: persisted.BilledCurrency, BilledNanousd: persisted.BilledNanousd, PricingSnapshot: persisted.LastBillingSnapshotJSON}
		switch intent.Outcome {
		case model.TerminalCompleted:
			_, err = s.turnProjections.CompleteTurn(txCtx, CompleteTurnRequest{Actor: persisted.Actor, Thread: persisted.Thread, RunID: persisted.RunID, Projection: projection, ContentType: "text", Content: content, Usage: usage})
		case model.TerminalFailed:
			_, err = s.turnProjections.FailTurn(txCtx, FailTurnRequest{Actor: persisted.Actor, Thread: persisted.Thread, RunID: persisted.RunID, Projection: projection, ContentType: "text", Content: content, Usage: usage, ErrorCode: intent.ErrorCode, ErrorMessage: intent.ErrorMessage})
		case model.TerminalCancelled:
			_, err = s.turnProjections.CancelTurn(txCtx, CancelTurnRequest{Actor: persisted.Actor, Thread: persisted.Thread, RunID: persisted.RunID, Projection: projection, ErrorCode: intent.ErrorCode, ErrorMessage: intent.ErrorMessage})
		default:
			err = ErrInvalidInput
		}
		if err == nil {
			if tracker, ok := s.repo.(HostProjectionTracker); ok {
				err = tracker.MarkHostProjectionRepaired(txCtx, persisted.RunID)
			}
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return events, applied, nil
}

func (s *Engine) validateRequiredWorkspaceArtifact(ctx context.Context, run model.Run, effective effectiveTextRunConfig) error {
	if effective.Workspace == nil || effective.Workspace.SchemaVersion != RuntimeSnapshotVersion || !effective.Workspace.Policy.RequiredArtifact {
		return nil
	}
	outputs, err := s.repo.ListOutputs(ctx, run.Actor, run.RunID)
	if err != nil {
		return err
	}
	for _, output := range outputs {
		var preview map[string]interface{}
		if json.Unmarshal([]byte(output.PreviewJSON), &preview) != nil || !containsRuntimeString(effective.Workspace.Policy.TerminalArtifactTypes, strings.TrimSpace(fmt.Sprint(preview["artifactType"]))) {
			continue
		}
		field := strings.TrimSpace(effective.Workspace.Policy.ArtifactResourceField)
		if field == "" || strings.TrimSpace(fmt.Sprint(preview[field])) == effective.Workspace.Request.ResourceID {
			return nil
		}
	}
	code := strings.TrimSpace(effective.Workspace.Policy.Failure.RequiredArtifactErrorCode)
	if code == "" {
		code = errorCodeWorkspaceArtifactMissing
	}
	return NewWorkspaceError(WorkspaceErrorClassification{
		Kind:             WorkspaceErrorRequiredArtifact,
		Code:             code,
		Message:          ErrWorkspaceArtifactMissing.Error(),
		AssistantContent: effective.Workspace.Policy.Failure.DefaultAssistantContent,
	}, ErrWorkspaceArtifactMissing)
}

func runUsageFromUsage(usage Usage) runUsage {
	return runUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, CacheWrite5mTokens: usage.CacheWrite5mTokens, CacheWrite1hTokens: usage.CacheWrite1hTokens, ReasoningTokens: usage.ReasoningTokens, RawUsageJSON: usage.RawUsageJSON, UsageSpeed: usage.Speed, UsageServiceTier: usage.ServiceTier, BillingRateClass: usage.BillingRateClass}
}

func addRunUsage(left, right runUsage) runUsage {
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.CacheReadTokens += right.CacheReadTokens
	left.CacheWriteTokens += right.CacheWriteTokens
	left.CacheWrite5mTokens += right.CacheWrite5mTokens
	left.CacheWrite1hTokens += right.CacheWrite1hTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.ServerSideToolUsage = mergeRunToolUsage(left.ServerSideToolUsage, right.ServerSideToolUsage)
	left.ServiceItems = append(left.ServiceItems, right.ServiceItems...)
	left.RawUsageJSON = MergeRawUsageJSON(left.RawUsageJSON, right.RawUsageJSON)
	if strings.TrimSpace(right.UsageSpeed) != "" {
		left.UsageSpeed = right.UsageSpeed
	}
	if strings.TrimSpace(right.UsageServiceTier) != "" {
		left.UsageServiceTier = right.UsageServiceTier
	}
	if strings.TrimSpace(right.BillingRateClass) != "" {
		left.BillingRateClass = right.BillingRateClass
	}
	return left
}

func mergeRunToolUsage(left, right map[string]int64) map[string]int64 {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	result := make(map[string]int64, len(left)+len(right))
	for key, count := range left {
		if strings.TrimSpace(key) != "" && count > 0 {
			result[key] += count
		}
	}
	for key, count := range right {
		if strings.TrimSpace(key) != "" && count > 0 {
			result[key] += count
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func runUsageHasData(value runUsage) bool {
	return value.InputTokens != 0 || value.OutputTokens != 0 || value.CacheReadTokens != 0 || value.CacheWriteTokens != 0 || value.CacheWrite5mTokens != 0 || value.CacheWrite1hTokens != 0 || value.ReasoningTokens != 0 || len(value.ServerSideToolUsage) != 0 || len(value.ServiceItems) != 0 || strings.TrimSpace(value.RawUsageJSON) != ""
}

func mergeRunUsage(left, right Usage) Usage {
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.CacheReadTokens += right.CacheReadTokens
	left.CacheWriteTokens += right.CacheWriteTokens
	left.CacheWrite5mTokens += right.CacheWrite5mTokens
	left.CacheWrite1hTokens += right.CacheWrite1hTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.RawUsageJSON = MergeRawUsageJSON(left.RawUsageJSON, right.RawUsageJSON)
	if strings.TrimSpace(right.Speed) != "" {
		left.Speed = right.Speed
	}
	if strings.TrimSpace(right.ServiceTier) != "" {
		left.ServiceTier = right.ServiceTier
	}
	if strings.TrimSpace(right.BillingRateClass) != "" {
		left.BillingRateClass = right.BillingRateClass
	}
	return left
}

func (s *Engine) settleRunSegment(ctx context.Context, run model.Run, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, route *LLMRoute, usage runUsage) error {
	usage, route, err := s.resolveRunSegmentUsage(ctx, run, usage, route)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段用量读取失败退回预扣")
		return err
	}
	result := s.runSegmentBillingResult(effective, route, usage)
	if s.billingSvc == nil {
		return s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段未配置计费服务退回预扣")
	}
	segmentKey := runSegmentKey(ctx, run)
	ledger, billable, err := s.buildRunUsageLedger(ctx, RunBillingInput{Actor: run.Actor, Thread: run.Thread, PlatformModelName: effective.PlatformModelName, ClientRunID: segmentKey, Usage: runTurnUsage(usage), Result: result})
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段计价失败退回预扣")
		return err
	}
	if !billable {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段无计费用量退回预扣")
		return nil
	}
	if err = s.billingSvc.RecordUsageWithReservation(ctx, ledger, reservation); err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "文本运行分段计费失败退回预扣")
		return err
	}
	return s.appendRunBillingWithRetry(ctx, run, segmentKey, ledger)
}

func (s *Engine) resolveRunSegmentUsage(ctx context.Context, run model.Run, usage runUsage, route *LLMRoute) (runUsage, *LLMRoute, error) {
	durableUsage, durableRoute, err := s.runSegmentUsage(ctx, run)
	if err != nil || !runUsageHasData(durableUsage) {
		return usage, route, err
	}
	if route == nil {
		route = durableRoute
	}
	return durableUsage, route, nil
}

func (s *Engine) runSegmentBillingResult(effective effectiveTextRunConfig, route *LLMRoute, usage runUsage) *RunMessageResult {
	result := &RunMessageResult{Billable: true, PlatformModelName: effective.PlatformModelName, EffectiveOptions: effective.Options, CacheWrite5mTokens: usage.CacheWrite5mTokens, CacheWrite1hTokens: usage.CacheWrite1hTokens, UsageSpeed: usage.UsageSpeed, UsageServiceTier: usage.UsageServiceTier, BillingRateClass: usage.BillingRateClass, RawUsageJSON: usage.RawUsageJSON, ServerSideToolUsage: usage.ServerSideToolUsage, ServiceItems: usage.ServiceItems}
	if route != nil {
		result.UpstreamID, result.UpstreamName, result.RoutedBindingCode, result.UpstreamModelName, result.UpstreamProtocol = route.UpstreamID, route.UpstreamName, route.BindingCode, route.UpstreamModel, route.Protocol
	}
	return result
}

func runTurnUsage(usage runUsage) TurnUsage {
	return TurnUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens, ReasoningTokens: usage.ReasoningTokens}
}

func (s *Engine) appendRunBillingWithRetry(ctx context.Context, run model.Run, segmentKey string, ledger *UsageLedger) error {
	eventIDSum := sha256.Sum256([]byte(segmentKey))
	event := newRunEvent(run, "billing.updated", run.CurrentStepID, "Text Run segment billed", nil, nil)
	event.EventID = "evt_billing_" + fmt.Sprintf("%x", eventIDSum[:16])
	var saved *model.Event
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		saved, _, err = s.repo.AppendRunBilling(ctx, run.RunID, segmentKey, ledger.BilledCurrency, ledger.BilledNanousd, ledger.PricingSnapshotJSON, event)
		if err == nil {
			break
		}
		if attempt < 4 {
			timer := time.NewTimer(time.Duration(1<<attempt) * 5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err != nil {
		return err
	}
	s.publishRunEvents(run.RunID, []model.Event{*saved})
	return nil
}

func (s *Engine) runSegmentUsage(ctx context.Context, run model.Run) (runUsage, *LLMRoute, error) {
	accumulator := runSegmentUsageAccumulator{segmentKey: runSegmentKey(ctx, run), seenToolCalls: make(map[string]struct{})}
	var cursor int64
	for {
		events, err := s.repo.ListRunEventsAfter(ctx, run.Actor, run.RunID, cursor, 1000)
		if err != nil {
			return runUsage{}, nil, err
		}
		for _, event := range events {
			cursor = int64(event.Seq)
			accumulator.apply(event)
		}
		if len(events) < 1000 {
			return accumulator.total, accumulator.route, nil
		}
	}
}

type runSegmentUsageAccumulator struct {
	segmentKey    string
	total         runUsage
	route         *LLMRoute
	seenToolCalls map[string]struct{}
}

type runSegmentToolPayload struct {
	SegmentKey   string `json:"segmentKey"`
	ToolKey      string `json:"toolKey"`
	ProviderKind string `json:"providerKind"`
}

type runSegmentUsagePayload struct {
	SegmentKey                                                                    string `json:"segmentKey"`
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens, ReasoningTokens int64
	CacheWrite5mTokens, CacheWrite1hTokens                                        int64
	ServerSideToolUsage                                                           map[string]int64 `json:"serverSideToolUsage"`
	RawUsageJSON, UsageSpeed, UsageServiceTier, BillingRateClass                  string
	UpstreamID                                                                    uint   `json:"upstreamID"`
	UpstreamName                                                                  string `json:"upstreamName"`
	BindingCode                                                                   string `json:"bindingCode"`
	UpstreamModel                                                                 string `json:"upstreamModel"`
	Protocol                                                                      string `json:"protocol"`
}

func (accumulator *runSegmentUsageAccumulator) apply(event model.Event) {
	if event.EventType == valueToolCompleted8D0A12FD {
		accumulator.applyTool(event)
		return
	}
	if event.EventType == valueUsageUpdatedABC8B0B2 {
		accumulator.applyUsage(event)
	}
}

func (accumulator *runSegmentUsageAccumulator) applyTool(event model.Event) {
	var payload runSegmentToolPayload
	if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.SegmentKey != accumulator.segmentKey || strings.TrimSpace(payload.ToolKey) == "" {
		return
	}
	callID := strings.TrimSpace(event.ToolCallID)
	if _, duplicate := accumulator.seenToolCalls[callID]; callID == "" || duplicate {
		return
	}
	accumulator.seenToolCalls[callID] = struct{}{}
	accumulator.total.ServiceItems = append(accumulator.total.ServiceItems, ServiceUsageInput{ServiceCode: "tool." + payload.ToolKey, ServiceName: payload.ToolKey, ProviderProtocol: payload.ProviderKind, CallCount: 1})
}

func (accumulator *runSegmentUsageAccumulator) applyUsage(event model.Event) {
	var payload runSegmentUsagePayload
	if json.Unmarshal([]byte(event.PayloadJSON), &payload) != nil || payload.SegmentKey != accumulator.segmentKey {
		return
	}
	accumulator.total = addRunUsage(accumulator.total, runUsage{InputTokens: payload.InputTokens, OutputTokens: payload.OutputTokens, CacheReadTokens: payload.CacheReadTokens, CacheWriteTokens: payload.CacheWriteTokens, CacheWrite5mTokens: payload.CacheWrite5mTokens, CacheWrite1hTokens: payload.CacheWrite1hTokens, ReasoningTokens: payload.ReasoningTokens, ServerSideToolUsage: payload.ServerSideToolUsage, RawUsageJSON: payload.RawUsageJSON, UsageSpeed: payload.UsageSpeed, UsageServiceTier: payload.UsageServiceTier, BillingRateClass: payload.BillingRateClass})
	if payload.UpstreamID != 0 || payload.UpstreamModel != "" {
		accumulator.route = &LLMRoute{UpstreamID: payload.UpstreamID, UpstreamName: payload.UpstreamName, BindingCode: payload.BindingCode, UpstreamModel: payload.UpstreamModel, Protocol: payload.Protocol}
	}
}

func truncateRunResult(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return string(runes)
}

func canonicalRunJSON(raw json.RawMessage) string {
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return strings.TrimSpace(string(raw))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(encoded)
}

func (s *Engine) GetPlan(ctx context.Context, actor model.ActorRef, runID string) (*PlanView, error) {
	current, err := s.repo.GetCurrentPlan(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	revisions, err := s.repo.ListPlans(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	steps, err := s.repo.ListRunSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	return &PlanView{Current: current, Revisions: revisions, Steps: steps}, nil
}

func (s *Engine) ListRunInteractions(ctx context.Context, actor model.ActorRef, runID string) ([]model.Interaction, error) {
	return s.repo.ListRunInteractions(ctx, actor, runID)
}

func (s *Engine) ListRunCheckpoints(ctx context.Context, actor model.ActorRef, runID string) ([]model.Checkpoint, error) {
	return s.repo.ListRunCheckpoints(ctx, actor, runID)
}

func (s *Engine) ListOutputs(ctx context.Context, actor model.ActorRef, runID string) ([]model.OutputRef, error) {
	return s.repo.ListOutputs(ctx, actor, runID)
}

func (s *Engine) ListUserOutputs(ctx context.Context, actor model.ActorRef, query, cursor string, limit int) ([]model.OutputListItem, string, error) {
	if !validActorRef(actor) {
		return nil, "", ErrInvalidInput
	}
	return s.repo.ListUserOutputs(ctx, actor, strings.TrimSpace(query), strings.TrimSpace(cursor), limit)
}

func (s *Engine) ResolveRunInteraction(ctx context.Context, input ResolveRunInteractionInput) (*model.Interaction, error) {
	prepared, err := s.prepareInteractionResolution(ctx, input)
	if err != nil {
		return nil, err
	}
	bundle, err := s.buildInteractionResolutionBundle(ctx, input, prepared.run, prepared.interaction, prepared.resolution)
	if err != nil {
		return nil, err
	}
	effective, err := s.validateInteractionResolutionRuntime(ctx, prepared.run)
	if err != nil {
		return nil, err
	}
	var reservation *UsageBalanceReservation
	if prepared.resolution.shouldContinue {
		reservation, _, err = s.ReserveRunUsageBalance(ctx, RunBillingInput{Actor: prepared.run.Actor, Thread: prepared.run.Thread, PlatformModelName: effective.PlatformModelName, ClientRunID: prepared.run.RunID + ":resolve:" + input.ClientResolveID})
		if err != nil {
			return nil, err
		}
	}
	return s.commitInteractionResolution(ctx, input, prepared.run, prepared.interaction, effective, prepared.responseJSON, prepared.fingerprint, prepared.resolution, bundle, reservation)
}

type preparedInteractionResolution struct {
	run          model.Run
	interaction  model.Interaction
	responseJSON string
	fingerprint  string
	resolution   interactionResolution
}

func (s *Engine) prepareInteractionResolution(ctx context.Context, input ResolveRunInteractionInput) (preparedInteractionResolution, error) {
	if !validResolveRunInteractionInput(input) {
		return preparedInteractionResolution{}, ErrInvalidInput
	}
	run, err := s.repo.GetRun(ctx, input.Actor, input.RunID)
	if err != nil {
		return preparedInteractionResolution{}, err
	}
	interaction, err := s.repo.GetRunInteraction(ctx, input.Actor, input.RunID, input.InteractionID)
	if err != nil {
		return preparedInteractionResolution{}, err
	}
	responseJSON, responseMap, err := normalizeRunInteractionResponse(input.Response)
	if err != nil {
		return preparedInteractionResolution{}, ErrRunInteractionResponseInvalid
	}
	if err = validateRunInteractionResponse(interaction.ResponseSchemaJSON, responseMap); err != nil {
		return preparedInteractionResolution{}, err
	}
	resolution := newInteractionResolution(*run, *interaction, responseJSON, responseMap)
	if err = s.applyInteractionResolution(ctx, input, *run, *interaction, responseMap, &resolution); err != nil {
		return preparedInteractionResolution{}, err
	}
	if err = s.appendSupersededPlanSteps(ctx, *run, &resolution); err != nil {
		return preparedInteractionResolution{}, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(responseJSON)))
	return preparedInteractionResolution{run: *run, interaction: *interaction, responseJSON: responseJSON, fingerprint: fingerprint, resolution: resolution}, nil
}

type interactionResolutionBundle struct {
	checkpoint *model.Checkpoint
	stepID     string
	events     []model.Event
}

func (s *Engine) buildInteractionResolutionBundle(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, resolution interactionResolution) (interactionResolutionBundle, error) {
	bundle := interactionResolutionBundle{stepID: interaction.StepID, events: resolution.events}
	if !resolution.shouldContinue {
		return bundle, nil
	}
	if resolution.reviseFeedback != "" {
		root, err := s.runRootStep(ctx, run.RunID)
		if err != nil {
			return interactionResolutionBundle{}, err
		}
		bundle.stepID = root.StepID
	}
	continuation, err := buildRunResolutionContinuation(run, interaction, bundle.stepID, run.RunID+":resolve:"+input.ClientResolveID, resolution.reviseFeedback, resolution.nextRevision, resolution.approvedTool, resolution.frozenApprovedTool)
	if err != nil {
		return interactionResolutionBundle{}, err
	}
	bundle.checkpoint = newRunContinuationCheckpoint(run, bundle.stepID, "post_interaction", continuation)
	bundle.checkpoint.CheckpointID = deterministicRunCheckpointID(run.RunID, interaction.InteractionID, input.ClientResolveID, "post_interaction")
	bundle.events = append(bundle.events, newRunEvent(run, "checkpoint.created", bundle.stepID, "Post-interaction execution checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: bundle.checkpoint.CheckpointID, valueKindE5B2EFB3: bundle.checkpoint.Kind, valueContinuationTypeDCB4DE9C: continuation.Type}, nil))
	resumed := newRunEvent(run, "run.resumed", bundle.stepID, "Text run resumed", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueStatus327C4193: resolution.nextStatus}, nil)
	resumed.Status = resolution.nextStatus
	bundle.events = append(bundle.events, resumed)
	return bundle, nil
}

func (s *Engine) validateInteractionResolutionRuntime(ctx context.Context, run model.Run) (effectiveTextRunConfig, error) {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) != nil || effective.SemanticVersion != RuntimeSnapshotVersion {
		return effectiveTextRunConfig{}, ErrInvalidInput
	}
	if _, err := s.loadTextRunContextMessages(ctx, run); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	if _, err := s.resolveRunTools(ctx, run.Actor, effective); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	return effective, nil
}

func (s *Engine) commitInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, effective effectiveTextRunConfig, responseJSON, fingerprint string, resolution interactionResolution, bundle interactionResolutionBundle, reservation *UsageBalanceReservation) (*model.Interaction, error) {
	resolved, continuationCheckpoint, saved, applied, err := s.repo.ResolveRunInteractionWithCheckpoint(ctx, input.Actor, input.RunID, input.InteractionID, input.ClientResolveID, responseJSON, fingerprint, resolution.nextStatus, bundle.checkpoint, bundle.events)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行交互解决失败退回预扣")
		return nil, err
	}
	if !applied {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行交互幂等复用退回预扣")
		return resolved, nil
	}
	s.publishRunEvents(run.RunID, saved)
	if !resolution.shouldContinue {
		_ = s.cancelTextRun(context.WithoutCancel(ctx), run, interaction.StepID, "Interaction rejected")
		s.FinishRunNotifications(run.RunID)
		return resolved, nil
	}
	if continuationCheckpoint == nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行恢复缺少后继检查点退回预扣")
		return nil, ErrRunSnapshotIncompatible
	}
	root, rootErr := s.runRootStep(ctx, run.RunID)
	if rootErr != nil {
		s.failTextRun(context.WithoutCancel(ctx), run, run.CurrentStepID, rootErr)
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行恢复缺少根步骤退回预扣")
		s.FinishRunNotifications(run.RunID)
		return nil, rootErr
	}
	continuation, continuationErr := decodeRunContinuation(*continuationCheckpoint)
	if continuationErr != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行恢复后继检查点不兼容退回预扣")
		return nil, ErrRunSnapshotIncompatible
	}
	segmentCtx := context.WithValue(context.Background(), runSegmentKeyContextKey{}, continuation.SegmentKey)
	run.Status, run.PendingInteractionID = resolution.nextStatus, ""
	s.launchRunContinuation(func() {
		s.executeRunContinuation(segmentCtx, run, root, effective, reservation, *continuationCheckpoint, "interaction_resolve")
	})
	return resolved, nil
}

func validResolveRunInteractionInput(input ResolveRunInteractionInput) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.RunID) != "" && strings.TrimSpace(input.InteractionID) != "" && strings.TrimSpace(input.ClientResolveID) != "" && len(input.ClientResolveID) <= 64
}

type interactionResolution struct {
	nextStatus         string
	events             []model.Event
	shouldContinue     bool
	reviseFeedback     string
	approvedTool       bool
	nextRevision       int
	frozenApprovedTool *runFrozenToolCall
}

func newInteractionResolution(run model.Run, interaction model.Interaction, responseJSON string, response map[string]interface{}) interactionResolution {
	events := make([]model.Event, 0, 4)
	if interaction.Type == model.InteractionAskUser {
		toolResult := newRunEvent(run, valueToolCompleted8D0A12FD, interaction.StepID, runControlAskUser, map[string]interface{}{valueToolCallID64CA70DB: interaction.ToolCallID, valueToolName4234B607: runControlAskUser, valueAnswer89191F03: response[valueAnswer89191F03]}, nil)
		toolResult.ToolCallID, toolResult.ToolName, toolResult.OutputJSON = interaction.ToolCallID, runControlAskUser, responseJSON
		events = append(events, toolResult)
	}
	events = append(events, newRunEvent(run, "interaction.resolved", interaction.StepID, "Interaction resolved", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type}, nil))
	return interactionResolution{nextStatus: model.RunStatusRunning, events: events, shouldContinue: true}
}

func (s *Engine) applyInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	switch interaction.Type {
	case model.InteractionSubmitPlan:
		return s.applyPlanInteractionResolution(ctx, input, run, interaction, response, resolution)
	case model.InteractionAskUser:
		return validateAskUserResolution(response)
	case model.InteractionApproveTool:
		return applyToolInteractionResolution(run, interaction, response, resolution)
	case model.InteractionApproveToolSet:
		return applyToolSetInteractionResolution(run, interaction, response, resolution)
	case model.InteractionApproveStep:
		return s.applyStepInteractionResolution(ctx, input, run, interaction, response, resolution)
	default:
		return ErrInvalidInput
	}
}

func (s *Engine) applyPlanInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	action, _ := response["action"].(string)
	switch strings.TrimSpace(action) {
	case valueApproveFF07A766:
		resolution.events = append(resolution.events, newRunEvent(run, "plan.approved", interaction.StepID, "Plan approved", map[string]interface{}{valuePlanID320F2BB9: run.CurrentPlanID, valueMode06EC588F: valueUser19341906}, nil))
		return nil
	case valueRevise9EA811FD:
		feedback, _ := response[valueFeedback83F69355].(string)
		return s.applyPlanRevision(ctx, input, run, interaction, feedback, false, resolution)
	case "reject":
		resolution.shouldContinue = false
		resolution.events = append(resolution.events, newRunEvent(run, "plan.rejected", interaction.StepID, "Plan rejected", map[string]interface{}{valuePlanID320F2BB9: run.CurrentPlanID}, nil))
		return nil
	default:
		return ErrInvalidInput
	}
}

func validateAskUserResolution(response map[string]interface{}) error {
	answer, _ := response[valueAnswer89191F03].(string)
	if strings.TrimSpace(answer) == "" {
		return ErrRunInteractionResponseInvalid
	}
	return nil
}

func applyToolInteractionResolution(run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	resolution.approvedTool, _ = response["approved"].(bool)
	var frozen runFrozenToolCall
	if err := json.Unmarshal([]byte(interaction.RequestPayloadJSON), &frozen); err != nil || frozen.ToolCallID == "" {
		return ErrRunSnapshotIncompatible
	}
	if resolution.approvedTool {
		resolution.frozenApprovedTool = &frozen
		return nil
	}
	denied := newRunEvent(run, valueToolFailedFB145984, interaction.StepID, "Tool execution denied by user", map[string]interface{}{valueToolCallID64CA70DB: frozen.ToolCallID, valueToolName4234B607: frozen.ToolName, valueStatus327C4193: "user_denied"}, nil)
	denied.ToolCallID, denied.ToolName = frozen.ToolCallID, frozen.ToolName
	denied.ErrorJSON = mustRunJSON(map[string]interface{}{valueErrorA8DE48C2: "user_denied"})
	resolution.events = append([]model.Event{denied}, resolution.events...)
	return nil
}

func applyToolSetInteractionResolution(run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	approved, _ := response["approved"].(bool)
	if !approved {
		resolution.shouldContinue = false
		resolution.events = append(resolution.events, newRunEvent(run, "tool_set.rejected", interaction.StepID, "Hosted tool activation rejected", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil))
		return nil
	}
	var request struct {
		ContinuationType string `json:"continuationType"`
	}
	if json.Unmarshal([]byte(interaction.RequestPayloadJSON), &request) != nil {
		return ErrRunSnapshotIncompatible
	}
	if request.ContinuationType == runContinuationStartPlanning {
		resolution.nextStatus = model.RunStatusPreparing
	}
	resolution.events = append(resolution.events, newRunEvent(run, "tool_set.approved", interaction.StepID, "Hosted tool activation approved", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID}, nil))
	return nil
}

func (s *Engine) applyStepInteractionResolution(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, response map[string]interface{}, resolution *interactionResolution) error {
	action, _ := response["action"].(string)
	switch strings.TrimSpace(action) {
	case valueApproveFF07A766:
		resolution.events = append(resolution.events, newRunEvent(run, "step.approved", interaction.StepID, "Step approved", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueStepID23C5C586: interaction.StepID}, nil))
		return nil
	case valueRevise9EA811FD:
		feedback, _ := response[valueFeedback83F69355].(string)
		return s.applyPlanRevision(ctx, input, run, interaction, feedback, true, resolution)
	default:
		return ErrInvalidInput
	}
}

func (s *Engine) applyPlanRevision(ctx context.Context, input ResolveRunInteractionInput, run model.Run, interaction model.Interaction, feedback string, stepRevision bool, resolution *interactionResolution) error {
	if strings.TrimSpace(feedback) == "" {
		return ErrRunInteractionResponseInvalid
	}
	var config effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &config) != nil {
		if stepRevision {
			return ErrRunSnapshotIncompatible
		}
		return ErrInvalidInput
	}
	plans, err := s.repo.ListPlans(ctx, input.Actor, input.RunID)
	if err != nil {
		return err
	}
	if len(plans) >= config.PlanMaxRevisions {
		return ErrPlanRevisionLimit
	}
	resolution.reviseFeedback, resolution.nextRevision, resolution.nextStatus = feedback, len(plans)+1, model.RunStatusPreparing
	payload := map[string]interface{}{valuePlanID320F2BB9: run.CurrentPlanID, valueFeedback83F69355: feedback}
	title := "Plan revision requested"
	if stepRevision {
		payload[valueStepID23C5C586], title = interaction.StepID, "Step revision requested"
	}
	resolution.events = append(resolution.events, newRunEvent(run, "plan.revised", interaction.StepID, title, payload, nil))
	return nil
}

func (s *Engine) appendSupersededPlanSteps(ctx context.Context, run model.Run, resolution *interactionResolution) error {
	if resolution.reviseFeedback == "" {
		return nil
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return err
	}
	for _, step := range steps {
		if step.PlanID == run.CurrentPlanID && supersedableRunStepStatus(step.Status) {
			resolution.events = append(resolution.events, newRunEvent(run, "step.skipped", step.StepID, "Plan superseded by user feedback", map[string]interface{}{valuePlanID320F2BB9: step.PlanID, valueReasonB5B063AA: "plan_superseded"}, nil))
		}
	}
	return nil
}

func supersedableRunStepStatus(status string) bool {
	return status == model.RunStatusQueued || status == model.RunStatusWaitingInput || status == model.RunStatusRunning || status == model.RunStatusSuspended
}

func buildRunResolutionContinuation(run model.Run, interaction model.Interaction, executionStepID, segmentKey, reviseFeedback string, nextRevision int, approvedTool bool, frozenTool *runFrozenToolCall) (runContinuation, error) {
	continuation := runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: strings.TrimSpace(segmentKey), Type: runContinuationContinuePlan, TargetStatus: model.RunStatusRunning, InteractionID: interaction.InteractionID, PlanID: run.CurrentPlanID, StepID: executionStepID}
	if strings.TrimSpace(reviseFeedback) != "" {
		continuation.Type, continuation.TargetStatus, continuation.SourceStepID, continuation.Feedback, continuation.NextRevision = runContinuationReplan, model.RunStatusPreparing, interaction.StepID, reviseFeedback, nextRevision
	} else if err := applyInteractionResolutionContinuation(&continuation, interaction, approvedTool, frozenTool); err != nil {
		return runContinuation{}, err
	}
	if err := validateRunContinuation(continuation); err != nil {
		return runContinuation{}, err
	}
	return continuation, nil
}

func applyInteractionResolutionContinuation(continuation *runContinuation, interaction model.Interaction, approvedTool bool, frozenTool *runFrozenToolCall) error {
	switch interaction.Type {
	case model.InteractionAskUser:
		continuation.DurableToolResult = &runDurableToolResult{ToolCallID: interaction.ToolCallID, EventType: valueToolCompleted8D0A12FD}
	case model.InteractionApproveTool:
		if approvedTool {
			continuation.Type, continuation.FrozenToolCall = runContinuationExecuteApprovedTool, frozenTool
		} else {
			continuation.DurableToolResult = &runDurableToolResult{ToolCallID: interaction.ToolCallID, EventType: valueToolFailedFB145984}
		}
	case model.InteractionApproveToolSet:
		return applyToolSetResolutionContinuation(continuation, interaction.RequestPayloadJSON)
	}
	return nil
}

func applyToolSetResolutionContinuation(continuation *runContinuation, requestJSON string) error {
	var request struct {
		ContinuationType string `json:"continuationType"`
	}
	if json.Unmarshal([]byte(requestJSON), &request) != nil {
		return ErrRunSnapshotIncompatible
	}
	switch request.ContinuationType {
	case runContinuationStartDirect:
		continuation.Type, continuation.TargetStatus = runContinuationStartDirect, model.RunStatusRunning
	case runContinuationStartPlanning:
		continuation.Type, continuation.TargetStatus, continuation.NextRevision = runContinuationStartPlanning, model.RunStatusPreparing, 1
	default:
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (s *Engine) ensureHostedToolSetApproval(ctx context.Context, run model.Run, effective effectiveTextRunConfig, continuation runContinuation) (bool, error) {
	toolKeys := inheritedHighRiskHostedToolKeys(effective.ToolPolicies)
	if len(toolKeys) == 0 {
		return false, nil
	}
	interactions, err := s.repo.ListRunInteractions(ctx, run.Actor, run.RunID)
	if err != nil {
		return false, err
	}
	if found, waiting := hostedToolSetApprovalState(interactions); found {
		return waiting, nil
	}
	return s.createHostedToolSetApproval(ctx, run, effective, continuation, toolKeys)
}

func inheritedHighRiskHostedToolKeys(policies []effectiveRunToolPolicy) []string {
	toolKeys := make([]string, 0)
	for _, policy := range policies {
		if policy.ExecutionMode == valueProviderHostedF3C237B6 && policy.RiskLevel == valueHighB19D217F {
			toolKeys = append(toolKeys, policy.ToolKey)
		}
	}
	sort.Strings(toolKeys)
	return toolKeys
}

func hostedToolSetApprovalState(interactions []model.Interaction) (bool, bool) {
	for _, interaction := range interactions {
		if interaction.Type != model.InteractionApproveToolSet {
			continue
		}
		switch interaction.Status {
		case model.InteractionPending:
			return true, true
		case model.InteractionResolved:
			var response struct {
				Approved bool `json:"approved"`
			}
			approved := json.Unmarshal([]byte(interaction.ResponseJSON), &response) == nil && response.Approved
			return true, !approved
		}
	}
	return false, false
}

func (s *Engine) createHostedToolSetApproval(ctx context.Context, run model.Run, effective effectiveTextRunConfig, continuation runContinuation, toolKeys []string) (bool, error) {
	request := map[string]interface{}{"toolKeys": toolKeys, valueContinuationTypeDCB4DE9C: continuation.Type}
	interaction := newRunInteraction(run, continuation.StepID, model.InteractionApproveToolSet, request, effective.InteractionTTLHours)
	checkpoint, err := newRunInteractionCheckpoint(run, interaction, "approve_tool_set")
	if err != nil {
		return false, err
	}
	events := []model.Event{
		newRunEvent(run, "checkpoint.created", continuation.StepID, "Hosted tool activation checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, valueKindE5B2EFB3: checkpoint.Kind}, nil),
		newRunEvent(run, "interaction.created", continuation.StepID, "Hosted tool activation required", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueType5EE8C955: interaction.Type, "toolKeys": toolKeys}, nil),
		newRunEvent(run, "run.waiting_input", continuation.StepID, "Waiting for hosted tool activation", map[string]interface{}{valueInteractionIDA8491B1B: interaction.InteractionID, valueReasonB5B063AA: "approve_tool_set"}, nil),
	}
	saved, err := s.repo.CreateRunInteractionBundle(context.WithoutCancel(ctx), run.RunID, continuation.TargetStatus, interaction, checkpoint, events)
	if err != nil {
		return false, err
	}
	s.publishRunEvents(run.RunID, saved)
	return true, nil
}

func (s *Engine) executeRunContinuation(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, checkpoint model.Checkpoint, source string) {
	continuation, err := decodeRunContinuation(checkpoint)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Text Run continuation 不兼容退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, checkpoint.StepID, err)
		s.FinishRunNotifications(run.RunID)
		return
	}
	s.logger.Info("run_continuation_started", String("run_id", run.RunID), String("checkpoint_id", checkpoint.CheckpointID), String("continuation_type", continuation.Type), String("source", source))
	if s.stopForHostedToolApproval(ctx, run, effective, reservation, continuation) || s.dispatchInitialRunContinuation(ctx, run, root, effective, reservation, continuation) {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	s.generationStreams.register(runCtx, run.RunID, run.Actor, cancel)
	defer cancel()
	lifecycle := newRunSegmentLifecycle(s, runCtx, run.RunID, reservation)
	defer lifecycle.abort()
	if err := s.validateDurableRunToolResult(runCtx, run, continuation); err != nil {
		lifecycle.fail(run, effective, continuation.StepID, err)
		return
	}
	if continuation.Type == runContinuationExecuteApprovedTool {
		if err := s.executeApprovedRunTool(runCtx, run, effective, checkpoint, continuation); err != nil {
			lifecycle.fail(run, effective, continuation.StepID, err)
			return
		}
	}
	lifecycle.transfer()
	if effective.Strategy == TextRunStrategyDirect {
		s.executeDirectStrategy(runCtx, run, root, effective, reservation, nil, runUsage{})
		return
	}
	s.executePlan(runCtx, run, root, effective, reservation, nil, runUsage{})
}

func (s *Engine) stopForHostedToolApproval(ctx context.Context, run model.Run, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, continuation runContinuation) bool {
	waiting, err := s.ensureHostedToolSetApproval(ctx, run, effective, continuation)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Hosted Tool 授权创建失败退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, continuation.StepID, err)
		return true
	}
	if waiting {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "等待 Hosted Tool 授权退回预扣")
	}
	return waiting
}

func (s *Engine) dispatchInitialRunContinuation(ctx context.Context, run model.Run, root model.Step, effective effectiveTextRunConfig, reservation *UsageBalanceReservation, continuation runContinuation) bool {
	switch continuation.Type {
	case runContinuationStartDirect:
		s.executeDirectStrategy(ctx, run, root, effective, reservation, nil, runUsage{})
		return true
	case runContinuationStartPlanning, runContinuationReplan:
		s.executePlanning(ctx, run, root, effective, reservation, continuation.NextRevision, continuation.Feedback)
		return true
	case runContinuationRenewInteraction:
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "运行交互续期被错误调度退回预扣")
		s.failTextRun(context.WithoutCancel(ctx), run, continuation.StepID, ErrRunSnapshotIncompatible)
		s.FinishRunNotifications(run.RunID)
		return true
	default:
		return false
	}
}

func (s *Engine) validateDurableRunToolResult(ctx context.Context, run model.Run, continuation runContinuation) error {
	expected := continuation.DurableToolResult
	if expected == nil {
		return nil
	}
	result, err := s.repo.GetRunToolResult(ctx, run.Actor, run.RunID, expected.ToolCallID)
	if err != nil {
		return err
	}
	if result == nil || result.EventType != expected.EventType || result.StepID != continuation.StepID {
		return ErrRunSnapshotIncompatible
	}
	return nil
}

func (s *Engine) executeApprovedRunTool(ctx context.Context, run model.Run, effective effectiveTextRunConfig, checkpoint model.Checkpoint, continuation runContinuation) error {
	request := continuation.FrozenToolCall
	tools, err := s.resolveRunTools(ctx, run.Actor, effective)
	if err != nil {
		return err
	}
	tool, ok := tools[request.ToolName]
	if !ok || !resolvedToolMatchesFrozen(tool, request) {
		return ErrRunSnapshotIncompatible
	}
	existing, resultErr := s.repo.GetRunToolResult(ctx, run.Actor, run.RunID, request.ToolCallID)
	if resultErr == nil && existing != nil {
		if !committedToolResultMatchesFrozen(existing, request) {
			return ErrRunSnapshotIncompatible
		}
		s.logger.Info("run_continuation_tool_result_reused", String("run_id", run.RunID), String("checkpoint_id", checkpoint.CheckpointID), String("tool_call_id", request.ToolCallID))
		return nil
	}
	if !errors.Is(resultErr, ErrNotFound) {
		return resultErr
	}
	if err = s.ensureRunCallBudgetWithReserve(ctx, run, effective, false, 0); err != nil {
		return err
	}
	_, _, err = s.executeFrozenRunTool(ctx, run, continuation.StepID, effective, tool, ToolCall{ToolCallID: request.ToolCallID, ToolName: request.ToolName, ArgumentsJSON: string(request.Arguments)})
	return err
}

func resolvedToolMatchesFrozen(tool ResolvedTool, request *runFrozenToolCall) bool {
	return tool.ToolKey == request.ToolKey && tool.OriginalName == request.OriginalName
}

func committedToolResultMatchesFrozen(event *model.Event, request *runFrozenToolCall) bool {
	terminal := event.EventType == valueToolCompleted8D0A12FD || event.EventType == valueToolFailedFB145984
	return event.ToolName == request.ToolName && terminal
}

func (s *Engine) ResumeTextRun(ctx context.Context, input ResumeTextRunInput) (*model.Checkpoint, bool, error) {
	prepared, err := s.prepareTextRunResume(ctx, input)
	if err != nil {
		return nil, false, err
	}
	if prepared.continuation.Type == runContinuationRenewInteraction {
		return s.renewExpiredRunInteraction(ctx, input, prepared.run, prepared.effective, prepared.checkpoint, prepared.continuation, prepared.fingerprint)
	}
	root, resumeStepIDs, err := s.prepareExplicitResumeSteps(ctx, prepared.run, prepared.reused)
	if err != nil {
		return nil, false, err
	}
	var reservation *UsageBalanceReservation
	if !prepared.reused {
		reservation, _, err = s.ReserveRunUsageBalance(ctx, RunBillingInput{Actor: prepared.run.Actor, Thread: prepared.run.Thread, PlatformModelName: prepared.effective.PlatformModelName, ClientRunID: prepared.continuation.SegmentKey})
		if err != nil {
			return nil, false, err
		}
	}
	return s.applyExplicitTextRunResume(ctx, input, prepared, root, resumeStepIDs, reservation)
}

type preparedTextRunResume struct {
	run          model.Run
	effective    effectiveTextRunConfig
	checkpoint   model.Checkpoint
	continuation runContinuation
	reused       bool
	fingerprint  string
}

func (s *Engine) prepareTextRunResume(ctx context.Context, input ResumeTextRunInput) (preparedTextRunResume, error) {
	if !validResumeTextRunInput(input) {
		return preparedTextRunResume{}, ErrInvalidInput
	}
	run, err := s.repo.GetRun(ctx, input.Actor, input.RunID)
	if err != nil {
		return preparedTextRunResume{}, err
	}
	effective, err := s.loadResumeTextRunRuntime(ctx, *run)
	if err != nil {
		return preparedTextRunResume{}, ErrRunSnapshotIncompatible
	}
	checkpoint, reused, err := s.selectRunResumeCheckpoint(ctx, input)
	if err != nil {
		return preparedTextRunResume{}, err
	}
	continuation, err := decodeRunContinuation(checkpoint)
	if err != nil {
		return preparedTextRunResume{}, ErrRunSnapshotIncompatible
	}
	requestedID := strings.TrimSpace(input.CheckpointID)
	if requestedID != "" && checkpoint.CheckpointID != requestedID {
		if reused {
			return preparedTextRunResume{}, ErrRunResumeIDConflict
		}
		return preparedTextRunResume{}, ErrRunResumeConflict
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(run.RunID+"\x00"+checkpoint.CheckpointID)))
	return preparedTextRunResume{run: *run, effective: effective, checkpoint: checkpoint, continuation: continuation, reused: reused, fingerprint: fingerprint}, nil
}

func validResumeTextRunInput(input ResumeTextRunInput) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.RunID) != "" && strings.TrimSpace(input.ClientResumeID) != "" && len(input.ClientResumeID) <= 64
}

func (s *Engine) loadResumeTextRunRuntime(ctx context.Context, run model.Run) (effectiveTextRunConfig, error) {
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) != nil || effective.SemanticVersion != RuntimeSnapshotVersion {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	if _, err := s.loadTextRunContextMessages(ctx, run); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	if _, err := s.resolveRunTools(ctx, run.Actor, effective); err != nil {
		return effectiveTextRunConfig{}, ErrRunSnapshotIncompatible
	}
	return effective, nil
}

func (s *Engine) selectRunResumeCheckpoint(ctx context.Context, input ResumeTextRunInput) (model.Checkpoint, bool, error) {
	checkpoints, err := s.repo.ListRunCheckpoints(ctx, input.Actor, input.RunID)
	if err != nil {
		return model.Checkpoint{}, false, err
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.ResumeRequestID == input.ClientResumeID {
			return checkpoint, true, nil
		}
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Status == model.CheckpointReady {
			return checkpoint, false, nil
		}
	}
	return model.Checkpoint{}, false, ErrRunResumeConflict
}

func (s *Engine) prepareExplicitResumeSteps(ctx context.Context, run model.Run, reused bool) (model.Step, []string, error) {
	if reused {
		return model.Step{}, nil, nil
	}
	root, err := s.runRootStep(ctx, run.RunID)
	if err != nil {
		return model.Step{}, nil, ErrRunResumeConflict
	}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return model.Step{}, nil, err
	}
	resumeStepIDs := suspendedResumeStepIDs(steps, run.CurrentStepID, root.StepID)
	if len(resumeStepIDs) == 0 {
		return model.Step{}, nil, ErrRunResumeConflict
	}
	return root, resumeStepIDs, nil
}

func suspendedResumeStepIDs(steps []model.Step, currentStepID, rootStepID string) []string {
	result := make([]string, 0, 2)
	seen := map[string]struct{}{}
	for _, step := range steps {
		if step.Status != model.RunStatusSuspended || step.StepID != currentStepID && step.StepID != rootStepID {
			continue
		}
		if _, exists := seen[step.StepID]; !exists {
			seen[step.StepID] = struct{}{}
			result = append(result, step.StepID)
		}
	}
	return result
}

func (s *Engine) applyExplicitTextRunResume(ctx context.Context, input ResumeTextRunInput, prepared preparedTextRunResume, root model.Step, resumeStepIDs []string, reservation *UsageBalanceReservation) (*model.Checkpoint, bool, error) {
	run, continuation, selectedCheckpoint := prepared.run, prepared.continuation, prepared.checkpoint
	nextStatus := continuation.TargetStatus
	successor := newRunContinuationCheckpoint(run, selectedCheckpoint.StepID, "resume_execution", continuation)
	successor.CheckpointID = deterministicRunCheckpointID(run.RunID, selectedCheckpoint.CheckpointID, input.ClientResumeID, "resume_execution")
	successor.ParentCheckpointID = selectedCheckpoint.CheckpointID
	events := runExplicitResumeEvents(run, selectedCheckpoint, *successor, nextStatus, resumeStepIDs, continuation.Type)
	checkpoint, continuationCheckpoint, saved, applied, err := s.repo.ResumeRun(ctx, input.Actor, input.RunID, selectedCheckpoint.CheckpointID, input.ClientResumeID, prepared.fingerprint, nextStatus, successor, events)
	if err != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行显式恢复失败退回预扣")
		return nil, false, err
	}
	if !applied {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行显式恢复幂等复用退回预扣")
		return checkpoint, true, nil
	}
	if continuationCheckpoint == nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行显式恢复缺少后继检查点退回预扣")
		return nil, false, ErrRunSnapshotIncompatible
	}
	s.publishRunEvents(run.RunID, saved)
	resumedContinuation, continuationErr := decodeRunContinuation(*continuationCheckpoint)
	if continuationErr != nil {
		_ = s.ReleaseRunUsageReservation(ctx, reservation, "运行显式恢复后继检查点不兼容退回预扣")
		return nil, false, ErrRunSnapshotIncompatible
	}
	segmentCtx := context.WithValue(context.Background(), runSegmentKeyContextKey{}, resumedContinuation.SegmentKey)
	run.Status, run.PendingInteractionID = nextStatus, ""
	s.launchRunContinuation(func() {
		s.executeRunContinuation(segmentCtx, run, root, prepared.effective, reservation, *continuationCheckpoint, "explicit_resume")
	})
	return checkpoint, false, nil
}

func (s *Engine) renewExpiredRunInteraction(ctx context.Context, input ResumeTextRunInput, run model.Run, effective effectiveTextRunConfig, selected model.Checkpoint, continuation runContinuation, fingerprint string) (*model.Checkpoint, bool, error) {
	frozen := continuation.FrozenInteraction
	if frozen == nil {
		return nil, false, ErrRunSnapshotIncompatible
	}
	expired, err := s.repo.GetRunInteraction(ctx, input.Actor, input.RunID, frozen.InteractionID)
	if err != nil || !expiredInteractionMatchesFrozen(expired, frozen) {
		return nil, false, ErrRunSnapshotIncompatible
	}
	renewed, successor, events, err := buildRenewedRunInteraction(input, run, effective, selected, frozen)
	if err != nil {
		return nil, false, err
	}
	checkpoint, _, _, saved, applied, err := s.repo.RenewExpiredRunInteraction(ctx, input.Actor, input.RunID, frozen.InteractionID, selected.CheckpointID, input.ClientResumeID, fingerprint, renewed, successor, events)
	if err != nil {
		return nil, false, err
	}
	if applied {
		s.publishRunEvents(run.RunID, saved)
	}
	return checkpoint, !applied, nil
}

func expiredInteractionMatchesFrozen(expired *model.Interaction, frozen *runFrozenInteraction) bool {
	return expired.Status == model.InteractionExpired && expired.Type == frozen.Type && expired.StepID == frozen.StepID && expired.ToolCallID == frozen.ToolCallID && canonicalRunJSON(json.RawMessage(expired.RequestPayloadJSON)) == canonicalRunJSON(frozen.Request) && canonicalRunJSON(json.RawMessage(expired.ResponseSchemaJSON)) == canonicalRunJSON(frozen.ResponseSchema)
}

func buildRenewedRunInteraction(input ResumeTextRunInput, run model.Run, effective effectiveTextRunConfig, selected model.Checkpoint, frozen *runFrozenInteraction) (*model.Interaction, *model.Checkpoint, []model.Event, error) {
	now := time.Now()
	expiresAt := now.Add(time.Duration(effective.InteractionTTLHours) * time.Hour)
	renewedID := deterministicRunInteractionID(run.RunID, frozen.InteractionID, input.ClientResumeID, "renewed")
	renewed := &model.Interaction{InteractionID: renewedID, RunID: run.RunID, StepID: frozen.StepID, ToolCallID: frozen.ToolCallID, Type: frozen.Type, Status: model.InteractionPending, RequestPayloadJSON: string(frozen.Request), ResponseSchemaJSON: string(frozen.ResponseSchema), RequestedAt: now, ExpiresAt: &expiresAt}
	successor, err := newRunInteractionCheckpoint(run, renewed, "interaction_renewed")
	if err != nil {
		return nil, nil, nil, err
	}
	successor.CheckpointID = deterministicRunCheckpointID(run.RunID, selected.CheckpointID, input.ClientResumeID, "interaction_renewed")
	successor.ParentCheckpointID = selected.CheckpointID
	var request interface{}
	if err = json.Unmarshal(frozen.Request, &request); err != nil {
		return nil, nil, nil, ErrRunSnapshotIncompatible
	}
	events := []model.Event{
		newRunEvent(run, "interaction.created", frozen.StepID, "Expired interaction reopened", map[string]interface{}{valueInteractionIDA8491B1B: renewed.InteractionID, "renewedFromInteractionID": frozen.InteractionID, valueType5EE8C955: frozen.Type, valueRequest91B6AFF3: request, "expiresAt": expiresAt}, nil),
		newRunEvent(run, "checkpoint.created", frozen.StepID, "Renewed interaction checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: successor.CheckpointID, valueKindE5B2EFB3: successor.Kind, valueContinuationTypeDCB4DE9C: runContinuationRenewInteraction}, nil),
		newRunEvent(run, "step.waiting_input", frozen.StepID, "Waiting for renewed interaction", map[string]interface{}{valueInteractionIDA8491B1B: renewed.InteractionID}, nil),
	}
	resumed := newRunEvent(run, "run.resumed", frozen.StepID, "Expired interaction reopened", map[string]interface{}{valueCheckpointID9CD08C70: selected.CheckpointID, "executionCheckpointID": successor.CheckpointID, valueInteractionIDA8491B1B: renewed.InteractionID, valueStatus327C4193: model.RunStatusWaitingInput}, nil)
	resumed.Status = model.RunStatusWaitingInput
	events = append(events, resumed)
	return renewed, successor, events, nil
}

func deterministicRunInteractionID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "interaction_" + fmt.Sprintf("%x", digest[:16])
}

func runExplicitResumeEvents(run model.Run, checkpoint, successor model.Checkpoint, nextStatus string, stepIDs []string, continuationType string) []model.Event {
	events := []model.Event{newRunEvent(run, "checkpoint.created", successor.StepID, "Resume execution checkpoint", map[string]interface{}{valueCheckpointID9CD08C70: successor.CheckpointID, valueKindE5B2EFB3: successor.Kind, valueContinuationTypeDCB4DE9C: continuationType}, nil)}
	for _, stepID := range stepIDs {
		events = append(events, newRunEvent(run, valueStepResumedF8C2AD47, stepID, "Step resumed", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID}, nil))
	}
	resumed := newRunEvent(run, "run.resumed", successor.StepID, "Text run resumed", map[string]interface{}{valueCheckpointID9CD08C70: checkpoint.CheckpointID, "executionCheckpointID": successor.CheckpointID}, nil)
	resumed.Status = nextStatus
	events = append(events, resumed)
	return events
}

func (s *Engine) runRootStep(ctx context.Context, runID string) (model.Step, error) {
	steps, err := s.repo.ListRunSteps(ctx, runID)
	if err != nil {
		return model.Step{}, err
	}
	for _, step := range steps {
		if step.ParentStepID == "" && step.Kind == valueOrchestration7969B2CD {
			return step, nil
		}
	}
	return model.Step{}, ErrNotFound
}

func normalizeRunInteractionResponse(value interface{}) (string, map[string]interface{}, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", nil, err
	}
	var normalized map[string]interface{}
	if err = json.Unmarshal(encoded, &normalized); err != nil || normalized == nil {
		return "", nil, ErrInvalidInput
	}
	encoded, err = json.Marshal(normalized)
	return string(encoded), normalized, err
}

func (s *Engine) ensureRunCallBudgetWithReserve(ctx context.Context, run model.Run, effective effectiveTextRunConfig, llm bool, reservedCalls int) error {
	eventType, limit, label := valueToolStartedB113F313, effective.MaxToolCalls, valueToolCCF14517
	if llm {
		eventType, limit, label = valueUsageUpdatedABC8B0B2, effective.MaxLLMCalls, "LLM"
	}
	counts, err := s.repo.CountRunEventsByType(ctx, run.Actor, run.RunID, []string{eventType})
	if err != nil {
		return err
	}
	if reservedCalls < 0 {
		reservedCalls = 0
	}
	if limit <= 0 || counts[eventType]+reservedCalls >= limit {
		return withErrorMessage(errCategoryAFA87E325A, fmt.Sprintf("text run %s call limit reached", label))
	}
	return nil
}

func (s *Engine) runLLMCallsUsed(ctx context.Context, run model.Run) (int, error) {
	counts, err := s.repo.CountRunEventsByType(ctx, run.Actor, run.RunID, []string{valueUsageUpdatedABC8B0B2})
	if err != nil {
		return 0, err
	}
	return counts[valueUsageUpdatedABC8B0B2], nil
}

func (s *Engine) recordRunLLMUsage(ctx context.Context, run model.Run, phase string, route *LLMRoute, output *GenerateOutput) error {
	return s.recordRunLLMUsageForStep(ctx, run, run.CurrentStepID, phase, route, output)
}

func (s *Engine) recordRunLLMUsageForStep(ctx context.Context, run model.Run, stepID, phase string, route *LLMRoute, output *GenerateOutput) error {
	usage := Usage{}
	var serverSideToolUsage map[string]int64
	var serverToolCalls []ToolCall
	if output != nil {
		usage = output.Usage
		serverSideToolUsage = output.ServerSideToolUsage
		serverToolCalls = output.ServerToolCalls
	}
	payload := map[string]interface{}{valueSegmentKeyB3442EFB: runSegmentKey(ctx, run), valuePhaseA62799FA: phase, "inputTokens": usage.InputTokens, "outputTokens": usage.OutputTokens, "cacheReadTokens": usage.CacheReadTokens, "cacheWriteTokens": usage.CacheWriteTokens, "cacheWrite5mTokens": usage.CacheWrite5mTokens, "cacheWrite1hTokens": usage.CacheWrite1hTokens, "reasoningTokens": usage.ReasoningTokens, "usageSpeed": usage.Speed, "usageServiceTier": usage.ServiceTier, "billingRateClass": usage.BillingRateClass, "rawUsageJSON": usage.RawUsageJSON, "serverSideToolUsage": serverSideToolUsage, "serverToolCalls": serverToolCalls}
	if route != nil {
		payload["upstreamID"], payload["upstreamName"], payload["bindingCode"], payload["upstreamModel"], payload["protocol"] = route.UpstreamID, route.UpstreamName, route.BindingCode, route.UpstreamModel, route.Protocol
	}
	event := newRunEvent(run, valueUsageUpdatedABC8B0B2, stepID, phase, payload, nil)
	return s.appendRunEvents(ctx, run.RunID, []model.Event{event})
}

var (
	errCategoryCD625F2DD4 = errors.New("plan contains a dependency cycle")
	errCategoryDE42830626 = errors.New("run step returned no result")
	errCategory1DF148F8BA = errors.New("text run assistant message is missing")
	errCategory29A343B698 = errors.New("text run segment billing message pair is missing")
	errCategory26D6F64F1B = errors.New("text runtime message pair is missing")
	errCategory9303C78FF1 = errors.New("error category 9303C78FF1")
	errCategoryB59D448B11 = errors.New("output limit reached")
	errCategoryEADA7D3E1E = errors.New("error category EADA7D3E1E")
	errCategory377637EA92 = errors.New("error category 377637EA92")
	errCategory440497AF28 = errors.New("planned execution must contain at least one step")
	errCategory51FBCA2215 = errors.New("error category 51FBCA2215")
	errCategory512384F21F = errors.New("error category 512384F21F")
	errCategory19575CB09B = errors.New("error category 19575CB09B")
	errCategoryFCA4993A9B = errors.New("error category FCA4993A9B")
	errCategory5687A8F7EC = errors.New("error category 5687A8F7EC")
	errCategoryF588B464C3 = errors.New("error category F588B464C3")
	errCategoryD8EDA1A858 = errors.New("error category D8EDA1A858")
	errCategoryAFA87E325A = errors.New("error category AFA87E325A")
	errCategoryDD926A6DAE = errors.New("error category DD926A6DAE")
	errCategory8A92970CAF = errors.New("text run LLM call limit reached")
	errCategory00919D2AA2 = errors.New("error category 00919D2AA2")
)
