package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

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
	var route *LLMRoute
	if err == nil && !waiting && len(effective.StructuredOutputSchema) > 0 {
		if validationErr := validateStructuredRunText(finalText, effective.StructuredOutputSchema); validationErr != nil {
			var correctionUsage Usage
			correctionUsage, route, finalText, err = s.repairStructuredRunAnswer(ctx, run, root.StepID, effective, contextMessages, finalText, validationErr)
			usage = addRunUsage(usage, runUsageFromUsage(correctionUsage))
		}
	}
	if err == nil && !waiting && strings.TrimSpace(finalText) != "" {
		err = s.appendRunEvent(context.WithoutCancel(ctx), &run, "message.delta", root.StepID, "", map[string]interface{}{valueDelta1F5E22EC: finalText}, nil)
	}
	return finalText, route, usage, waiting, err
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
