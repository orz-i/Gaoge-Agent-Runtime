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

func TestApplyEventProjectsRouteWithoutUsage(t *testing.T) {
	run := domain.Run{RunID: projectionTestRunID}
	applyProjectionEvent(t, &run, nil, domain.Event{
		EventType: eventLLMRouteSelected,
		PayloadJSON: `{
			"upstreamName":"GPT upstream",
			"bindingCode":"gpt-primary",
			"upstreamModel":"gpt-5.6-terra",
			"protocol":"openai_responses",
			"endpoint":"/v1/responses",
			"modelVendor":"openai",
			"modelIcon":"openai"
		}`,
	})
	gotRoute := routePayload{
		UpstreamName: run.UpstreamName, BindingCode: run.RoutedBindingCode,
		UpstreamModel: run.UpstreamModelName, Protocol: run.ProviderProtocol,
		Endpoint: run.Endpoint, ModelVendor: run.ModelVendor, ModelIcon: run.ModelIcon,
	}
	wantRoute := routePayload{
		UpstreamName: "GPT upstream", BindingCode: "gpt-primary",
		UpstreamModel: "gpt-5.6-terra", Protocol: "openai_responses",
		Endpoint: "/v1/responses", ModelVendor: "openai", ModelIcon: "openai",
	}
	if gotRoute != wantRoute {
		t.Fatalf("route projection = %#v, want %#v", gotRoute, wantRoute)
	}
	gotUsage := struct {
		Calls                                                          int
		Input, Output, CacheRead, CacheWrite, Reasoning, BilledNanousd int64
		BillingSnapshot                                                string
	}{
		Calls: run.LLMCallsCount, Input: run.InputTokens, Output: run.OutputTokens,
		CacheRead: run.CacheReadTokens, CacheWrite: run.CacheWriteTokens,
		Reasoning: run.ReasoningTokens, BilledNanousd: run.BilledNanousd,
		BillingSnapshot: run.LastBillingSnapshotJSON,
	}
	if gotUsage != (struct {
		Calls                                                          int
		Input, Output, CacheRead, CacheWrite, Reasoning, BilledNanousd int64
		BillingSnapshot                                                string
	}{}) {
		t.Fatalf("route event changed usage or billing: %#v", gotUsage)
	}

	applyProjectionEvent(t, &run, nil, domain.Event{
		EventType:   eventUsageUpdated,
		PayloadJSON: `{"inputTokens":1,"outputTokens":2,"upstreamName":"confirmed upstream","bindingCode":"confirmed","upstreamModel":"gpt-5.6-terra-2","protocol":"openai_chat_completions"}`,
	})
	if run.LLMCallsCount != 1 || run.UpstreamName != "confirmed upstream" || run.RoutedBindingCode != "confirmed" || run.UpstreamModelName != "gpt-5.6-terra-2" || run.ProviderProtocol != "openai_chat_completions" {
		t.Fatalf("usage did not confirm route exactly once: %#v", run)
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

func TestApplyEventRejectsMalformedRoute(t *testing.T) {
	run := domain.Run{RunID: projectionTestRunID}
	err := ApplyEvent(&run, nil, domain.Event{RunID: run.RunID, EventType: eventLLMRouteSelected, PayloadJSON: "{"})
	if err == nil {
		t.Fatal("malformed route event was accepted")
	}
}

func TestApplyEventRejectsWrongRun(t *testing.T) {
	run := domain.Run{RunID: projectionTestRunID}
	if err := ApplyEvent(&run, nil, domain.Event{RunID: "run_other", EventType: "run.started"}); err == nil {
		t.Fatal("event for another run was accepted")
	}
}
