package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueAction11628904          = "action"
	valueAssistant9564D316       = "assistant"
	valueCheckpointFCE17F9B      = "checkpoint"
	valueContext1E6A2143         = "context"
	valueContextCompiled46DA71A3 = "context.compiled"
	valueExecution22CE8488       = "execution"
	valueGoal33002E73            = "goal"
	valueInteraction0DA88982     = "interaction"
	valueMessageDelta8D963128    = "message.delta"
	valueOutput2060C6DF          = "output"
	valuePhaseB99DC3AB           = "phase"
	valuePlan78EDC9FE            = "plan"
	valuePlanning7A3C70BD        = "planning"
	valueQueued5052B952          = "queued"
	valueRunCancelledD74AD332    = "run.cancelled"
	valueRunCompleted20A7FCFE    = "run.completed"
	valueRunFailedD21BA399       = "run.failed"
	valueRunPreparing142D9E38    = "run.preparing"
	valueRunResumedB398BE30      = "run.resumed"
	valueRunSuspendedA2ED2B05    = "run.suspended"
	valueRunWaitingInput4621EBDE = "run.waiting_input"
	valueRunWaitingHandoff       = "run.waiting_handoff"
	valueStep1396E1CE            = "step"
	valueStepResumed395D0C55     = "step.resumed"
	valueSynthesis58DF5D37       = "synthesis"
	valueToolE422AB02            = "tool"
	valueTrace48C00821           = "trace"
	valueUsageUpdatedBD37B6AA    = "usage.updated"
	valueUser13846E16            = "user"
)

const (
	workbenchProjectionVersion = 2
	workbenchContractVersion   = 1
)

type PhaseView struct {
	PhaseID     string     `json:"phaseID"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Status      string     `json:"status"`
	StartSeq    int64      `json:"startSeq"`
	EndSeq      int64      `json:"endSeq,omitempty"`
	StepIDs     []string   `json:"stepIDs"`
	ToolCallIDs []string   `json:"toolCallIDs"`
	OutputIDs   []string   `json:"outputIDs"`
	StartedAt   time.Time  `json:"startedAt,omitempty"`
	EndedAt     *time.Time `json:"endedAt,omitempty"`
}

type ToolGroupView struct {
	GroupID      string            `json:"groupID"`
	PhaseID      string            `json:"phaseID"`
	StepID       string            `json:"stepID,omitempty"`
	Title        string            `json:"title,omitempty"`
	Status       string            `json:"status"`
	StartSeq     int64             `json:"startSeq"`
	EndSeq       int64             `json:"endSeq"`
	ToolCallIDs  []string          `json:"toolCallIDs"`
	ToolNames    map[string]string `json:"toolNames,omitempty"`
	ToolEventIDs map[string]string `json:"toolEventIDs,omitempty"`
	ToolStatuses map[string]string `json:"toolStatuses,omitempty"`
}

type WorkbenchOverview struct {
	Goal            string     `json:"goal"`
	Status          string     `json:"status"`
	CurrentPhaseID  string     `json:"currentPhaseID,omitempty"`
	StatusReason    string     `json:"statusReason,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ErrorMessage    string     `json:"errorMessage,omitempty"`
	Strategy        string     `json:"strategy,omitempty"`
	PlannerRepairs  int        `json:"plannerRepairs"`
	StartedAt       time.Time  `json:"startedAt,omitempty"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	LLMCalls        int        `json:"llmCalls"`
	ToolCalls       int        `json:"toolCalls"`
	InputTokens     int64      `json:"inputTokens"`
	OutputTokens    int64      `json:"outputTokens"`
	ReasoningTokens int64      `json:"reasoningTokens"`
	BilledCurrency  string     `json:"billedCurrency,omitempty"`
	BilledNanousd   int64      `json:"billedNanousd"`
}

type WorkbenchGraphNode struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Subtitle string `json:"subtitle,omitempty"`
	Status   string `json:"status"`
	EntityID string `json:"entityID,omitempty"`
	PhaseID  string `json:"phaseID,omitempty"`
}

type WorkbenchGraphEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

type WorkbenchSelectionTarget struct {
	Tab     string `json:"tab"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	PhaseID string `json:"phaseID,omitempty"`
	Seq     int64  `json:"seq,omitempty"`
}

