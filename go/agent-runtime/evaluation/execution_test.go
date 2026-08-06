package evaluation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/evaluation"
)

var (
	errEvaluatorFailed   = errors.New("evaluator failed")
	errMissingTargetData = errors.New("missing target result")
)

func TestRunnerProducesStableRecordsAndReportAcrossConcurrency(t *testing.T) {
	t.Parallel()
	dataset := mustDataset(t)
	runner := mustRunner(t, staticTarget{}, []evaluation.EvaluatorRegistration{
		{Name: "z-style", Policy: evaluation.EvaluatorOptional, Evaluator: labelEvaluator{label: "style"}},
		{Name: "a-quality", Policy: evaluation.EvaluatorRequired, Evaluator: fixedEvaluator{}},
	})
	serial, err := runner.Execute(context.Background(), evaluation.ExecuteRequest{
		RequestID: "request_1", TargetName: targetCandidate, Dataset: dataset, MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("execute serial evaluation: %v", err)
	}
	parallel, err := runner.Execute(context.Background(), evaluation.ExecuteRequest{
		RequestID: "request_1", TargetName: targetCandidate, Dataset: dataset, MaxConcurrency: 8,
	})
	if err != nil {
		t.Fatalf("execute parallel evaluation: %v", err)
	}
	assertStableEvaluationIdentity(t, serial, parallel)
	assertEvaluationOrder(t, serial)
	assertAggregateReport(t, serial.Report)
}

func TestRunnerRecordsOptionalFailureAndTargetSkip(t *testing.T) {
	t.Parallel()
	dataset := mustDataset(t)
	target := scriptedTarget{results: map[string]evaluation.TargetResult{
		"a": {Status: evaluation.TargetCompleted, Output: json.RawMessage(`{"answer":"a"}`)},
		"b": {Status: evaluation.TargetSkipped, Reason: "not supported"},
	}}
	runner := mustRunner(t, target, []evaluation.EvaluatorRegistration{
		{Name: "required", Policy: evaluation.EvaluatorRequired, Evaluator: fixedEvaluator{}},
		{Name: "optional", Policy: evaluation.EvaluatorOptional, Evaluator: failingEvaluator{}},
	})
	record, err := runner.Execute(context.Background(), evaluation.ExecuteRequest{
		RequestID: "request_skip", TargetName: targetCandidate, Dataset: dataset,
	})
	if err != nil {
		t.Fatalf("execute evaluation: %v", err)
	}
	if record.Cases[0].Status != evaluation.CaseCompleted || record.Cases[1].Status != evaluation.CaseSkipped {
		t.Fatalf("unexpected case statuses: %#v", record.Cases)
	}
	if record.Cases[0].Findings[0].Status != evaluation.FindingFailed ||
		record.Cases[0].Findings[0].Policy != evaluation.EvaluatorOptional {
		t.Fatalf("optional evaluator failure not recorded: %#v", record.Cases[0].Findings)
	}
	if record.Report.CompletedCount != 1 || record.Report.SkippedCount != 1 || record.Report.FailedCount != 0 {
		t.Fatalf("unexpected skip report: %#v", record.Report)
	}
}

func TestRunnerRequiredEvaluatorFailureFailsCaseButContinuesBatch(t *testing.T) {
	t.Parallel()
	dataset := mustDataset(t)
	runner := mustRunner(t, staticTarget{}, []evaluation.EvaluatorRegistration{
		{Name: "required", Policy: evaluation.EvaluatorRequired, Evaluator: selectiveEvaluator{failCaseID: "a"}},
	})
	record, err := runner.Execute(context.Background(), evaluation.ExecuteRequest{
		RequestID: "request_fail", TargetName: targetCandidate, Dataset: dataset,
	})
	if err != nil {
		t.Fatalf("execute evaluation: %v", err)
	}
	if record.Cases[0].Status != evaluation.CaseFailed || record.Cases[1].Status != evaluation.CaseCompleted {
		t.Fatalf("batch did not continue after required evaluator failure: %#v", record.Cases)
	}
	if record.Report.FailedCount != 1 || record.Report.CompletedCount != 1 || record.Report.Passed {
		t.Fatalf("unexpected failed report: %#v", record.Report)
	}
}

func TestRunnerRejectsInvalidScoresPerCase(t *testing.T) {
	t.Parallel()
	dataset := mustDataset(t)
	runner := mustRunner(t, staticTarget{}, []evaluation.EvaluatorRegistration{
		{Name: "invalid", Policy: evaluation.EvaluatorRequired, Evaluator: invalidScoreEvaluator{}},
	})
	record, err := runner.Execute(context.Background(), evaluation.ExecuteRequest{
		RequestID: "request_invalid_score", TargetName: targetCandidate, Dataset: dataset,
	})
	if err != nil {
		t.Fatalf("execute evaluation: %v", err)
	}
	for _, item := range record.Cases {
		if item.Status != evaluation.CaseFailed || item.ErrorCode != "evaluator_score_invalid" {
			t.Fatalf("invalid score did not fail case: %#v", item)
		}
	}
}

func TestRunnerIsSafeForConcurrentReuse(t *testing.T) {
	t.Parallel()
	dataset := mustDataset(t)
	runner := mustRunner(t, staticTarget{}, []evaluation.EvaluatorRegistration{
		{Name: "fixed", Evaluator: fixedEvaluator{}},
	})
	const workers = 12
	results := make(chan evaluation.RunRecord, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			record, err := runner.Execute(context.Background(), evaluation.ExecuteRequest{
				RequestID: "request_concurrent", TargetName: targetCandidate, Dataset: dataset, MaxConcurrency: 2,
			})
			results <- record
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent evaluation failed: %v", err)
		}
	}
	var runID string
	var reportHash string
	for record := range results {
		if runID == "" {
			runID, reportHash = record.ID, record.Report.Hash
			continue
		}
		if record.ID != runID || record.Report.Hash != reportHash {
			t.Fatalf("concurrent evaluation identities changed: %#v", record)
		}
	}
}

