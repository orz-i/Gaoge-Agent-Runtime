package projection

import (
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const projectionTestRunID = "run_projection"

func TestApplyEventProjectsStepLifecycle(t *testing.T) {
	started := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	run := domain.Run{RunID: projectionTestRunID, Status: domain.RunStatusQueued, StartedAt: started}
	step := domain.Step{RunID: run.RunID, StepID: "step_root", Status: domain.RunStatusQueued}

	applyProjectionEvent(t, &run, &step, domain.Event{EventType: "step.started", CreatedAt: started.Add(time.Second)})
	assertProjectedStepStart(t, run, step, started.Add(time.Second))

	ended := started.Add(4 * time.Second)
	applyProjectionEvent(t, &run, &step, domain.Event{EventType: "step.completed", Summary: "done", EndedAt: &ended})
	assertProjectedStepCompletion(t, step, ended)
	applyProjectionEvent(t, &run, &step, domain.Event{EventType: "run.completed", Summary: "complete", EndedAt: &ended})
	assertProjectedRunCompletion(t, run, ended)
}

func assertProjectedStepStart(t *testing.T, run domain.Run, step domain.Step, started time.Time) {
	t.Helper()
	if run.CurrentStepID != step.StepID || step.Status != domain.RunStatusRunning || !step.StartedAt.Equal(started) {
		t.Fatalf("step start projection mismatch: run=%#v step=%#v", run, step)
	}
}

func assertProjectedStepCompletion(t *testing.T, step domain.Step, ended time.Time) {
	t.Helper()
	if step.Status != domain.RunStatusCompleted || step.ResultSummary != "done" || step.EndedAt == nil || !step.EndedAt.Equal(ended) {
		t.Fatalf("step completion projection mismatch: %#v", step)
	}
}

func assertProjectedRunCompletion(t *testing.T, run domain.Run, ended time.Time) {
	t.Helper()
	if run.Status != domain.RunStatusCompleted || run.TotalLatencyMS != 4000 || run.EndedAt == nil || !run.EndedAt.Equal(ended) {
		t.Fatalf("run completion projection mismatch: %#v", run)
	}
}

func TestApplyEventProjectsUsageAndLatency(t *testing.T) {
	started := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	run := domain.Run{RunID: projectionTestRunID, StartedAt: started}

	applyProjectionEvent(t, &run, nil, domain.Event{EventType: eventMessageDelta, CreatedAt: started.Add(1250 * time.Millisecond)})
	applyProjectionEvent(t, &run, nil, domain.Event{EventType: eventMessageDelta, CreatedAt: started.Add(2 * time.Second)})
	if run.FirstTokenLatencyMS != 1250 {
		t.Fatalf("first token latency = %d, want 1250", run.FirstTokenLatencyMS)
	}

	applyProjectionEvent(t, &run, nil, domain.Event{
		EventType:   eventUsageUpdated,
		PayloadJSON: `{"inputTokens":12,"outputTokens":256,"cacheReadTokens":4,"cacheWriteTokens":3,"reasoningTokens":9,"bindingCode":"gemini","upstreamModel":"gemini-2.5-flash","protocol":"google_generate_content"}`,
	})
	applyProjectionEvent(t, &run, nil, domain.Event{EventType: eventUsageUpdated, PayloadJSON: `{"inputTokens":2,"outputTokens":8}`})
	assertProjectedUsage(t, run)

	applyProjectionEvent(t, &run, nil, domain.Event{EventType: eventToolStarted})
	if run.ToolCallsCount != 1 {
		t.Fatalf("tool calls = %d, want 1", run.ToolCallsCount)
	}
}

func assertProjectedUsage(t *testing.T, run domain.Run) {
	t.Helper()
	if run.InputTokens != 14 || run.OutputTokens != 264 || run.CacheReadTokens != 4 || run.CacheWriteTokens != 3 || run.ReasoningTokens != 9 {
		t.Fatalf("usage projection mismatch: %#v", run)
	}
	if run.LLMCallsCount != 2 || run.RoutedBindingCode != "gemini" || run.UpstreamModelName != "gemini-2.5-flash" || run.ProviderProtocol != "google_generate_content" {
		t.Fatalf("route or call projection mismatch: %#v", run)
	}
}

func applyProjectionEvent(t *testing.T, run *domain.Run, step *domain.Step, event domain.Event) {
	t.Helper()
	event.RunID = run.RunID
	if step != nil {
		event.StepID = step.StepID
	}
	if err := ApplyEvent(run, step, event); err != nil {
		t.Fatalf("ApplyEvent(%s): %v", event.EventType, err)
	}
}

func TestApplyEventRejectsMalformedUsage(t *testing.T) {
	run := domain.Run{RunID: projectionTestRunID}
	err := ApplyEvent(&run, nil, domain.Event{RunID: run.RunID, EventType: eventUsageUpdated, PayloadJSON: "{"})
	if err == nil {
		t.Fatal("malformed usage event was accepted")
	}
}

func TestApplyEventRejectsWrongRun(t *testing.T) {
	run := domain.Run{RunID: projectionTestRunID}
	if err := ApplyEvent(&run, nil, domain.Event{RunID: "run_other", EventType: "run.started"}); err == nil {
		t.Fatal("event for another run was accepted")
	}
}
