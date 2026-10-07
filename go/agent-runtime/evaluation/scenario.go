package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const DimensionRuntimeCorrectness = "runtime_correctness"

var ErrInvalidScenario = errors.New("invalid runtime evaluation scenario")

// EffectCount is one externally visible logical effect and its exact expected count.
type EffectCount struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// ScenarioExpectation is the deterministic runtime contract for one scenario.
// EventTypes preserves journal order; Effects is normalized by key.
type ScenarioExpectation struct {
	Status     kernel.RunStatus `json:"status"`
	Revision   uint64           `json:"revision,omitempty"`
	EventTypes []string         `json:"eventTypes,omitempty"`
	Effects    []EffectCount    `json:"effects,omitempty"`
	ErrorCode  string           `json:"errorCode,omitempty"`
	StateHash  string           `json:"stateHash,omitempty"`
}

// ScenarioObservation is the content-free runtime evidence returned by a scenario executor.
type ScenarioObservation struct {
	Status     kernel.RunStatus `json:"status"`
	Revision   uint64           `json:"revision,omitempty"`
	EventTypes []string         `json:"eventTypes,omitempty"`
	Effects    []EffectCount    `json:"effects,omitempty"`
	ErrorCode  string           `json:"errorCode,omitempty"`
	StateHash  string           `json:"stateHash,omitempty"`
}

// Scenario is one typed runtime case compiled into the existing immutable Dataset contract.
type Scenario struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Input    json.RawMessage     `json:"input"`
	Expected ScenarioExpectation `json:"expected"`
	Metadata map[string]string   `json:"metadata,omitempty"`
}

// ScenarioSuiteDraft is a typed runtime-native corpus before Dataset compilation.
type ScenarioSuiteDraft struct {
	ID            string     `json:"id"`
	Revision      int        `json:"revision"`
	Name          string     `json:"name"`
	PassThreshold float64    `json:"passThreshold,omitempty"`
	Scenarios     []Scenario `json:"scenarios"`
}

// ScenarioRequest is the immutable input supplied to one deterministic executor.
type ScenarioRequest struct {
	ID       string
	Input    json.RawMessage
	Metadata map[string]string
}

// ScenarioExecutor executes runtime behavior and returns structural evidence only.
type ScenarioExecutor interface {
	Execute(context.Context, ScenarioRequest) (ScenarioObservation, error)
}

type scenarioTarget struct {
	executor ScenarioExecutor
}

type scenarioEvaluator struct{}

// CompileScenarioDataset reuses Dataset hashing/reporting while giving runtime
// scenarios a typed correctness contract.
func CompileScenarioDataset(draft ScenarioSuiteDraft) (Dataset, error) {
	draft.ID = strings.TrimSpace(draft.ID)
	draft.Name = strings.TrimSpace(draft.Name)
	if draft.PassThreshold == 0 {
		draft.PassThreshold = 1
	}
	if draft.ID == "" || draft.Revision <= 0 || draft.Name == "" || len(draft.Scenarios) == 0 {
		return Dataset{}, ErrInvalidScenario
	}
	cases := make([]Case, 0, len(draft.Scenarios))
	seen := make(map[string]struct{}, len(draft.Scenarios))
	for _, scenario := range draft.Scenarios {
		normalized, err := normalizeScenario(scenario)
		if err != nil {
			return Dataset{}, err
		}
		if _, duplicate := seen[normalized.ID]; duplicate {
			return Dataset{}, ErrInvalidScenario
		}
		seen[normalized.ID] = struct{}{}
		expected, err := json.Marshal(normalized.Expected)
		if err != nil {
			return Dataset{}, errors.Join(ErrInvalidScenario, err)
		}
		cases = append(cases, Case{
			ID: normalized.ID, Name: normalized.Name, Input: normalized.Input,
			Expected: expected, Metadata: normalized.Metadata,
		})
	}
	dataset, err := CompileDataset(DatasetDraft{
		ID: draft.ID, Revision: draft.Revision, Name: draft.Name,
		PassThreshold: draft.PassThreshold,
		Dimensions:    []Dimension{{Name: DimensionRuntimeCorrectness, Weight: 1}},
		Cases:         cases,
	})
	if err != nil {
		return Dataset{}, errors.Join(ErrInvalidScenario, err)
	}
	return dataset, nil
}

// NewScenarioRunner creates a deterministic runtime evaluator over the existing Runner.
func NewScenarioRunner(executor ScenarioExecutor) (*Runner, error) {
	if executor == nil {
		return nil, ErrInvalidScenario
	}
	return NewRunner(scenarioTarget{executor: executor}, []EvaluatorRegistration{{
		Name: "runtime-contract", Policy: EvaluatorRequired, Evaluator: scenarioEvaluator{},
	}})
}

func (target scenarioTarget) Run(ctx context.Context, item Case) (TargetResult, error) {
	observation, err := target.executor.Execute(ctx, ScenarioRequest{
		ID: item.ID, Input: cloneJSON(item.Input), Metadata: cloneMetadata(item.Metadata),
	})
	if err != nil {
		return TargetResult{}, err
	}
	normalized, err := normalizeScenarioObservation(observation)
	if err != nil {
		return TargetResult{}, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return TargetResult{}, errors.Join(ErrInvalidScenario, err)
	}
	return TargetResult{Status: TargetCompleted, Output: encoded}, nil
}