func mustDataset(t *testing.T) evaluation.Dataset {
	t.Helper()
	dataset, err := evaluation.CompileDataset(evaluation.DatasetDraft{
		ID: "dataset", Revision: 1, Name: "Dataset", PassThreshold: 0.6,
		Dimensions: []evaluation.Dimension{
			{Name: dimensionAccuracy, Weight: 1},
			{Name: dimensionHelpfulness, Weight: 2},
		},
		Cases: []evaluation.Case{
			{ID: "b", Name: "B", Input: json.RawMessage(`{"id":"b"}`), Expected: json.RawMessage(`{"answer":"b"}`)},
			{ID: "a", Name: "A", Input: json.RawMessage(`{"id":"a"}`), Expected: json.RawMessage(`{"answer":"a"}`)},
		},
	})
	if err != nil {
		t.Fatalf("compile dataset: %v", err)
	}
	return dataset
}

func mustRunner(
	t *testing.T,
	target evaluation.Target,
	registrations []evaluation.EvaluatorRegistration,
) *evaluation.Runner {
	t.Helper()
	runner, err := evaluation.NewRunner(target, registrations)
	if err != nil {
		t.Fatalf("create evaluation runner: %v", err)
	}
	return runner
}

func assertAggregateReport(t *testing.T, report evaluation.Report) {
	t.Helper()
	assertAggregateCounts(t, report)
	assertAggregateDimensions(t, report)
	if report.Hash == "" || report.ID == "" {
		t.Fatalf("missing report identity: %#v", report)
	}
}