type Workbench struct {
	ProjectionVersion   int
	ProjectionSeq       int64
	ProjectionPersisted bool
	Run                 model.Run
	Workflow            *model.WorkflowExecution
	Result              *model.RunResult
	Overview            WorkbenchOverview
	Phases              []PhaseView
	ToolGroups          []ToolGroupView
	Plan                *PlanView
	Steps               []model.Step
	PendingInteraction  *model.Interaction
	Interactions        []model.Interaction
	Checkpoints         []model.Checkpoint
	Outputs             []model.OutputRef
	Context             *TextRunContextSummary
	Config              *TextRunConfigSummary
	GraphNodes          []WorkbenchGraphNode
	GraphEdges          []WorkbenchGraphEdge
	SelectionIndex      map[string]WorkbenchSelectionTarget
}

func (s *Engine) GetWorkbench(ctx context.Context, actor model.ActorRef, runID string) (*Workbench, error) {
	if !validActorRef(actor) || strings.TrimSpace(runID) == "" {
		return nil, ErrInvalidInput
	}
	snapshot, err := s.repo.LoadWorkbenchSnapshot(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	detail := textRunDetailFromWorkbenchSnapshot(snapshot)
	if detail.Config != nil {
		// Rebuild summary with route protocol so ProviderToolPayloadBytes matches budget gates.
		var effective effectiveTextRunConfig
		if json.Unmarshal([]byte(snapshot.Run.RunConfigSnapshotJSON), &effective) == nil {
			protocol := s.resolveProviderProtocolForSummary(ctx, snapshot.Run, effective.PlatformModelName)
			detail.Config = summarizeTextRunConfig(effective, protocol)
		}
	}
	phases, persisted := s.loadOrBuildPhasesFromSnapshot(ctx, actor, snapshot)
	var plan *PlanView
	for index := range snapshot.Plans {
		if snapshot.Plans[index].PlanID == snapshot.Run.CurrentPlanID {
			current := snapshot.Plans[index]
			plan = &PlanView{Current: &current, Revisions: snapshot.Plans, Steps: detail.Steps}
			break
		}
	}
	var pending *model.Interaction
	for index := range snapshot.Interactions {
		if snapshot.Interactions[index].Status == model.InteractionPending {
			item := snapshot.Interactions[index]
			pending = &item
			break
		}
	}
	result := &Workbench{ProjectionVersion: workbenchProjectionVersion, ProjectionSeq: detail.Run.LastPresentationEventSeq, ProjectionPersisted: persisted, Run: detail.Run, Workflow: snapshot.Workflow, Result: snapshot.Result, Phases: phases, Plan: plan, Steps: detail.Steps, PendingInteraction: pending, Interactions: snapshot.Interactions, Checkpoints: snapshot.Checkpoints, Outputs: snapshot.Outputs, Context: detail.Context, Config: detail.Config}
	result.Overview = buildWorkbenchOverview(detail.Run, phases, snapshot.Events, detail.Config)
	result.ToolGroups = buildToolGroups(phases, snapshot.Events)
	result.GraphNodes, result.GraphEdges, result.SelectionIndex = buildWorkbenchGraph(result)
	return result, nil
}

func textRunDetailFromWorkbenchSnapshot(snapshot *model.WorkbenchSnapshot) TextRunDetail {
	detail := TextRunDetail{Run: snapshot.Run, Steps: snapshot.Steps}
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(snapshot.Run.RunConfigSnapshotJSON), &effective) == nil {
		detail.Config = summarizeTextRunConfig(effective, strings.TrimSpace(snapshot.Run.ProviderProtocol))
	}
	if snapshot.Context != nil {
		item := snapshot.Context
		detail.Context = &TextRunContextSummary{SnapshotID: item.SnapshotID, SemanticVersion: item.SchemaVersion, ContentHash: item.ContentHash, FileCount: item.FileCount, RAGCount: item.RAGCount, SkillCount: item.SkillCount, MemoryCount: item.MemoryCount, OutputCount: item.OutputCount, EvidenceCount: item.EvidenceCount, RetrievalFallbackCount: item.RetrievalFallbackCount, SkippedCount: item.SkippedCount, CompiledAt: item.CreatedAt}
	}
	return detail
}

func (s *Engine) loadOrBuildPhasesFromSnapshot(ctx context.Context, actor model.ActorRef, snapshot *model.WorkbenchSnapshot) ([]PhaseView, bool) {
	run := snapshot.Run
	base, afterSeq, current, reload := workbenchProjectionBase(snapshot)
	if current {
		return phaseViewsFromDomain(snapshot.Phases), true
	}
	if reload {
		allEvents, err := s.repo.ListPresentationEvents(ctx, actor, run.RunID, 0)
		if err != nil {
			return projectPhasesFrom(run, snapshot.Steps, nil, nil), false
		}
		snapshot.Events = allEvents
	}
	events := workbenchEventsAfter(snapshot.Events, afterSeq)
	phases := projectPhasesFrom(run, snapshot.Steps, base, events)
	domainPhases := phaseViewsToDomain(run.RunID, phases)
	persistErr := s.repo.ReplaceWorkbenchProjection(ctx, actor, &model.WorkbenchProjection{RunID: run.RunID, ProjectionVersion: workbenchProjectionVersion, SourcePresentationEventSeq: run.LastPresentationEventSeq}, domainPhases)
	if persistErr != nil && s.logger != nil {
		s.logger.Warn("workbench_projection_persist_failed", String("run_id", run.RunID), Error(persistErr))
	}
	return phases, persistErr == nil
}

