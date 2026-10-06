package evaluation_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/evaluation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestRuntimeScenarioCorpus(t *testing.T) {
	draft := loadScenarioSuite(t, "testdata/runtime-smoke.json")
	dataset, err := evaluation.CompileScenarioDataset(draft)
	if err != nil {
		t.Fatalf("compile runtime scenario dataset: %v", err)
	}
	runner, err := evaluation.NewScenarioRunner(runtimeScenarioExecutor{})
	if err != nil {
		t.Fatalf("create runtime scenario runner: %v", err)
	}
	record, err := runner.Execute(t.Context(), evaluation.ExecuteRequest{
		RequestID: "runtime-smoke-v1", TargetName: "memory-runtime",
		Dataset: dataset, MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("execute runtime scenario corpus: %v", err)
	}
	baseline := loadBaseline(t, "testdata/runtime-smoke-baseline.json")
	comparison, err := evaluation.CompareBaseline(record, baseline)
	if err != nil {
		t.Fatalf("compare runtime baseline (dataset hash %s): %v", dataset.Hash, err)
	}
	if !comparison.Passed {
		t.Fatalf("runtime baseline regressed: %v", comparison.Regressions)
	}
	encoded, err := json.Marshal(record.Report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("runtime eval report: %s", encoded)
}

func TestScenarioEvaluatorReportsStructuralRegression(t *testing.T) {
	dataset, err := evaluation.CompileScenarioDataset(evaluation.ScenarioSuiteDraft{
		ID: "regression", Revision: 1, Name: "Regression",
		Scenarios: []evaluation.Scenario{{
			ID: "case", Name: "Case", Input: json.RawMessage(`{"operation":"mismatch"}`),
			Expected: evaluation.ScenarioExpectation{
				Status: kernel.RunStatusCompleted, Revision: 2,
				EventTypes: []string{"run.created", "scenario.completed"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := evaluation.NewScenarioRunner(staticScenarioExecutor{observation: evaluation.ScenarioObservation{
		Status: kernel.RunStatusFailed, Revision: 2, EventTypes: []string{"run.created", "scenario.failed"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	record, err := runner.Execute(t.Context(), evaluation.ExecuteRequest{
		RequestID: "regression", TargetName: "static", Dataset: dataset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Report.Passed || record.Report.OverallScore != 0 ||
		len(record.Cases) != 1 || len(record.Cases[0].Findings) != 1 ||
		record.Cases[0].Findings[0].Labels[0] != "runtime_regression" {
		t.Fatalf("regression was not scored deterministically: %#v", record)
	}
}

func TestCompareBaselineReportsThresholdRegressions(t *testing.T) {
	record := evaluation.RunRecord{
		DatasetID: "dataset", DatasetHash: "hash",
		Report: evaluation.Report{
			DatasetHash: "hash", OverallScore: 0.75, PassedCount: 2, FailedCount: 1, Passed: false,
		},
	}
	comparison, err := evaluation.CompareBaseline(record, evaluation.Baseline{
		DatasetID: "dataset", DatasetHash: "hash", MinimumOverallScore: 0.9,
		MinimumPassedCases: 3, MaximumFailedCases: 0, RequireReportPass: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Passed || len(comparison.Regressions) != 4 {
		t.Fatalf("unexpected baseline comparison: %#v", comparison)
	}
}

type staticScenarioExecutor struct {
	observation evaluation.ScenarioObservation
}

func (executor staticScenarioExecutor) Execute(
	context.Context,
	evaluation.ScenarioRequest,
) (evaluation.ScenarioObservation, error) {
	return executor.observation, nil
}

type runtimeScenarioExecutor struct{}

type runtimeScenarioInput struct {
	Operation string `json:"operation"`
}

func (runtimeScenarioExecutor) Execute(
	ctx context.Context,
	request evaluation.ScenarioRequest,
) (evaluation.ScenarioObservation, error) {
	var input runtimeScenarioInput
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	switch input.Operation {
	case "crash_recovery_terminal":
		return executeCrashRecoveryScenario(ctx)
	case "stale_cas_fenced":
		return executeStaleCASScenario(ctx)
	case "duplicate_delivery_logical_once":
		return executeDuplicateDeliveryScenario(ctx)
	default:
		return evaluation.ScenarioObservation{}, errors.New("unsupported runtime scenario")
	}
}

func executeCrashRecoveryScenario(ctx context.Context) (evaluation.ScenarioObservation, error) {
	store, runtime, err := newScenarioRuntime()
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	created, err := createScenarioRun(ctx, runtime, "eval-recovery")
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	if _, err = completeScenarioRun(ctx, runtime, created); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	recovered, err := kernel.New(kernel.Dependencies{Store: store, Clock: scenarioClock{}})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	snapshot, err := recovered.Load(ctx, created.Run.ID)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	return observeScenario(ctx, recovered, snapshot, []evaluation.EffectCount{{Key: "recovered_load", Count: 1}})
}

func executeStaleCASScenario(ctx context.Context) (evaluation.ScenarioObservation, error) {
	_, runtime, err := newScenarioRuntime()
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	created, err := createScenarioRun(ctx, runtime, "eval-cas")
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	current, err := runtime.Apply(ctx, created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"phase":"first"}`),
		Events: []kernel.EventDraft{{Type: "scenario.first"}},
	})
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	_, staleErr := runtime.Apply(ctx, created.Run.ID, created.Run.Revision, completedScenarioMutation())
	if !errors.Is(staleErr, kernel.ErrConflict) {
		return evaluation.ScenarioObservation{}, errors.New("stale revision was not fenced")
	}
	completed, err := runtime.Apply(ctx, current.Run.ID, current.Run.Revision, completedScenarioMutation())
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	return observeScenario(ctx, runtime, completed, []evaluation.EffectCount{{Key: "stale_cas_rejected", Count: 1}})
}

func executeDuplicateDeliveryScenario(ctx context.Context) (evaluation.ScenarioObservation, error) {
	store, runtime, err := newScenarioRuntime()
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	created, err := createScenarioRun(ctx, runtime, "eval-delivery")
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	completed, err := completeScenarioRun(ctx, runtime, created)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	now := scenarioNow()
	request := kernel.TransitionClaimRequest{
		WorkerID: "eval-worker", Limit: 1, LeaseDuration: time.Minute, Now: now,
	}
	first, err := store.ClaimTransitions(ctx, request)
	if err != nil || len(first) != 1 {
		return evaluation.ScenarioObservation{}, errors.New("first transition delivery missing")
	}
	duplicate, err := store.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
		WorkerID: "other-worker", Limit: 1, LeaseDuration: time.Minute, Now: now,
	})
	if err != nil || len(duplicate) != 0 {
		return evaluation.ScenarioObservation{}, errors.New("active transition lease was duplicated")
	}
	lease := kernel.TransitionLeaseRequest{
		TransitionID: first[0].Transition.ID, LeaseID: first[0].LeaseID, WorkerID: first[0].WorkerID,
	}
	if err = store.RetryTransition(ctx, kernel.TransitionRetryRequest{
		TransitionLeaseRequest: lease, AvailableAt: now,
	}); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	second, err := store.ClaimTransitions(ctx, request)
	if err != nil || len(second) != 1 {
		return evaluation.ScenarioObservation{}, errors.New("retried transition delivery missing")
	}
	if err = store.AckTransition(ctx, kernel.TransitionLeaseRequest{
		TransitionID: second[0].Transition.ID, LeaseID: second[0].LeaseID, WorkerID: second[0].WorkerID,
	}); err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	afterAck, err := store.ClaimTransitions(ctx, request)
	if err != nil || len(afterAck) != 0 {
		return evaluation.ScenarioObservation{}, errors.New("acknowledged transition was redelivered")
	}
	return observeScenario(ctx, runtime, completed, []evaluation.EffectCount{
		{Key: "physical_delivery", Count: 2},
		{Key: "logical_consumption", Count: 1},
	})
}

func newScenarioRuntime() (*memory.Store, *kernel.Runtime, error) {
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: scenarioClock{}})
	return store, runtime, err
}

func createScenarioRun(
	ctx context.Context,
	runtime *kernel.Runtime,
	id string,
) (kernel.Snapshot, error) {
	return runtime.Create(ctx, kernel.CreateRequest{
		ID: id, Kind: kernel.RunKind("evaluation"),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "evaluation", ID: "thread"},
		Goal:   "evaluate runtime correctness", State: json.RawMessage(`{"phase":"created"}`),
	})
}

func completeScenarioRun(
	ctx context.Context,
	runtime *kernel.Runtime,
	current kernel.Snapshot,
) (kernel.Snapshot, error) {
	return runtime.Apply(ctx, current.Run.ID, current.Run.Revision, completedScenarioMutation())
}

func completedScenarioMutation() kernel.Mutation {
	return kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"phase":"completed"}`),
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)},
		Events: []kernel.EventDraft{{Type: "scenario.completed"}},
	}
}

func observeScenario(
	ctx context.Context,
	runtime *kernel.Runtime,
	snapshot kernel.Snapshot,
	effects []evaluation.EffectCount,
) (evaluation.ScenarioObservation, error) {
	events, err := runtime.ListEvents(ctx, snapshot.Run.ID, 0, 100)
	if err != nil {
		return evaluation.ScenarioObservation{}, err
	}
	eventTypes := make([]string, len(events))
	for index, event := range events {
		eventTypes[index] = event.Type
	}
	return evaluation.ScenarioObservation{
		Status: snapshot.Run.Status, Revision: snapshot.Run.Revision,
		EventTypes: eventTypes, Effects: effects, ErrorCode: snapshot.Run.ErrorCode,
	}, nil
}

type scenarioClock struct{}

func (scenarioClock) Now() time.Time { return scenarioNow() }

func scenarioNow() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}

func loadScenarioSuite(t *testing.T, path string) evaluation.ScenarioSuiteDraft {
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

func loadBaseline(t *testing.T, path string) evaluation.Baseline {
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
