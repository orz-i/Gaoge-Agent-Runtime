package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/evaluation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestA2ADeterministicScenarioCorpus(t *testing.T) {
	draft := loadA2AScenarioSuite(t, "testdata/a2a-eval-smoke.json")
	dataset, err := evaluation.CompileScenarioDataset(draft)
	if err != nil {
		t.Fatalf("compile A2A scenario dataset: %v", err)
	}
	runner, err := evaluation.NewScenarioRunner(a2aScenarioExecutor{t: t})
	if err != nil {
		t.Fatalf("create A2A scenario runner: %v", err)
	}
	record, err := runner.Execute(t.Context(), evaluation.ExecuteRequest{
		RequestID: "a2a-eval-smoke-v1", TargetName: "a2a-local-http",
		Dataset: dataset, MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("execute A2A scenario corpus: %v", err)
	}
	baseline := loadA2ABaseline(t, "testdata/a2a-eval-smoke-baseline.json")
	comparison, err := evaluation.CompareBaseline(record, baseline)
	if err != nil {
		t.Fatalf("compare A2A baseline (dataset hash %s): %v", dataset.Hash, err)
	}
	if !comparison.Passed {
		t.Fatalf("A2A baseline regressed: %v", comparison.Regressions)
	}
	encoded, err := json.Marshal(record.Report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("A2A eval report: %s", encoded)
}

type a2aScenarioExecutor struct {
	t *testing.T
}

type a2aScenarioInput struct {
	Operation string `json:"operation"`
}

func (executor a2aScenarioExecutor) Execute(
	ctx context.Context,
	request evaluation.ScenarioRequest,
) (evaluation.ScenarioObservation, error) {
	var input a2aScenarioInput
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	switch input.Operation {
	case "discover_send_terminal":
		return executor.discoverSendTerminal(ctx)
	case "stream_terminal":
		return executor.streamTerminal(ctx)
	case "input_required_cancel":
		return executor.inputRequiredCancel(ctx)
	case "shadow_resume_unknown":
		return executor.shadowResumeUnknown(ctx)
	default:
		return evaluation.ScenarioObservation{}, errors.New("unsupported A2A evaluation scenario")
	}
}

func (executor a2aScenarioExecutor) discoverSendTerminal(
	ctx context.Context,
) (evaluation.ScenarioObservation, error) {
	server, _ := newA2ATestServer(executor.t, false)
	observer := &testA2AObserver{name: "a2a-eval-observer"}
	client := newA2ATestClient(executor.t, server.Client(), observer)
	discovery, err := client.Discover(ctx, server.URL)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	interaction, err := client.SendMessage(ctx, discovery, SendRequest{
		MessageID: "a2a-eval-message", Text: "complete deterministically",
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	if interaction.Task == nil || interaction.Task.State != string(a2asdk.TaskStateCompleted) || !interaction.Task.Terminal {
		return evaluation.ScenarioObservation{}, errors.New("A2A task did not complete")
	}
	events := make([]string, 0, len(observer.events))
	for _, event := range observer.events {
		events = append(events, event.Type+":"+event.Status)
	}
	return evaluation.ScenarioObservation{
		Status: kernel.RunStatusCompleted, Revision: 1, EventTypes: events,
		Effects: []evaluation.EffectCount{
			{Key: "discovered_skill", Count: len(discovery.Skills)},
			{Key: "terminal_task", Count: 1},
		},
	}, nil
}

func (executor a2aScenarioExecutor) streamTerminal(ctx context.Context) (evaluation.ScenarioObservation, error) {
	server, _ := newA2ATestServer(executor.t, false)
	observer := &testA2AObserver{name: "a2a-stream-eval-observer"}
	client := newA2ATestClient(executor.t, server.Client(), observer)
	discovery, err := client.Discover(ctx, server.URL)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	count := 0
	terminal := 0
	for event, streamErr := range client.SendStreamingMessage(ctx, discovery, SendRequest{
		MessageID: "a2a-eval-stream", Text: "stream deterministically",
	}) {
		if streamErr != nil {
			return evaluation.ScenarioObservation{}, streamErr
		}
		count++
		if event.Task != nil && event.Task.Terminal {
			terminal++
		}
	}
	events := make([]string, 0, len(observer.events))
	for _, event := range observer.events {
		events = append(events, event.Type+":"+event.Status)
	}
	return evaluation.ScenarioObservation{
		Status: kernel.RunStatusCompleted, Revision: 1, EventTypes: events,
		Effects: []evaluation.EffectCount{
			{Key: "stream_event", Count: count},
			{Key: "terminal_task", Count: terminal},
		},
	}, nil
}

func (executor a2aScenarioExecutor) inputRequiredCancel(ctx context.Context) (evaluation.ScenarioObservation, error) {
	server, _ := newA2ATestServer(executor.t, true)
	observer := &testA2AObserver{name: "a2a-cancel-eval-observer"}
	client := newA2ATestClient(executor.t, server.Client(), observer)
	discovery, err := client.Discover(ctx, server.URL)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	interaction, err := client.SendMessage(ctx, discovery, SendRequest{
		MessageID: "a2a-eval-cancel", Text: "request input",
	})
	if err != nil || interaction.Task == nil || interaction.Task.State != string(a2asdk.TaskStateInputRequired) {
		return evaluation.ScenarioObservation{}, errors.New("A2A task did not enter input-required")
	}
	cancelled, err := client.CancelTask(ctx, discovery, interaction.Task.ID)
	if err != nil || cancelled.State != string(a2asdk.TaskStateCanceled) || !cancelled.Terminal {
		return evaluation.ScenarioObservation{}, errors.New("A2A task cancellation did not become terminal")
	}
	events := make([]string, 0, len(observer.events))
	for _, event := range observer.events {
		events = append(events, event.Type+":"+event.Status)
	}
	return evaluation.ScenarioObservation{
		Status: kernel.RunStatusCancelled, Revision: 1, EventTypes: events,
		Effects: []evaluation.EffectCount{
			{Key: "cancel_call", Count: 1},
			{Key: "input_required", Count: 1},
		},
	}, nil
}

func (executor a2aScenarioExecutor) shadowResumeUnknown(ctx context.Context) (evaluation.ScenarioObservation, error) {
	runtime := newShadowRuntime(executor.t)
	remote := newUncertainResumeClient()
	runner := newIdentityTestShadow(executor.t, runtime, remote)
	started, err := runner.StartRun(ctx, shadowStartRequest("a2a-eval-shadow-uncertain"))
	if err != nil || started.Run.Status != kernel.RunStatusWaitingInput {
		return evaluation.ScenarioObservation{}, errors.New("A2A shadow did not enter waiting input")
	}
	remote.resumeErr = errors.New("response lost after possible remote input acceptance")
	pending, err := runner.ResumeRun(ctx, started.Run.ID, started.Run.Revision, "approved")
	if !errors.Is(err, ErrRemoteOutcomeUnknown) || pending.Run.Status != kernel.RunStatusRunning {
		return evaluation.ScenarioObservation{}, errors.New("A2A shadow did not persist the uncertain input intent")
	}
	if _, err := runner.ResumeRun(ctx, started.Run.ID, pending.Run.Revision, "approved"); !errors.Is(err, ErrRemoteOutcomeUnknown) {
		return evaluation.ScenarioObservation{}, errors.New("A2A unknown resume permitted a second physical send")
	}
	if _, err := runner.LoadRun(ctx, started.Run.ID); !errors.Is(err, ErrRemoteOutcomeUnknown) {
		return evaluation.ScenarioObservation{}, errors.New("unchanged remote input-required status incorrectly settled unknown outcome")
	}
	remote.setTask(completedTask())
	completed, err := newIdentityTestShadow(executor.t, runtime, remote).LoadRun(ctx, started.Run.ID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		return evaluation.ScenarioObservation{}, errors.New("read-only remote terminal reconciliation failed")
	}
	_, resumes, _, _ := remote.counts()
	if resumes != 1 {
		return evaluation.ScenarioObservation{}, errors.New("A2A uncertain input was dispatched more than once")
	}
	events, err := runtime.ListEvents(ctx, completed.Run.ID, 0, 100)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	eventTypes := make([]string, len(events))
	for index, event := range events {
		eventTypes[index] = event.Type
	}
	return evaluation.ScenarioObservation{
		Status: completed.Run.Status, Revision: completed.Run.Revision, EventTypes: eventTypes,
		Effects: []evaluation.EffectCount{
			{Key: "resume_send_attempts", Count: resumes},
			{Key: "duplicate_resume_sends", Count: 0},
			{Key: "read_only_reconciliation", Count: 1},
		},
	}, nil
}

func loadA2AScenarioSuite(t *testing.T, path string) evaluation.ScenarioSuiteDraft {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var draft evaluation.ScenarioSuiteDraft
	if err = json.Unmarshal(encoded, &draft); err != nil {
		t.Fatal(err)
	}
	return draft
}

func loadA2ABaseline(t *testing.T, path string) evaluation.Baseline {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var baseline evaluation.Baseline
	if err = json.Unmarshal(encoded, &baseline); err != nil {
		t.Fatal(err)
	}
	return baseline
}