func workbenchProjectionBase(snapshot *model.WorkbenchSnapshot) ([]PhaseView, int64, bool, bool) {
	projection := snapshot.Projection
	if projection == nil {
		return nil, 0, false, false
	}
	if projection.ProjectionVersion != workbenchProjectionVersion {
		return nil, 0, false, true
	}
	if projection.SourcePresentationEventSeq == snapshot.Run.LastPresentationEventSeq {
		return nil, 0, true, false
	}
	if projection.SourcePresentationEventSeq < snapshot.Run.LastPresentationEventSeq {
		return phaseViewsFromDomain(snapshot.Phases), projection.SourcePresentationEventSeq, false, false
	}
	return nil, 0, false, false
}

func workbenchEventsAfter(events []model.Event, afterSeq int64) []model.Event {
	result := make([]model.Event, 0, len(events))
	for _, event := range events {
		if int64(event.Seq) > afterSeq {
			result = append(result, event)
		}
	}
	return result
}

type phaseBuilder struct {
	PhaseView
	stepSet, toolSet, outputSet map[string]struct{}
}

func projectPhases(run model.Run, steps []model.Step, events []model.Event) []PhaseView {
	return projectPhasesFrom(run, steps, nil, events)
}

func workbenchStepsByID(steps []model.Step) map[string]model.Step {
	result := make(map[string]model.Step, len(steps))
	for _, step := range steps {
		result[step.StepID] = step
	}
	return result
}

