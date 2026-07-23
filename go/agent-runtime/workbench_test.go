package agentruntime

import (
	"reflect"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueRoot7E7D4986    = "root"
	valueStepOneE3346110 = "step_one"
	valueToolOneDF00DEED = "tool_one"
)

const (
	valueCallA231AAEE0             = "call_a"
	valueCallB10E9B2CF             = "call_b"
	valueCallC79CB0D79             = "call_c"
	valueCheckpointCreated24978044 = "checkpoint.created"
	valueContext30E95E1F           = "context"
	valueContextCompiledDA47CFB9   = "context.compiled"
	valueExecution47590376         = "execution"
	valueMessageDelta86C6D8E9      = "message.delta"
	valueOrchestration99394298     = "orchestration"
	valueRunCompleted844FE3B6      = "run.completed"
	valueRunPreparingA98EC30F      = "run.preparing"
	valueRunResumedB2BCF4D9        = "run.resumed"
	valueRunStarted6877EF0A        = "run.started"
	valueStep144F07B4              = "step"
	valueStepStarted01EE95E9       = "step.started"
	valueToolCompletedD8BE44E1     = "tool.completed"
	valueToolStarted01F022F7       = "tool.started"
	valueUsageUpdated0D557F58      = "usage.updated"
	valueShellCommand              = "shell_command"
)

func TestProjectPhasesIncrementalMatchesFullProjection(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	run := model.Run{RunID: "run_workbench", Goal: "build report", Status: model.RunStatusCompleted}
	steps := []model.Step{
		{StepID: valueRoot7E7D4986, RunID: run.RunID, Kind: valueOrchestration99394298, Title: "Orchestration", Status: model.RunStatusCompleted},
		{StepID: valueStepOneE3346110, RunID: run.RunID, ParentStepID: valueRoot7E7D4986, PlanID: "plan_one", Kind: valueExecution47590376, Title: "Collect evidence", Status: model.RunStatusCompleted},
	}
	events := []model.Event{
		{RunID: run.RunID, EventType: valueRunStarted6877EF0A, Seq: 1, StartedAt: started},
		{RunID: run.RunID, EventType: valueContextCompiledDA47CFB9, Seq: 2, StartedAt: started.Add(time.Second)},
		{RunID: run.RunID, EventType: valueRunPreparingA98EC30F, Seq: 3, StepID: valueRoot7E7D4986, StartedAt: started.Add(2 * time.Second)},
		{RunID: run.RunID, EventType: "plan.approved", Seq: 4, StepID: valueRoot7E7D4986, StartedAt: started.Add(3 * time.Second)},
		{RunID: run.RunID, EventType: valueStepStarted01EE95E9, Seq: 5, StepID: valueStepOneE3346110, StartedAt: started.Add(4 * time.Second)},
		{RunID: run.RunID, EventType: valueMessageDelta86C6D8E9, Seq: 6, StepID: valueStepOneE3346110, StartedAt: started.Add(5 * time.Second)},
		{RunID: run.RunID, EventType: valueToolStarted01F022F7, Seq: 7, StepID: valueStepOneE3346110, ToolCallID: valueToolOneDF00DEED, StartedAt: started.Add(6 * time.Second)},
		{RunID: run.RunID, EventType: valueToolCompletedD8BE44E1, Seq: 8, StepID: valueStepOneE3346110, ToolCallID: valueToolOneDF00DEED, StartedAt: started.Add(7 * time.Second)},
		{RunID: run.RunID, EventType: "output.created", Seq: 9, StepID: valueStepOneE3346110, PayloadJSON: `{"outputID":"report"}`, StartedAt: started.Add(8 * time.Second)},
		{RunID: run.RunID, EventType: "step.completed", Seq: 10, StepID: valueStepOneE3346110, Summary: "Evidence collected", StartedAt: started.Add(9 * time.Second)},
		{RunID: run.RunID, EventType: valueUsageUpdated0D557F58, Seq: 11, PayloadJSON: `{"phase":"synthesis"}`, StartedAt: started.Add(10 * time.Second)},
		{RunID: run.RunID, EventType: "message.completed", Seq: 12, StartedAt: started.Add(11 * time.Second)},
		{RunID: run.RunID, EventType: valueRunCompleted844FE3B6, Seq: 13, StartedAt: started.Add(12 * time.Second)},
	}

	full := projectPhases(run, steps, events)
	first := projectPhases(run, steps, events[:8])
	incremental := projectPhasesFrom(run, steps, first, events[8:])
	assertIncrementalPhases(t, full, incremental)
	assertProjectedPhaseShape(t, full)
}

