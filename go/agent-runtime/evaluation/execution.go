package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const CapabilityRunner kernel.Capability = "evaluation.runner"

var ErrInvalidExecution = errors.New("invalid evaluation execution")

// TargetStatus is one candidate execution outcome.
type TargetStatus string

const (
	TargetCompleted TargetStatus = "completed"
	TargetSkipped   TargetStatus = "skipped"
)

// EvaluatorPolicy controls whether evaluator failure fails one Case Record.
type EvaluatorPolicy string

const (
	EvaluatorRequired EvaluatorPolicy = "required"
	EvaluatorOptional EvaluatorPolicy = "optional"
)

// FindingStatus is one evaluator outcome.
type FindingStatus string

const (
	FindingScored  FindingStatus = "scored"
	FindingSkipped FindingStatus = "skipped"
	FindingFailed  FindingStatus = "failed"
)

// CaseStatus is the durable semantic status of one evaluated case.
type CaseStatus string

const (
	CaseCompleted CaseStatus = "completed"
	CaseSkipped   CaseStatus = "skipped"
	CaseFailed    CaseStatus = "failed"
)

// RunStatus is the terminal status of one evaluation batch.
type RunStatus string

const RunCompleted RunStatus = "completed"

// TargetResult is the candidate output presented to evaluators.
type TargetResult struct {
	Status   TargetStatus      `json:"status"`
	Output   json.RawMessage   `json:"output"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Reason   string            `json:"reason,omitempty"`
}

// Target executes one exact Dataset Case.
type Target interface {
	Run(context.Context, Case) (TargetResult, error)
}

// Score is one bounded dimension observation.
type Score struct {
	Dimension string  `json:"dimension"`
	Value     float64 `json:"value"`
	Weight    float64 `json:"weight"`
	Evidence  string  `json:"evidence,omitempty"`
}

// EvaluationRequest is the immutable input to one Evaluator.
type EvaluationRequest struct {
	RunID      string
	RecordID   string
	Dataset    Dataset
	Case       Case
	TargetName string
	Output     json.RawMessage
	Metadata   map[string]string
}

// EvaluationResult is one bounded evaluator response.
type EvaluationResult struct {
	Status   FindingStatus     `json:"status"`
	Scores   []Score           `json:"scores,omitempty"`
	Labels   []string          `json:"labels,omitempty"`
	Evidence string            `json:"evidence,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Reason   string            `json:"reason,omitempty"`
}

// Evaluator scores one candidate output without retaining request content.
type Evaluator interface {
	Evaluate(context.Context, EvaluationRequest) (EvaluationResult, error)
}

// EvaluatorRegistration freezes evaluator name, policy and implementation.
type EvaluatorRegistration struct {
	Name      string
	Policy    EvaluatorPolicy
	Evaluator Evaluator
}