func phaseBuildersFromBase(base []PhaseView) map[string]*phaseBuilder {
	builders := make(map[string]*phaseBuilder, len(base))
	for _, phase := range base {
		builder := &phaseBuilder{PhaseView: phase, stepSet: stringSet(phase.StepIDs), toolSet: stringSet(phase.ToolCallIDs), outputSet: stringSet(phase.OutputIDs)}
		builders[phase.PhaseID] = builder
	}
	return builders
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func ensurePhaseBuilder(builders map[string]*phaseBuilder, runID, key, kind, title string, event model.Event) *phaseBuilder {
	id := phaseID(runID, key)
	item := builders[id]
	if item == nil {
		item = &phaseBuilder{PhaseView: PhaseView{PhaseID: id, Kind: kind, Title: title, Status: valueQueued5052B952, StartSeq: int64(event.Seq), StartedAt: event.StartedAt}, stepSet: map[string]struct{}{}, toolSet: map[string]struct{}{}, outputSet: map[string]struct{}{}}
		builders[id] = item
	}
	if item.StartSeq == 0 || int64(event.Seq) < item.StartSeq {
		item.StartSeq, item.StartedAt = int64(event.Seq), event.StartedAt
	}
	if int64(event.Seq) > item.EndSeq {
		item.EndSeq = int64(event.Seq)
	}
	return item
}

type phaseDescriptor struct{ Key, Kind, Title string }

func classifyPhaseEvent(event model.Event, payload map[string]interface{}, steps map[string]model.Step, stepCount int) (phaseDescriptor, bool) {
	if descriptor, matched := classifyContextOrPlanningEvent(event, payload); matched {
		return descriptor, true
	}
	interactionID := workbenchPayloadString(payload, "interactionID")
	if descriptor, matched := classifyInteractionEvent(event, interactionID); matched {
		return descriptor, true
	}
	if event.EventType == valueRunResumedB398BE30 {
		return classifyResumedPhaseEvent(event, payload, interactionID, steps, stepCount)
	}
	if synthesisPhaseEvent(event, payload) {
		return phaseDescriptor{Key: valueSynthesis58DF5D37, Kind: valueSynthesis58DF5D37}, true
	}
	if event.StepID != "" {
		return classifyStepPhaseEvent(event, steps, stepCount), true
	}
	return phaseDescriptor{Key: valueSynthesis58DF5D37, Kind: valueSynthesis58DF5D37}, true
}

func classifyContextOrPlanningEvent(event model.Event, payload map[string]interface{}) (phaseDescriptor, bool) {
	if event.EventType == "run.started" || event.EventType == valueContextCompiled46DA71A3 {
		return phaseDescriptor{Key: valueContext1E6A2143, Kind: valueContext1E6A2143}, true
	}
	plannedStrategy := event.EventType == "run.strategy_selected" && workbenchPayloadString(payload, "strategy") == TextRunStrategyPlanned
	if plannedStrategy || event.EventType == valueRunPreparing142D9E38 || strings.HasPrefix(event.EventType, "plan.") {
		return phaseDescriptor{Key: valuePlanning7A3C70BD, Kind: valuePlanning7A3C70BD}, true
	}
	return phaseDescriptor{}, false
}

func classifyInteractionEvent(event model.Event, interactionID string) (phaseDescriptor, bool) {
	if strings.HasPrefix(event.EventType, "interaction.") {
		return interactionPhaseDescriptor(firstNonEmptyString(interactionID, event.StepID)), true
	}
	waiting := event.EventType == valueRunWaitingInput4621EBDE || event.EventType == "step.waiting_input" || event.EventType == valueRunWaitingHandoff || event.EventType == "step.waiting_handoff" || event.EventType == "step.approved"
	if waiting && interactionID != "" {
		return interactionPhaseDescriptor(interactionID), true
	}
	return phaseDescriptor{}, false
}

func interactionPhaseDescriptor(interactionID string) phaseDescriptor {
	return phaseDescriptor{Key: "interaction:" + interactionID, Kind: valueInteraction0DA88982}
}

func classifyResumedPhaseEvent(event model.Event, payload map[string]interface{}, interactionID string, steps map[string]model.Step, stepCount int) (phaseDescriptor, bool) {
	status := firstNonEmptyString(event.Status, workbenchPayloadString(payload, "status"))
	if status == model.RunStatusWaitingInput && interactionID != "" {
		return interactionPhaseDescriptor(interactionID), true
	}
	if status == model.RunStatusPreparing {
		return phaseDescriptor{Key: valuePlanning7A3C70BD, Kind: valuePlanning7A3C70BD}, true
	}
	step, found := steps[event.StepID]
	if found && step.ParentStepID != "" || stepCount == 1 && event.StepID != "" {
		return executionPhaseDescriptor(event.StepID, step.Title), true
	}
	return phaseDescriptor{}, false
}

func synthesisPhaseEvent(event model.Event, payload map[string]interface{}) bool {
	messageTerminal := event.EventType == "message.completed" || event.EventType == "message.failed" || event.EventType == "message.cancelled"
	return messageTerminal || isRunTerminalEvent(event.EventType) || event.EventType == valueUsageUpdatedBD37B6AA && fmt.Sprint(payload[valuePhaseB99DC3AB]) == valueSynthesis58DF5D37
}

func classifyStepPhaseEvent(event model.Event, steps map[string]model.Step, stepCount int) phaseDescriptor {
	step := steps[event.StepID]
	if step.ParentStepID == "" && stepCount > 1 && !strings.HasPrefix(event.EventType, "tool.") {
		return phaseDescriptor{Key: valuePlanning7A3C70BD, Kind: valuePlanning7A3C70BD}
	}
	return executionPhaseDescriptor(event.StepID, step.Title)
}

func executionPhaseDescriptor(stepID, title string) phaseDescriptor {
	return phaseDescriptor{Key: "execution:" + stepID, Kind: valueExecution22CE8488, Title: title}
}

func projectPhasesFrom(run model.Run, steps []model.Step, base []PhaseView, events []model.Event) []PhaseView {
	stepByID := workbenchStepsByID(steps)
	builders := phaseBuildersFromBase(base)
	for _, event := range events {
		if event.EventType == valueMessageDelta8D963128 {
			continue
		}
		payload := map[string]interface{}{}
		_ = json.Unmarshal([]byte(event.PayloadJSON), &payload)
		descriptor, include := classifyPhaseEvent(event, payload, stepByID, len(steps))
		if !include {
			continue
		}
		item := ensurePhaseBuilder(builders, run.RunID, descriptor.Key, descriptor.Kind, descriptor.Title, event)
		applyPhaseEvent(item, event, payload, run.Status)
	}
	return finalizePhaseViews(builders)
}

func applyPhaseEvent(item *phaseBuilder, event model.Event, payload map[string]interface{}, runStatus string) {
	if event.StepID != "" {
		item.stepSet[event.StepID] = struct{}{}
	}
	if event.ToolCallID != "" {
		item.toolSet[event.ToolCallID] = struct{}{}
	}
	if outputID := workbenchPayloadString(payload, "outputID"); outputID != "" {
		item.outputSet[outputID] = struct{}{}
	}
	if event.Summary != "" {
		item.Summary = event.Summary
	}
	item.Status = phaseStatusFromEvent(item.Status, event, runStatus)
	if isPhaseClosingEvent(event.EventType) {
		ended := event.StartedAt
		item.EndedAt = &ended
	}
}

func finalizePhaseViews(builders map[string]*phaseBuilder) []PhaseView {
	result := make([]PhaseView, 0, len(builders))
	for _, item := range builders {
		item.StepIDs = sortedStringSet(item.stepSet)
		item.ToolCallIDs = sortedStringSet(item.toolSet)
		item.OutputIDs = sortedStringSet(item.outputSet)
		if item.EndSeq == item.StartSeq && item.EndedAt == nil {
			item.EndSeq = 0
		}
		result = append(result, item.PhaseView)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartSeq < result[j].StartSeq })
	return result
}