func (scenarioEvaluator) Evaluate(_ context.Context, request EvaluationRequest) (EvaluationResult, error) {
	var expected ScenarioExpectation
	if err := json.Unmarshal(request.Case.Expected, &expected); err != nil {
		return EvaluationResult{}, errors.Join(ErrInvalidScenario, err)
	}
	expected, err := normalizeScenarioExpectation(expected)
	if err != nil {
		return EvaluationResult{}, err
	}
	var observed ScenarioObservation
	if err = json.Unmarshal(request.Output, &observed); err != nil {
		return EvaluationResult{}, errors.Join(ErrInvalidScenario, err)
	}
	observed, err = normalizeScenarioObservation(observed)
	if err != nil {
		return EvaluationResult{}, err
	}
	if scenarioMatches(expected, observed) {
		return EvaluationResult{
			Status: FindingScored,
			Scores: []Score{{Dimension: DimensionRuntimeCorrectness, Value: 1}},
			Labels: []string{"runtime_contract_passed"},
		}, nil
	}
	return EvaluationResult{
		Status: FindingScored,
		Scores: []Score{{
			Dimension: DimensionRuntimeCorrectness, Value: 0,
			Evidence: scenarioMismatchEvidence(expected, observed),
		}},
		Labels: []string{"runtime_regression"},
	}, nil
}

func normalizeScenario(value Scenario) (Scenario, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.Name = strings.TrimSpace(value.Name)
	value.Input = normalizeRawJSON(value.Input, json.RawMessage(`null`))
	value.Metadata = normalizeMetadata(value.Metadata)
	expected, err := normalizeScenarioExpectation(value.Expected)
	if value.ID == "" || value.Name == "" || !json.Valid(value.Input) || err != nil {
		return Scenario{}, ErrInvalidScenario
	}
	value.Expected = expected
	return value, nil
}

func normalizeScenarioExpectation(value ScenarioExpectation) (ScenarioExpectation, error) {
	value.EventTypes = normalizeEventTypes(value.EventTypes)
	value.ErrorCode = strings.TrimSpace(value.ErrorCode)
	value.StateHash = strings.TrimSpace(value.StateHash)
	effects, err := normalizeEffectCounts(value.Effects)
	if err != nil || !validScenarioStatus(value.Status) {
		return ScenarioExpectation{}, ErrInvalidScenario
	}
	value.Effects = effects
	return value, nil
}

func normalizeScenarioObservation(value ScenarioObservation) (ScenarioObservation, error) {
	value.EventTypes = normalizeEventTypes(value.EventTypes)
	value.ErrorCode = strings.TrimSpace(value.ErrorCode)
	value.StateHash = strings.TrimSpace(value.StateHash)
	effects, err := normalizeEffectCounts(value.Effects)
	if err != nil || !validScenarioStatus(value.Status) {
		return ScenarioObservation{}, ErrInvalidScenario
	}
	value.Effects = effects
	return value, nil
}

func normalizeEventTypes(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizeEffectCounts(values []EffectCount) ([]EffectCount, error) {
	result := append([]EffectCount(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for index := range result {
		result[index].Key = strings.TrimSpace(result[index].Key)
		if result[index].Key == "" || result[index].Count < 0 {
			return nil, ErrInvalidScenario
		}
		if _, duplicate := seen[result[index].Key]; duplicate {
			return nil, ErrInvalidScenario
		}
		seen[result[index].Key] = struct{}{}
	}
	sort.Slice(result, func(left int, right int) bool { return result[left].Key < result[right].Key })
	return result, nil
}

func validScenarioStatus(status kernel.RunStatus) bool {
	return status == kernel.RunStatusRunning || status == kernel.RunStatusWaitingInput ||
		status == kernel.RunStatusCompleted || status == kernel.RunStatusFailed ||
		status == kernel.RunStatusCancelled
}

func scenarioMatches(expected ScenarioExpectation, observed ScenarioObservation) bool {
	if expected.Status != observed.Status || expected.ErrorCode != observed.ErrorCode ||
		expected.StateHash != observed.StateHash {
		return false
	}
	if expected.Revision != 0 && expected.Revision != observed.Revision {
		return false
	}
	if len(expected.EventTypes) != len(observed.EventTypes) || len(expected.Effects) != len(observed.Effects) {
		return false
	}
	for index := range expected.EventTypes {
		if expected.EventTypes[index] != observed.EventTypes[index] {
			return false
		}
	}
	for index := range expected.Effects {
		if expected.Effects[index] != observed.Effects[index] {
			return false
		}
	}
	return true
}

func scenarioMismatchEvidence(expected ScenarioExpectation, observed ScenarioObservation) string {
	return fmt.Sprintf(
		"expected status=%s revision=%d events=%v effects=%v error=%q stateHash=%q; observed status=%s revision=%d events=%v effects=%v error=%q stateHash=%q",
		expected.Status, expected.Revision, expected.EventTypes, expected.Effects, expected.ErrorCode, expected.StateHash,
		observed.Status, observed.Revision, observed.EventTypes, observed.Effects, observed.ErrorCode, observed.StateHash,
	)
}
