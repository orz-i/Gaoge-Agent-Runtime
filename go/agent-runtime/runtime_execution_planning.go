package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

var errPlanSummaryConflict = errors.New("summary conflicts with planSummary")

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

func buildPlanningBundle(run model.Run, root model.Step, effective effectiveTextRunConfig, payload planPayload, revision int) (planningBundle, error) {
	planID := "plan_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	planJSON, err := json.Marshal(payload)
	if err != nil {
		return planningBundle{}, err
	}
	status := model.PlanProposed
	plan := model.Plan{PlanID: planID, RunID: run.RunID, Revision: revision, Status: status, Goal: run.Goal, Summary: payload.Summary, PayloadJSON: string(planJSON)}
	steps, err := buildPlanningSteps(run, root, planID, payload.Steps)
	if err != nil {
		return planningBundle{}, err
	}
	bundle := planningBundle{Plan: plan, Steps: steps, Events: planningEvents(run, root, payload, planID, revision, steps)}
	return addRequiredPlanningApproval(bundle, run, root, effective, payload, revision)
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

func buildPlannerRequest(runID, goal string, effective effectiveTextRunConfig, revision int, feedback string, repair bool, planMaxSteps int, structuredOutput modelcap.StructuredOutputMode, contextMessages ...[]Message) GenerateInput {
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
	applyPlannerStructuredOutput(options, structuredOutput, schema)
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
	if err := validatePlanSize(payload, maxSteps); err != nil {
		return payload, err
	}
	keys, err := normalizeAndValidatePlanSteps(payload.Steps, shape.Steps)
	if err != nil {
		return payload, err
	}
	if err = validatePlanDependencyGraph(payload.Steps, keys); err != nil {
		return payload, err
	}
	payload.Summary = strings.TrimSpace(payload.Summary)
	if payload.Summary == "" {
		payload.Summary = derivePlanSummary(payload.Steps)
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

func validatePlanSize(payload planPayload, maxSteps int) error {
	if len(payload.Steps) == 0 {
		return errCategory440497AF28
	}
	if len(payload.Steps) > maxSteps {
		return fmt.Errorf("%w: plan must contain at most %d steps; got %d", errPlanBudgetExceeded, maxSteps, len(payload.Steps))
	}
	return nil
}

func derivePlanSummary(steps []planStepSpec) string {
	const maxSummaryRunes = 240
	titles := make([]string, 0, min(len(steps), 3))
	for _, step := range steps {
		if title := strings.TrimSpace(step.Title); title != "" {
			titles = append(titles, title)
		}
		if len(titles) == 3 {
			break
		}
	}
	summary := "执行计划：" + strings.Join(titles, "；")
	runes := []rune(summary)
	if len(runes) > maxSummaryRunes {
		return string(runes[:maxSummaryRunes])
	}
	return summary
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
			return false, errPlanSummaryConflict
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