func phaseStatusFromEvent(current string, event model.Event, runStatus string) string {
	if phaseStatusIsTerminal(current) {
		return current
	}
	if current == model.RunStatusSuspended && event.EventType != valueRunResumedB398BE30 && event.EventType != valueStepResumed395D0C55 {
		return current
	}
	if status, matched := phaseStatusForEvent(event); matched {
		return status
	}
	if current == valueQueued5052B952 && runStatus != "" {
		return runStatus
	}
	return current
}

func phaseStatusIsTerminal(status string) bool {
	return status == model.RunStatusCompleted || status == model.RunStatusFailed || status == model.RunStatusCancelled
}

func phaseStatusForEvent(event model.Event) (string, bool) {
	if event.EventType == valueRunResumedB398BE30 && event.Status == model.RunStatusWaitingInput {
		return model.RunStatusWaitingInput, true
	}
	exact := map[string]string{
		valueContextCompiled46DA71A3: model.RunStatusCompleted,
		valueRunFailedD21BA399:       model.RunStatusFailed,
		valueRunCancelledD74AD332:    model.RunStatusCancelled,
		valueRunSuspendedA2ED2B05:    model.RunStatusSuspended,
		valueRunWaitingHandoff:       model.RunStatusWaitingHandoff,
		"run.waiting_timer":          model.RunStatusWaitingTimer,
		"run.cancelling":             model.RunStatusCancelling,
		"run.compensating":           model.RunStatusCompensating,
		"interaction.expired":        model.RunStatusSuspended,
		"interaction.created":        model.RunStatusWaitingInput,
		valueRunPreparing142D9E38:    model.RunStatusRunning,
	}
	if status, ok := exact[event.EventType]; ok {
		return status, true
	}
	rules := []struct{ suffix, status string }{
		{".failed", model.RunStatusFailed}, {".cancelled", model.RunStatusCancelled}, {".suspended", model.RunStatusSuspended},
		{".completed", model.RunStatusCompleted}, {".resolved", model.RunStatusCompleted}, {".approved", model.RunStatusCompleted},
		{".waiting_input", model.RunStatusWaitingInput}, {".waiting_handoff", model.RunStatusWaitingHandoff}, {".waiting_timer", model.RunStatusWaitingTimer},
		{".cancelling", model.RunStatusCancelling}, {".compensating", model.RunStatusCompensating}, {".started", model.RunStatusRunning}, {".resumed", model.RunStatusRunning},
	}
	for _, rule := range rules {
		if strings.HasSuffix(event.EventType, rule.suffix) {
			return rule.status, true
		}
	}
	return "", false
}

func isRunTerminalEvent(value string) bool {
	return value == valueRunCompleted20A7FCFE || value == valueRunFailedD21BA399 || value == valueRunCancelledD74AD332 || value == valueRunSuspendedA2ED2B05
}
func isPhaseClosingEvent(value string) bool {
	return value == valueContextCompiled46DA71A3 || value == "interaction.expired" || strings.HasSuffix(value, ".completed") || strings.HasSuffix(value, ".failed") || strings.HasSuffix(value, ".cancelled") || strings.HasSuffix(value, ".suspended") || strings.HasSuffix(value, ".resolved") || strings.HasSuffix(value, ".approved")
}

func workbenchPayloadString(payload map[string]interface{}, key string) string {
	value := strings.TrimSpace(fmt.Sprint(payload[key]))
	if value == "<nil>" {
		return ""
	}
	return value
}

