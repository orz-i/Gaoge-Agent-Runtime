package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// DimensionReport is one deterministic weighted dimension aggregate.
type DimensionReport struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
	Count  int     `json:"count"`
	Score  float64 `json:"score"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// CaseSummary is one compact deterministic case outcome.
type CaseSummary struct {
	RecordID string     `json:"recordID"`
	CaseID   string     `json:"caseID"`
	Status   CaseStatus `json:"status"`
	Score    float64    `json:"score"`
	Passed   bool       `json:"passed"`
}

// Report is one complete evaluation aggregate.
type Report struct {
	ID             string            `json:"id"`
	RunID          string            `json:"runID"`
	DatasetHash    string            `json:"datasetHash"`
	PassThreshold  float64           `json:"passThreshold"`
	OverallScore   float64           `json:"overallScore"`
	Passed         bool              `json:"passed"`
	CaseCount      int               `json:"caseCount"`
	CompletedCount int               `json:"completedCount"`
	PassedCount    int               `json:"passedCount"`
	FailedCount    int               `json:"failedCount"`
	SkippedCount   int               `json:"skippedCount"`
	Dimensions     []DimensionReport `json:"dimensions"`
	Cases          []CaseSummary     `json:"cases"`
	Hash           string            `json:"hash"`
}

type reportHashMaterial struct {
	RunID          string            `json:"runID"`
	DatasetHash    string            `json:"datasetHash"`
	PassThreshold  float64           `json:"passThreshold"`
	OverallScore   float64           `json:"overallScore"`
	Passed         bool              `json:"passed"`
	CaseCount      int               `json:"caseCount"`
	CompletedCount int               `json:"completedCount"`
	PassedCount    int               `json:"passedCount"`
	FailedCount    int               `json:"failedCount"`
	SkippedCount   int               `json:"skippedCount"`
	Dimensions     []DimensionReport `json:"dimensions"`
	Cases          []CaseSummary     `json:"cases"`
}

type weightedAccumulator struct {
	weightedTotal float64
	weightTotal   float64
	count         int
	min           float64
	max           float64
}

func buildReport(runID string, dataset Dataset, records []CaseRecord) (Report, error) {
	if strings.TrimSpace(runID) == "" || ValidateDataset(dataset) != nil || len(records) != len(dataset.Cases) {
		return Report{}, ErrInvalidExecution
	}
	dimensionReports := aggregateDimensions(dataset, records)
	caseSummaries := aggregateCases(dataset, records)
	report := Report{
		RunID: runID, DatasetHash: dataset.Hash, PassThreshold: dataset.PassThreshold,
		OverallScore: overallDimensionScore(dimensionReports),
		CaseCount:    len(records), Dimensions: dimensionReports, Cases: caseSummaries,
	}
	countReportCases(&report, caseSummaries)
	report.Passed = reportPasses(report)
	hash, err := reportHash(report)
	if err != nil {
		return Report{}, err
	}
	report.Hash = hash
	report.ID = stableEvaluationID("evalreport", runID, hash)
	return report, nil
}

func countReportCases(report *Report, caseSummaries []CaseSummary) {
	for _, summary := range caseSummaries {
		switch summary.Status {
		case CaseCompleted:
			report.CompletedCount++
			if summary.Passed {
				report.PassedCount++
			}
		case CaseFailed:
			report.FailedCount++
		case CaseSkipped:
			report.SkippedCount++
		}
	}
}

func reportPasses(report Report) bool {
	return report.CompletedCount > 0 && report.FailedCount == 0 &&
		report.OverallScore >= report.PassThreshold && report.PassedCount == report.CompletedCount
}

func aggregateDimensions(dataset Dataset, records []CaseRecord) []DimensionReport {
	result := make([]DimensionReport, 0, len(dataset.Dimensions))
	for _, dimension := range dataset.Dimensions {
		accumulator := weightedAccumulator{}
		for _, record := range records {
			if record.Status != CaseCompleted {
				continue
			}
			for _, score := range record.Scores {
				if score.Dimension == dimension.Name {
					accumulator.add(score.Value, score.Weight)
				}
			}
		}
		result = append(result, DimensionReport{
			Name: dimension.Name, Weight: dimension.Weight, Count: accumulator.count,
			Score: accumulator.mean(), Min: accumulator.minimum(), Max: accumulator.maximum(),
		})
	}
	return result
}

func aggregateCases(dataset Dataset, records []CaseRecord) []CaseSummary {
	result := make([]CaseSummary, 0, len(records))
	for _, record := range records {
		score := caseScore(dataset, record)
		result = append(result, CaseSummary{
			RecordID: record.ID, CaseID: record.CaseID, Status: record.Status,
			Score: score, Passed: record.Status == CaseCompleted && score >= dataset.PassThreshold,
		})
	}
	return result
}

func caseScore(dataset Dataset, record CaseRecord) float64 {
	if record.Status != CaseCompleted {
		return 0
	}
	dimensionScores := make(map[string]float64, len(dataset.Dimensions))
	for _, dimension := range dataset.Dimensions {
		accumulator := weightedAccumulator{}
		for _, score := range record.Scores {
			if score.Dimension == dimension.Name {
				accumulator.add(score.Value, score.Weight)
			}
		}
		if accumulator.count > 0 {
			dimensionScores[dimension.Name] = accumulator.mean()
		}
	}
	total := 0.0
	weights := 0.0
	for _, dimension := range dataset.Dimensions {
		value, ok := dimensionScores[dimension.Name]
		if !ok {
			continue
		}
		total += value * dimension.Weight
		weights += dimension.Weight
	}
	if weights == 0 {
		return 0
	}
	return total / weights
}

func overallDimensionScore(dimensions []DimensionReport) float64 {
	total := 0.0
	weights := 0.0
	for _, dimension := range dimensions {
		if dimension.Count == 0 {
			continue
		}
		total += dimension.Score * dimension.Weight
		weights += dimension.Weight
	}
	if weights == 0 {
		return 0
	}
	return total / weights
}

func (accumulator *weightedAccumulator) add(value float64, weight float64) {
	if accumulator.count == 0 {
		accumulator.min = value
		accumulator.max = value
	} else {
		if value < accumulator.min {
			accumulator.min = value
		}
		if value > accumulator.max {
			accumulator.max = value
		}
	}
	accumulator.weightedTotal += value * weight
	accumulator.weightTotal += weight
	accumulator.count++
}

func (accumulator weightedAccumulator) mean() float64 {
	if accumulator.weightTotal == 0 {
		return 0
	}
	return accumulator.weightedTotal / accumulator.weightTotal
}

func (accumulator weightedAccumulator) minimum() float64 {
	if accumulator.count == 0 {
		return 0
	}
	return accumulator.min
}

func (accumulator weightedAccumulator) maximum() float64 {
	if accumulator.count == 0 {
		return 0
	}
	return accumulator.max
}

func reportHash(report Report) (string, error) {
	material := reportHashMaterial{
		RunID: report.RunID, DatasetHash: report.DatasetHash, PassThreshold: report.PassThreshold,
		OverallScore: report.OverallScore, Passed: report.Passed,
		CaseCount: report.CaseCount, CompletedCount: report.CompletedCount, PassedCount: report.PassedCount,
		FailedCount: report.FailedCount, SkippedCount: report.SkippedCount,
		Dimensions: cloneDimensionReports(report.Dimensions), Cases: append([]CaseSummary(nil), report.Cases...),
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", errors.Join(ErrInvalidExecution, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func stableEvaluationID(prefix string, values ...string) string {
	joined := strings.Join(values, "\x1f")
	sum := sha256.Sum256([]byte(joined))
	return prefix + "_" + hex.EncodeToString(sum[:])[:32]
}

func cloneDimensionReports(values []DimensionReport) []DimensionReport {
	return append([]DimensionReport(nil), values...)
}
