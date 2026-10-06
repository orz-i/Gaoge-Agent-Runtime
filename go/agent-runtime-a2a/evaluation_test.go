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