func phaseID(runID, key string) string {
	sum := sha256.Sum256([]byte(runID + ":" + key))
	return "phase_" + hex.EncodeToString(sum[:12])
}
func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func phaseViewsToDomain(runID string, phases []PhaseView) []model.PhaseProjection {
	result := make([]model.PhaseProjection, 0, len(phases))
	for _, item := range phases {
		result = append(result, model.PhaseProjection{PhaseID: item.PhaseID, RunID: runID, Kind: item.Kind, Title: item.Title, Summary: item.Summary, Status: item.Status, StartSeq: item.StartSeq, EndSeq: item.EndSeq, StepIDsJSON: encodeStringSlice(item.StepIDs), ToolCallIDsJSON: encodeStringSlice(item.ToolCallIDs), OutputIDsJSON: encodeStringSlice(item.OutputIDs), StartedAt: item.StartedAt, EndedAt: item.EndedAt})
	}
	return result
}

func encodeStringSlice(values []string) string {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func phaseViewsFromDomain(phases []model.PhaseProjection) []PhaseView {
	result := make([]PhaseView, 0, len(phases))
	for _, item := range phases {
		var steps, tools, outputs []string
		_ = json.Unmarshal([]byte(item.StepIDsJSON), &steps)
		_ = json.Unmarshal([]byte(item.ToolCallIDsJSON), &tools)
		_ = json.Unmarshal([]byte(item.OutputIDsJSON), &outputs)
		result = append(result, PhaseView{PhaseID: item.PhaseID, Kind: item.Kind, Title: item.Title, Summary: item.Summary, Status: item.Status, StartSeq: item.StartSeq, EndSeq: item.EndSeq, StepIDs: steps, ToolCallIDs: tools, OutputIDs: outputs, StartedAt: item.StartedAt, EndedAt: item.EndedAt})
	}
	return result
}

func buildWorkbenchOverview(run model.Run, phases []PhaseView, events []model.Event, config *TextRunConfigSummary) WorkbenchOverview {
	current := ""
	for index := range phases {
		if phases[index].Status == model.RunStatusRunning || phases[index].Status == model.RunStatusWaitingInput || phases[index].Status == model.RunStatusWaitingHandoff || phases[index].Status == model.RunStatusSuspended {
			current = phases[index].PhaseID
		}
	}
	if current == "" && len(phases) > 0 {
		current = phases[len(phases)-1].PhaseID
	}
	strategy := ""
	if config != nil {
		strategy = config.Strategy
	}
	return WorkbenchOverview{Goal: run.Goal, Status: run.Status, CurrentPhaseID: current, StatusReason: run.StatusReason, ErrorCode: run.ErrorCode, ErrorMessage: run.ErrorMessage, Strategy: strategy, PlannerRepairs: plannerRepairCount(events), StartedAt: run.StartedAt, EndedAt: run.EndedAt, LLMCalls: run.LLMCallsCount, ToolCalls: run.ToolCallsCount, InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, ReasoningTokens: run.ReasoningTokens, BilledCurrency: run.BilledCurrency, BilledNanousd: run.BilledNanousd}
}

func plannerRepairCount(events []model.Event) int {
	count := 0
	for _, event := range events {
		if event.EventType != valueUsageUpdatedBD37B6AA {
			continue
		}
		var payload map[string]interface{}
		if json.Unmarshal([]byte(event.PayloadJSON), &payload) == nil && workbenchPayloadString(payload, valuePhaseB99DC3AB) == "planner_repair" {
			count++
		}
	}
	return count
}

func buildToolGroups(phases []PhaseView, events []model.Event) []ToolGroupView {
	result := make([]ToolGroupView, 0)
	var current *ToolGroupView
	seen := map[string]struct{}{}
	for _, event := range events {
		if !strings.HasPrefix(event.EventType, "tool.") || strings.TrimSpace(event.ToolCallID) == "" {
			current = nil
			seen = map[string]struct{}{}
			continue
		}
		phaseID := phaseIDForToolEvent(phases, event)
		if current == nil || current.StepID != event.StepID || current.PhaseID != phaseID {
			result = append(result, ToolGroupView{GroupID: fmt.Sprintf("tools_%s_%d", phaseID, event.Seq), PhaseID: phaseID, StepID: event.StepID, Status: model.RunStatusRunning, StartSeq: int64(event.Seq), EndSeq: int64(event.Seq), ToolNames: map[string]string{}, ToolEventIDs: map[string]string{}, ToolStatuses: map[string]string{}})
			current = &result[len(result)-1]
			seen = map[string]struct{}{}
		}
		if _, exists := seen[event.ToolCallID]; !exists {
			current.ToolCallIDs = append(current.ToolCallIDs, event.ToolCallID)
			seen[event.ToolCallID] = struct{}{}
		}
		name := strings.TrimSpace(event.ToolName)
		if name == "" {
			name = "Tool"
		}
		current.ToolNames[event.ToolCallID] = name
		current.ToolEventIDs[event.ToolCallID] = event.EventID
		current.ToolStatuses[event.ToolCallID] = runEventStatus(event.EventType)
		if current.Title == "" {
			current.Title = runToolEventTitle(name, event.PayloadJSON)
		}
		current.EndSeq = int64(event.Seq)
		current.Status = phaseStatusFromEvent(current.Status, event, "")
	}
	return result
}

func runToolEventTitle(name, payloadJSON string) string {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(payloadJSON), &payload)
	parts := []string{name}
	for _, key := range []string{valueAction11628904, "resource", "target"} {
		value := workbenchPayloadString(payload, key)
		if value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " · ")
}

