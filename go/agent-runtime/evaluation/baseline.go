package evaluation

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var ErrInvalidBaseline = errors.New("invalid evaluation baseline")

// Baseline freezes regression thresholds for one exact Dataset revision.
type Baseline struct {
	DatasetID           string  `json:"datasetID"`
	DatasetHash         string  `json:"datasetHash"`
	MinimumOverallScore float64 `json:"minimumOverallScore"`
	MinimumPassedCases  int     `json:"minimumPassedCases"`
	MaximumFailedCases  int     `json:"maximumFailedCases"`
	RequireReportPass   bool    `json:"requireReportPass"`
}

// BaselineComparison is a deterministic regression decision with stable reasons.
type BaselineComparison struct {
	Passed      bool     `json:"passed"`
	Regressions []string `json:"regressions,omitempty"`
}

// CompareBaseline compares one RunRecord against thresholds pinned to its Dataset hash.
func CompareBaseline(record RunRecord, baseline Baseline) (BaselineComparison, error) {
	baseline.DatasetID = strings.TrimSpace(baseline.DatasetID)
	baseline.DatasetHash = strings.TrimSpace(baseline.DatasetHash)
	if baseline.DatasetID == "" || baseline.DatasetHash == "" ||
		baseline.MinimumOverallScore < 0 || baseline.MinimumOverallScore > 1 ||
		math.IsNaN(baseline.MinimumOverallScore) || math.IsInf(baseline.MinimumOverallScore, 0) ||
		baseline.MinimumPassedCases < 0 || baseline.MaximumFailedCases < 0 {
		return BaselineComparison{}, ErrInvalidBaseline
	}
	if record.DatasetID != baseline.DatasetID || record.DatasetHash != baseline.DatasetHash ||
		record.Report.DatasetHash != baseline.DatasetHash {
		return BaselineComparison{}, ErrInvalidBaseline
	}
	regressions := make([]string, 0, 4)
	if record.Report.OverallScore < baseline.MinimumOverallScore {
		regressions = append(regressions, fmt.Sprintf(
			"overall score %.4f below baseline %.4f",
			record.Report.OverallScore, baseline.MinimumOverallScore,
		))
	}
	if record.Report.PassedCount < baseline.MinimumPassedCases {
		regressions = append(regressions, fmt.Sprintf(
			"passed cases %d below baseline %d",
			record.Report.PassedCount, baseline.MinimumPassedCases,
		))
	}
	if record.Report.FailedCount > baseline.MaximumFailedCases {
		regressions = append(regressions, fmt.Sprintf(
			"failed cases %d above baseline %d",
			record.Report.FailedCount, baseline.MaximumFailedCases,
		))
	}
	if baseline.RequireReportPass && !record.Report.Passed {
		regressions = append(regressions, "report no longer passes its dataset threshold")
	}
	return BaselineComparison{Passed: len(regressions) == 0, Regressions: regressions}, nil
}