func assertIncrementalPhases(t *testing.T, full, incremental []PhaseView) {
	t.Helper()
	if !reflect.DeepEqual(full, incremental) {
		t.Fatalf("incremental projection differs\nfull=%#v\nincremental=%#v", full, incremental)
	}
}

func assertProjectedPhaseShape(t *testing.T, full []PhaseView) {
	t.Helper()
	if len(full) != 4 {
		t.Fatalf("phase count = %d, want context/planning/execution/synthesis", len(full))
	}
	assertContextPhase(t, full[0])
	assertExecutionPhase(t, full[2])
}

func assertContextPhase(t *testing.T, phase PhaseView) {
	t.Helper()
	if phase.Kind != valueContext30E95E1F || phase.Status != model.RunStatusCompleted || phase.EndSeq != 2 {
		t.Fatalf("context phase = %#v", phase)
	}
}

func assertExecutionPhase(t *testing.T, execution PhaseView) {
	t.Helper()
	if execution.Kind != valueExecution47590376 || execution.Title != "Collect evidence" || !reflect.DeepEqual(execution.ToolCallIDs, []string{valueToolOneDF00DEED}) || !reflect.DeepEqual(execution.OutputIDs, []string{"report"}) {
		t.Fatalf("execution phase = %#v", execution)
	}
	if execution.StartSeq != 5 || execution.EndSeq != 10 {
		t.Fatalf("execution bounds = %d..%d", execution.StartSeq, execution.EndSeq)
	}
}

func TestWorkbenchProjectionDoesNotDependOnMessageDelta(t *testing.T) {
	t.Parallel()
	run := model.Run{RunID: "run_delta", Status: model.RunStatusRunning}
	step := model.Step{StepID: valueStep144F07B4, RunID: run.RunID, Title: "Execute", Status: model.RunStatusRunning}
	without := []model.Event{{RunID: run.RunID, EventType: valueStepStarted01EE95E9, StepID: step.StepID, Seq: 1}}
	with := append(append([]model.Event{}, without...), model.Event{RunID: run.RunID, EventType: valueMessageDelta86C6D8E9, StepID: step.StepID, Seq: 2})
	left := projectPhases(run, []model.Step{step}, without)
	right := projectPhases(run, []model.Step{step}, with)
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("message delta changed semantic identity: %#v vs %#v", left, right)
	}
}

func TestWorkbenchProjectionIgnoresLongRunMessageDeltaVolume(t *testing.T) {
	t.Parallel()
	run := model.Run{RunID: "run_long", Status: model.RunStatusCompleted}
	step := model.Step{StepID: "step_long", RunID: run.RunID, Title: "Long execution", Status: model.RunStatusCompleted}
	events := make([]model.Event, 0, 50002)
	events = append(events, model.Event{RunID: run.RunID, EventType: valueStepStarted01EE95E9, StepID: step.StepID, Seq: 1})
	for seq := int64(2); seq <= 50001; seq++ {
		events = append(events, model.Event{RunID: run.RunID, EventType: valueMessageDelta86C6D8E9, StepID: step.StepID, Seq: seq})
	}
	events = append(events, model.Event{RunID: run.RunID, EventType: "step.completed", StepID: step.StepID, Seq: 50002})

	phases := projectPhases(run, []model.Step{step}, events)
	if len(phases) != 1 || phases[0].StartSeq != 1 || phases[0].EndSeq != 50002 || phases[0].Status != model.RunStatusCompleted {
		t.Fatalf("long-run projection = %#v", phases)
	}
}