func phaseIDForToolEvent(phases []PhaseView, event model.Event) string {
	for _, phase := range phases {
		if phase.Kind == valueExecution22CE8488 && containsString(phase.StepIDs, event.StepID) {
			return phase.PhaseID
		}
	}
	for _, phase := range phases {
		if int64(event.Seq) >= phase.StartSeq && (phase.EndSeq == 0 || int64(event.Seq) <= phase.EndSeq) {
			return phase.PhaseID
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func buildWorkbenchGraph(workbench *Workbench) ([]WorkbenchGraphNode, []WorkbenchGraphEdge, map[string]WorkbenchSelectionTarget) {
	goalID := "goal:" + workbench.Run.RunID
	builder := workbenchGraphBuilder{
		nodes:  []WorkbenchGraphNode{{ID: goalID, Kind: valueGoal33002E73, Label: workbench.Run.Goal, Status: workbench.Run.Status, EntityID: workbench.Run.RunID}},
		index:  map[string]WorkbenchSelectionTarget{goalID: {Tab: "overview", Kind: valuePhaseB99DC3AB, ID: workbench.Overview.CurrentPhaseID}},
		goalID: goalID,
	}
	builder.appendPhases(workbench.Phases)
	builder.appendPlan(workbench.Plan)
	builder.appendSteps(workbench.Steps, workbench.Phases)
	builder.appendDependencies(workbench.Steps)
	builder.appendToolGroups(workbench.ToolGroups)
	builder.appendCheckpoints(workbench.Checkpoints, workbench.Phases)
	builder.appendOutputs(workbench.Outputs, workbench.Phases)
	return builder.nodes, builder.edges, builder.index
}

type workbenchGraphBuilder struct {
	nodes        []WorkbenchGraphNode
	edges        []WorkbenchGraphEdge
	index        map[string]WorkbenchSelectionTarget
	goalID       string
	previous     string
	planNodeID   string
	stepNodeByID map[string]string
}

func (builder *workbenchGraphBuilder) appendPhases(phases []PhaseView) {
	previous := builder.goalID
	for _, phase := range phases {
		id := "phase:" + phase.PhaseID
		builder.nodes = append(builder.nodes, WorkbenchGraphNode{ID: id, Kind: phase.Kind, Label: phase.Title, Status: phase.Status, EntityID: phase.PhaseID, PhaseID: phase.PhaseID})
		builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: previous + ">" + id, Source: previous, Target: id, Kind: "next"})
		builder.index[id] = WorkbenchSelectionTarget{Tab: valueTrace48C00821, Kind: valuePhaseB99DC3AB, ID: phase.PhaseID, PhaseID: phase.PhaseID, Seq: phase.StartSeq}
		previous = id
	}
	builder.previous = previous
}

func (builder *workbenchGraphBuilder) appendPlan(planView *PlanView) {
	if planView == nil || planView.Current == nil {
		return
	}
	plan := planView.Current
	builder.planNodeID = "plan:" + plan.PlanID
	builder.nodes = append(builder.nodes, WorkbenchGraphNode{ID: builder.planNodeID, Kind: valuePlan78EDC9FE, Label: firstNonEmptyString(plan.Summary, plan.Goal), Status: plan.Status, EntityID: plan.PlanID})
	builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: builder.goalID + ">" + builder.planNodeID, Source: builder.goalID, Target: builder.planNodeID, Kind: "planned"})
}