// Finding is one normalized evaluator record.
type Finding struct {
	Evaluator string            `json:"evaluator"`
	Policy    EvaluatorPolicy   `json:"policy"`
	Status    FindingStatus     `json:"status"`
	Scores    []Score           `json:"scores,omitempty"`
	Labels    []string          `json:"labels,omitempty"`
	Evidence  string            `json:"evidence,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	ErrorCode string            `json:"errorCode,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// CaseRecord is one stable evaluation case execution.
type CaseRecord struct {
	ID         string            `json:"id"`
	CaseID     string            `json:"caseID"`
	CaseName   string            `json:"caseName"`
	Status     CaseStatus        `json:"status"`
	Output     json.RawMessage   `json:"output,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Findings   []Finding         `json:"findings,omitempty"`
	Scores     []Score           `json:"scores,omitempty"`
	ErrorCode  string            `json:"errorCode,omitempty"`
	Error      string            `json:"error,omitempty"`
	SkipReason string            `json:"skipReason,omitempty"`
}

// RunRecord is one complete deterministic evaluation batch.
type RunRecord struct {
	ID          string       `json:"id"`
	RequestID   string       `json:"requestID"`
	TargetName  string       `json:"targetName"`
	DatasetID   string       `json:"datasetID"`
	DatasetHash string       `json:"datasetHash"`
	Status      RunStatus    `json:"status"`
	Cases       []CaseRecord `json:"cases"`
	Report      Report       `json:"report"`
}

// ExecuteRequest starts one deterministic evaluation batch.
type ExecuteRequest struct {
	RequestID      string
	TargetName     string
	Dataset        Dataset
	MaxConcurrency int
}

type registeredEvaluator struct {
	name      string
	policy    EvaluatorPolicy
	evaluator Evaluator
}

// Runner owns one explicit Target and one immutable Evaluator set.
type Runner struct {
	target     Target
	evaluators []registeredEvaluator
}

// NewRunner freezes evaluator order by name.
func NewRunner(target Target, registrations []EvaluatorRegistration) (*Runner, error) {
	if target == nil || len(registrations) == 0 {
		return nil, ErrInvalidExecution
	}
	evaluators, err := normalizeEvaluatorRegistrations(registrations)
	if err != nil {
		return nil, err
	}
	return &Runner{target: target, evaluators: evaluators}, nil
}

// Descriptor declares Evaluation as a pure capability with no Runtime Kind.
func (runner *Runner) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "evaluation", Provides: []kernel.Capability{CapabilityRunner}}
}

// Execute evaluates all Dataset cases with stable identities and deterministic ordering.
func (runner *Runner) Execute(ctx context.Context, request ExecuteRequest) (RunRecord, error) {
	request = normalizeExecuteRequest(request)
	if runner == nil || runner.target == nil || len(runner.evaluators) == 0 || !validExecuteRequest(request) {
		return RunRecord{}, ErrInvalidExecution
	}
	runID := stableEvaluationID("evalrun", request.Dataset.Hash, request.TargetName, request.RequestID)
	records := make([]CaseRecord, len(request.Dataset.Cases))
	if err := runner.executeCases(ctx, runID, request, records); err != nil {
		return RunRecord{}, err
	}
	report, err := buildReport(runID, request.Dataset, records)
	if err != nil {
		return RunRecord{}, err
	}
	return RunRecord{
		ID: runID, RequestID: request.RequestID, TargetName: request.TargetName,
		DatasetID: request.Dataset.ID, DatasetHash: request.Dataset.Hash,
		Status: RunCompleted, Cases: cloneCaseRecords(records), Report: report,
	}, nil
}

func (runner *Runner) executeCases(
	ctx context.Context,
	runID string,
	request ExecuteRequest,
	records []CaseRecord,
) error {
	semaphore := make(chan struct{}, request.MaxConcurrency)
	var group sync.WaitGroup
	var cancelOnce sync.Once
	var executionErr error
	for index, item := range request.Dataset.Cases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		group.Add(1)
		go func(caseIndex int, current Case) {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				cancelOnce.Do(func() { executionErr = ctx.Err() })
				return
			}
			records[caseIndex] = runner.executeCase(ctx, runID, request, current)
		}(index, item)
	}
	group.Wait()
	return executionErr
}

func (runner *Runner) executeCase(
	ctx context.Context,
	runID string,
	request ExecuteRequest,
	item Case,
) CaseRecord {
	record := CaseRecord{
		ID: stableEvaluationID("evalcase", runID, item.ID), CaseID: item.ID, CaseName: item.Name,
		Status: CaseCompleted,
	}
	targetResult, err := runner.target.Run(ctx, cloneCase(item))
	if err != nil {
		return failedCaseRecord(record, "target_error", err)
	}
	targetResult = normalizeTargetResult(targetResult)
	if !validTargetResult(targetResult) {
		return failedCaseRecord(record, "target_result_invalid", ErrInvalidExecution)
	}
	if targetResult.Status == TargetSkipped {
		record.Status = CaseSkipped
		record.SkipReason = targetResult.Reason
		return record
	}
	record.Output = cloneJSON(targetResult.Output)
	record.Metadata = cloneMetadata(targetResult.Metadata)
	for _, evaluator := range runner.evaluators {
		finding := runner.evaluateCase(ctx, runID, request, record, item, evaluator)
		record.Findings = append(record.Findings, finding)
		record.Scores = append(record.Scores, finding.Scores...)
		if evaluator.policy == EvaluatorRequired && finding.Status == FindingFailed {
			record.Status = CaseFailed
			record.ErrorCode = finding.ErrorCode
			record.Error = finding.Error
		}
	}
	return normalizeCaseRecord(record)
}

func (runner *Runner) evaluateCase(
	ctx context.Context,
	runID string,
	request ExecuteRequest,
	record CaseRecord,
	item Case,
	evaluator registeredEvaluator,
) Finding {
	result, err := evaluator.evaluator.Evaluate(ctx, EvaluationRequest{
		RunID: runID, RecordID: record.ID, Dataset: cloneDataset(request.Dataset), Case: cloneCase(item),
		TargetName: request.TargetName, Output: cloneJSON(record.Output), Metadata: cloneMetadata(record.Metadata),
	})
	return normalizeFinding(request.Dataset, evaluator, result, err)
}

func normalizeEvaluatorRegistrations(registrations []EvaluatorRegistration) ([]registeredEvaluator, error) {
	result := make([]registeredEvaluator, 0, len(registrations))
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		name := strings.TrimSpace(registration.Name)
		policy := registration.Policy
		if policy == "" {
			policy = EvaluatorRequired
		}
		if name == "" || registration.Evaluator == nil ||
			(policy != EvaluatorRequired && policy != EvaluatorOptional) {
			return nil, ErrInvalidExecution
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, ErrInvalidExecution
		}
		seen[name] = struct{}{}
		result = append(result, registeredEvaluator{name: name, policy: policy, evaluator: registration.Evaluator})
	}
	sort.Slice(result, func(left int, right int) bool { return result[left].name < result[right].name })
	return result, nil
}

func normalizeExecuteRequest(request ExecuteRequest) ExecuteRequest {
	request.RequestID = strings.TrimSpace(request.RequestID)
	request.TargetName = strings.TrimSpace(request.TargetName)
	request.Dataset = cloneDataset(request.Dataset)
	if request.MaxConcurrency <= 0 {
		request.MaxConcurrency = 4
	}
	if request.MaxConcurrency > 32 {
		request.MaxConcurrency = 32
	}
	return request
}

func validExecuteRequest(request ExecuteRequest) bool {
	return request.RequestID != "" && request.TargetName != "" && request.MaxConcurrency > 0 &&
		ValidateDataset(request.Dataset) == nil
}

func normalizeTargetResult(result TargetResult) TargetResult {
	if result.Status == "" {
		result.Status = TargetCompleted
	}
	result.Output = normalizeRawJSON(result.Output, json.RawMessage(`null`))
	result.Metadata = normalizeMetadata(result.Metadata)
	result.Reason = strings.TrimSpace(result.Reason)
	return result
}

func validTargetResult(result TargetResult) bool {
	if result.Status != TargetCompleted && result.Status != TargetSkipped {
		return false
	}
	if result.Status == TargetSkipped {
		return result.Reason != ""
	}
	return json.Valid(result.Output)
}

func normalizeFinding(dataset Dataset, evaluator registeredEvaluator, result EvaluationResult, evaluationErr error) Finding {
	finding := Finding{
		Evaluator: evaluator.name, Policy: evaluator.policy,
		Status: result.Status, Labels: normalizeLabels(result.Labels),
		Evidence: truncateText(result.Evidence, 1_024), Metadata: normalizeMetadata(result.Metadata),
		Reason: truncateText(result.Reason, 512),
	}
	if finding.Status == "" {
		finding.Status = FindingScored
	}
	if evaluationErr != nil {
		finding.Status = FindingFailed
		finding.ErrorCode = "evaluator_error"
		finding.Error = truncateText(evaluationErr.Error(), 512)
		return finding
	}
	if finding.Status == FindingSkipped {
		if finding.Reason == "" {
			finding.Status = FindingFailed
			finding.ErrorCode = "evaluator_result_invalid"
			finding.Error = ErrInvalidExecution.Error()
		}
		return finding
	}
	if finding.Status != FindingScored {
		finding.Status = FindingFailed
		finding.ErrorCode = "evaluator_result_invalid"
		finding.Error = ErrInvalidExecution.Error()
		return finding
	}
	scores, err := normalizeScores(dataset, result.Scores)
	if err != nil {
		finding.Status = FindingFailed
		finding.ErrorCode = "evaluator_score_invalid"
		finding.Error = err.Error()
		return finding
	}
	finding.Scores = scores
	return finding
}

func normalizeScores(dataset Dataset, values []Score) ([]Score, error) {
	if len(values) == 0 {
		return nil, ErrInvalidExecution
	}
	allowed := allowedDimensions(dataset)
	result := append([]Score(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for index := range result {
		normalized, err := normalizeScore(result[index], allowed)
		if err != nil {
			return nil, ErrInvalidExecution
		}
		if _, duplicate := seen[normalized.Dimension]; duplicate {
			return nil, ErrInvalidExecution
		}
		seen[normalized.Dimension] = struct{}{}
		result[index] = normalized
	}
	sort.Slice(result, func(left int, right int) bool { return result[left].Dimension < result[right].Dimension })
	return result, nil
}

func allowedDimensions(dataset Dataset) map[string]struct{} {
	result := make(map[string]struct{}, len(dataset.Dimensions))
	for _, dimension := range dataset.Dimensions {
		result[dimension.Name] = struct{}{}
	}
	return result
}

func normalizeScore(score Score, allowed map[string]struct{}) (Score, error) {
	score.Dimension = strings.TrimSpace(score.Dimension)
	score.Evidence = truncateText(score.Evidence, 1_024)
	if score.Weight == 0 {
		score.Weight = 1
	}
	_, dimensionAllowed := allowed[score.Dimension]
	validValue := score.Value >= 0 && score.Value <= 1 && !math.IsNaN(score.Value) && !math.IsInf(score.Value, 0)
	validWeight := score.Weight > 0 && !math.IsNaN(score.Weight) && !math.IsInf(score.Weight, 0)
	if !dimensionAllowed || !validValue || !validWeight {
		return Score{}, ErrInvalidExecution
	}
	return score, nil
}

func normalizeLabels(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func failedCaseRecord(record CaseRecord, code string, err error) CaseRecord {
	record.Status = CaseFailed
	record.ErrorCode = code
	record.Error = truncateText(err.Error(), 512)
	return record
}

func normalizeCaseRecord(record CaseRecord) CaseRecord {
	record.Output = cloneJSON(record.Output)
	record.Metadata = cloneMetadata(record.Metadata)
	record.Findings = cloneFindings(record.Findings)
	record.Scores = append([]Score(nil), record.Scores...)
	sort.Slice(record.Scores, func(left int, right int) bool {
		if record.Scores[left].Dimension == record.Scores[right].Dimension {
			return record.Scores[left].Evidence < record.Scores[right].Evidence
		}
		return record.Scores[left].Dimension < record.Scores[right].Dimension
	})
	return record
}

func cloneCase(item Case) Case {
	item.Input = cloneJSON(item.Input)
	item.Expected = cloneJSON(item.Expected)
	item.Metadata = cloneMetadata(item.Metadata)
	return item
}

func cloneFindings(values []Finding) []Finding {
	result := append([]Finding(nil), values...)
	for index := range result {
		result[index].Scores = append([]Score(nil), result[index].Scores...)
		result[index].Labels = append([]string(nil), result[index].Labels...)
		result[index].Metadata = cloneMetadata(result[index].Metadata)
	}
	return result
}

func cloneCaseRecords(values []CaseRecord) []CaseRecord {
	result := append([]CaseRecord(nil), values...)
	for index := range result {
		result[index] = normalizeCaseRecord(result[index])
	}
	return result
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