func TestRunToolGroupsSplitAtSemanticBoundary(t *testing.T) {
	t.Parallel()
	phases := []PhaseView{{PhaseID: "phase_execution", Kind: valueExecution47590376, StepIDs: []string{valueStepOneE3346110}, StartSeq: 1, EndSeq: 9}}
	events := []model.Event{
		{EventID: "event_a_started", EventType: valueToolStarted01F022F7, StepID: valueStepOneE3346110, ToolCallID: valueCallA231AAEE0, ToolName: valueShellCommand, Seq: 1},
		{EventID: "event_a_completed", EventType: valueToolCompletedD8BE44E1, StepID: valueStepOneE3346110, ToolCallID: valueCallA231AAEE0, ToolName: valueShellCommand, Seq: 2},
		{EventType: valueToolStarted01F022F7, StepID: valueStepOneE3346110, ToolCallID: valueCallB10E9B2CF, Seq: 3},
		{EventType: valueToolCompletedD8BE44E1, StepID: valueStepOneE3346110, ToolCallID: valueCallB10E9B2CF, Seq: 4},
		{EventType: valueCheckpointCreated24978044, StepID: valueStepOneE3346110, Seq: 5},
		{EventType: valueToolStarted01F022F7, StepID: valueStepOneE3346110, ToolCallID: valueCallC79CB0D79, Seq: 6},
		{EventType: valueToolCompletedD8BE44E1, StepID: valueStepOneE3346110, ToolCallID: valueCallC79CB0D79, Seq: 7},
	}

	groups := buildToolGroups(phases, events)
	assertRunToolGroupCount(t, groups)
	assertFirstRunToolGroup(t, groups[0])
	assertSecondRunToolGroup(t, groups[1])
}

func assertRunToolGroupCount(t *testing.T, groups []ToolGroupView) {
	t.Helper()
	if len(groups) != 2 {
		t.Fatalf("tool groups = %#v", groups)
	}
}

func assertFirstRunToolGroup(t *testing.T, group ToolGroupView) {
	t.Helper()
	if !reflect.DeepEqual(group.ToolCallIDs, []string{valueCallA231AAEE0, valueCallB10E9B2CF}) || group.StartSeq != 1 || group.EndSeq != 4 {
		t.Fatalf("first group = %#v", group)
	}
	if group.ToolNames[valueCallA231AAEE0] != valueShellCommand || group.ToolEventIDs[valueCallA231AAEE0] != "event_a_completed" || group.ToolStatuses[valueCallA231AAEE0] != model.RunStatusCompleted {
		t.Fatalf("tool diagnostics index = %#v", group)
	}
}

func assertSecondRunToolGroup(t *testing.T, group ToolGroupView) {
	t.Helper()
	if !reflect.DeepEqual(group.ToolCallIDs, []string{valueCallC79CB0D79}) || group.StartSeq != 6 || group.EndSeq != 7 {
		t.Fatalf("second group = %#v", group)
	}
}