func (builder *workbenchGraphBuilder) appendSteps(steps []model.Step, phases []PhaseView) {
	builder.stepNodeByID = make(map[string]string, len(steps))
	for _, step := range steps {
		if step.ParentStepID == "" {
			continue
		}
		id := "step:" + step.StepID
		builder.stepNodeByID[step.StepID] = id
		phaseID := phaseIDForRunStep(phases, step.StepID)
		builder.nodes = append(builder.nodes, WorkbenchGraphNode{ID: id, Kind: valueStep1396E1CE, Label: step.Title, Status: step.Status, EntityID: step.StepID, PhaseID: phaseID})
		source := builder.planNodeID
		if source == "" {
			source = builder.goalID
		}
		var dependencies []string
		_ = json.Unmarshal([]byte(step.DependsOnJSON), &dependencies)
		if len(dependencies) == 0 {
			builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: source + ">" + id, Source: source, Target: id, Kind: "contains"})
		}
		builder.index[id] = WorkbenchSelectionTarget{Tab: "overview", Kind: valueStep1396E1CE, ID: step.StepID}
	}
}

func (builder *workbenchGraphBuilder) appendDependencies(steps []model.Step) {
	for _, step := range steps {
		target := builder.stepNodeByID[step.StepID]
		if target == "" {
			continue
		}
		var dependencies []string
		_ = json.Unmarshal([]byte(step.DependsOnJSON), &dependencies)
		for _, dependency := range dependencies {
			if source := builder.stepNodeByID[dependency]; source != "" {
				builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: source + ">" + target, Source: source, Target: target, Kind: "depends_on"})
			}
		}
	}
}

func (builder *workbenchGraphBuilder) appendToolGroups(groups []ToolGroupView) {
	for _, group := range groups {
		source := builder.stepNodeByID[group.StepID]
		if source == "" {
			source = "phase:" + group.PhaseID
		}
		for _, toolCallID := range group.ToolCallIDs {
			id := "tool:" + toolCallID
			label := firstNonEmptyString(group.ToolNames[toolCallID], group.Title, "Tool")
			builder.nodes = append(builder.nodes, WorkbenchGraphNode{ID: id, Kind: valueToolE422AB02, Label: label, Subtitle: toolCallID, Status: group.Status, EntityID: toolCallID, PhaseID: group.PhaseID})
			builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: source + ">" + id, Source: source, Target: id, Kind: "used"})
			builder.index[id] = WorkbenchSelectionTarget{Tab: "details", Kind: valueToolE422AB02, ID: toolCallID, PhaseID: group.PhaseID, Seq: group.StartSeq}
		}
	}
}

func (builder *workbenchGraphBuilder) appendCheckpoints(checkpoints []model.Checkpoint, phases []PhaseView) {
	for _, checkpoint := range checkpoints {
		id := "checkpoint:" + checkpoint.CheckpointID
		source := builder.stepNodeByID[checkpoint.StepID]
		if source == "" {
			source = builder.previous
		}
		phaseID := phaseIDForRunStep(phases, checkpoint.StepID)
		builder.nodes = append(builder.nodes, WorkbenchGraphNode{ID: id, Kind: valueCheckpointFCE17F9B, Label: checkpoint.Kind, Status: checkpoint.Status, EntityID: checkpoint.CheckpointID, PhaseID: phaseID})
		builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: source + ">" + id, Source: source, Target: id, Kind: valueCheckpointFCE17F9B})
		builder.index[id] = WorkbenchSelectionTarget{Tab: "details", Kind: valueCheckpointFCE17F9B, ID: checkpoint.CheckpointID, Seq: checkpoint.EventSeq}
	}
}

func (builder *workbenchGraphBuilder) appendOutputs(outputs []model.OutputRef, phases []PhaseView) {
	for _, output := range outputs {
		id := "output:" + output.OutputID
		outputPhaseID := ""
		source := builder.previous
		for _, phase := range phases {
			for _, value := range phase.OutputIDs {
				if value == output.OutputID {
					source = "phase:" + phase.PhaseID
					outputPhaseID = phase.PhaseID
				}
			}
		}
		builder.nodes = append(builder.nodes, WorkbenchGraphNode{ID: id, Kind: valueOutput2060C6DF, Label: output.Title, Status: output.Status, EntityID: output.OutputID, PhaseID: outputPhaseID})
		builder.edges = append(builder.edges, WorkbenchGraphEdge{ID: source + ">" + id, Source: source, Target: id, Kind: "produced"})
		builder.index[id] = WorkbenchSelectionTarget{Tab: "outputs", Kind: valueOutput2060C6DF, ID: output.OutputID}
	}
}

func phaseIDForRunStep(phases []PhaseView, stepID string) string {
	for _, phase := range phases {
		if containsString(phase.StepIDs, stepID) {
			return phase.PhaseID
		}
	}
	return ""
}