func assertStableEvaluationIdentity(t *testing.T, first evaluation.RunRecord, second evaluation.RunRecord) {
	t.Helper()
	if first.ID != second.ID || first.Report.ID != second.Report.ID || first.Report.Hash != second.Report.Hash {
		t.Fatalf("execution identity changed with concurrency:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func assertEvaluationOrder(t *testing.T, record evaluation.RunRecord) {
	t.Helper()
	if len(record.Cases) != 2 || record.Cases[0].CaseID != "a" || record.Cases[1].CaseID != "b" {
		t.Fatalf("case order is not deterministic: %#v", record.Cases)
	}
	for _, item := range record.Cases {
		if len(item.Findings) != 2 || item.Findings[0].Evaluator != "a-quality" ||
			item.Findings[1].Evaluator != "z-style" {
			t.Fatalf("evaluator order is not deterministic: %#v", item.Findings)
		}
	}
}

func assertAggregateCounts(t *testing.T, report evaluation.Report) {
	t.Helper()
	if report.CaseCount != 2 || report.CompletedCount != 2 || report.PassedCount != 2 || !report.Passed {
		t.Fatalf("unexpected report counts: %#v", report)
	}
}

func assertAggregateDimensions(t *testing.T, report evaluation.Report) {
	t.Helper()
	if len(report.Dimensions) != 2 || report.Dimensions[0].Name != dimensionAccuracy ||
		report.Dimensions[0].Score != 0.75 || report.Dimensions[1].Name != dimensionHelpfulness ||
		report.Dimensions[1].Score != 0.875 {
		t.Fatalf("unexpected dimension aggregation: %#v", report.Dimensions)
	}
}

type staticTarget struct{}

func (staticTarget) Run(_ context.Context, item evaluation.Case) (evaluation.TargetResult, error) {
	return evaluation.TargetResult{
		Status: evaluation.TargetCompleted,
		Output: json.RawMessage(fmt.Sprintf(`{"caseID":%q}`, item.ID)),
	}, nil
}

type scriptedTarget struct {
	results map[string]evaluation.TargetResult
}

func (target scriptedTarget) Run(_ context.Context, item evaluation.Case) (evaluation.TargetResult, error) {
	result, ok := target.results[item.ID]
	if !ok {
		return evaluation.TargetResult{}, errMissingTargetData
	}
	return result, nil
}

type fixedEvaluator struct{}

func (fixedEvaluator) Evaluate(_ context.Context, request evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	if request.Case.ID == "a" {
		return evaluation.EvaluationResult{
			Status: evaluation.FindingScored,
			Scores: []evaluation.Score{
				{Dimension: dimensionAccuracy, Value: 1, Weight: 1},
				{Dimension: dimensionHelpfulness, Value: 0.5, Weight: 1},
			},
		}, nil
	}
	return evaluation.EvaluationResult{
		Status: evaluation.FindingScored,
		Scores: []evaluation.Score{
			{Dimension: dimensionAccuracy, Value: 0.5, Weight: 1},
			{Dimension: dimensionHelpfulness, Value: 1, Weight: 3},
		},
	}, nil
}

type labelEvaluator struct {
	label string
}

func (evaluator labelEvaluator) Evaluate(_ context.Context, _ evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	return evaluation.EvaluationResult{
		Status: evaluation.FindingSkipped, Reason: "classification only", Labels: []string{evaluator.label},
	}, nil
}

type failingEvaluator struct{}

func (failingEvaluator) Evaluate(context.Context, evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	return evaluation.EvaluationResult{}, errEvaluatorFailed
}

type selectiveEvaluator struct {
	failCaseID string
}

func (evaluator selectiveEvaluator) Evaluate(_ context.Context, request evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	if request.Case.ID == evaluator.failCaseID {
		return evaluation.EvaluationResult{}, errEvaluatorFailed
	}
	return fixedEvaluator{}.Evaluate(context.Background(), request)
}

type invalidScoreEvaluator struct{}

func (invalidScoreEvaluator) Evaluate(context.Context, evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	return evaluation.EvaluationResult{
		Status: evaluation.FindingScored,
		Scores: []evaluation.Score{{Dimension: "unknown", Value: 2}},
	}, nil
}