func TestWorkbenchHITLDoesNotReopenCompletedPlanningOrCompleteExecutionEarly(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	run := model.Run{RunID: "run_hitl", Status: model.RunStatusCompleted}
	steps := []model.Step{
		{StepID: valueRoot7E7D4986, RunID: run.RunID, Kind: valueOrchestration99394298, Status: model.RunStatusCompleted},
		{StepID: valueStepOneE3346110, RunID: run.RunID, ParentStepID: valueRoot7E7D4986, PlanID: "plan_one", Title: "Execute", Status: model.RunStatusCompleted},
	}
	event := func(seq int, eventType, stepID, payload string) model.Event {
		return model.Event{RunID: run.RunID, EventType: eventType, StepID: stepID, Seq: int64(seq), PayloadJSON: payload, StartedAt: started.Add(time.Duration(seq) * time.Second)}
	}
	events := []model.Event{
		event(1, valueRunStarted6877EF0A, valueRoot7E7D4986, `{}`),
		event(2, valueContextCompiledDA47CFB9, valueRoot7E7D4986, `{}`),
		event(3, valueRunPreparingA98EC30F, valueRoot7E7D4986, `{}`),
		event(4, "interaction.created", valueRoot7E7D4986, `{"interactionID":"plan_approval"}`),
		event(5, "run.waiting_input", valueRoot7E7D4986, `{"interactionID":"plan_approval"}`),
		event(6, "interaction.resolved", valueRoot7E7D4986, `{"interactionID":"plan_approval"}`),
		event(7, "plan.approved", valueRoot7E7D4986, `{}`),
		event(8, valueCheckpointCreated24978044, valueRoot7E7D4986, `{}`),
		{RunID: run.RunID, EventType: valueRunResumedB2BCF4D9, StepID: valueRoot7E7D4986, Status: model.RunStatusRunning, Seq: 9, PayloadJSON: `{"interactionID":"plan_approval","status":"running"}`, StartedAt: started.Add(9 * time.Second)},
		event(10, "interaction.created", valueStepOneE3346110, `{"interactionID":"step_approval"}`),
		event(11, "step.waiting_input", valueStepOneE3346110, `{"interactionID":"step_approval"}`),
		event(12, "run.waiting_input", valueStepOneE3346110, `{"interactionID":"step_approval"}`),
		event(13, "interaction.resolved", valueStepOneE3346110, `{"interactionID":"step_approval"}`),
		event(14, "step.approved", valueStepOneE3346110, `{"interactionID":"step_approval"}`),
		{RunID: run.RunID, EventType: valueRunResumedB2BCF4D9, StepID: valueStepOneE3346110, Status: model.RunStatusRunning, Seq: 15, PayloadJSON: `{"interactionID":"step_approval","status":"running"}`, StartedAt: started.Add(15 * time.Second)},
		event(16, valueStepStarted01EE95E9, valueStepOneE3346110, `{}`),
		event(17, "step.completed", valueStepOneE3346110, `{}`),
		event(18, valueUsageUpdated0D557F58, valueRoot7E7D4986, `{"phase":"synthesis"}`),
		event(19, "message.completed", valueRoot7E7D4986, `{}`),
		event(20, valueRunCompleted844FE3B6, valueRoot7E7D4986, `{}`),
	}

	phases := projectPhases(run, steps, events)
	byKind := map[string][]PhaseView{}
	for _, phase := range phases {
		byKind[phase.Kind] = append(byKind[phase.Kind], phase)
	}
	assertHITLPhaseStatuses(t, byKind)
	assertWorkbenchCurrentSynthesis(t, buildWorkbenchOverview(run, phases, nil, nil), byKind)
}

func assertHITLPhaseStatuses(t *testing.T, byKind map[string][]PhaseView) {
	t.Helper()
	if len(byKind["planning"]) != 1 || byKind["planning"][0].Status != model.RunStatusCompleted {
		t.Fatalf("planning phase reopened: %#v", byKind["planning"])
	}
	if len(byKind["interaction"]) != 2 || byKind["interaction"][0].Status != model.RunStatusCompleted || byKind["interaction"][1].Status != model.RunStatusCompleted {
		t.Fatalf("interaction phases = %#v", byKind["interaction"])
	}
	if len(byKind[valueExecution47590376]) != 1 || byKind[valueExecution47590376][0].Status != model.RunStatusCompleted || byKind[valueExecution47590376][0].StartSeq != 15 {
		t.Fatalf("execution phase completed before resume/start: %#v", byKind[valueExecution47590376])
	}
}

func assertWorkbenchCurrentSynthesis(t *testing.T, overview WorkbenchOverview, byKind map[string][]PhaseView) {
	t.Helper()
	if overview.CurrentPhaseID != byKind["synthesis"][0].PhaseID {
		t.Fatalf("current phase = %q, want synthesis %q", overview.CurrentPhaseID, byKind["synthesis"][0].PhaseID)
	}
}

func TestPhaseStatusOnlyExplicitResumeReopensSuspended(t *testing.T) {
	t.Parallel()
	suspended := phaseStatusFromEvent(model.RunStatusSuspended, model.Event{EventType: valueCheckpointCreated24978044}, model.RunStatusSuspended)
	if suspended != model.RunStatusSuspended {
		t.Fatalf("suspended phase changed without resume: %s", suspended)
	}
	resumed := phaseStatusFromEvent(model.RunStatusSuspended, model.Event{EventType: valueRunResumedB2BCF4D9, Status: model.RunStatusRunning}, model.RunStatusRunning)
	if resumed != model.RunStatusRunning {
		t.Fatalf("explicit resume did not reopen phase: %s", resumed)
	}
}
