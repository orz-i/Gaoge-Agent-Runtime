package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Reports contain numeric receipts only; provider metadata strings and response
// contents never enter the baseline event stream.
type tokenUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

type usageCoverage struct {
	KnownCalls   int         `json:"knownCalls"`
	MissingCalls int         `json:"missingCalls"`
	Complete     bool        `json:"complete"`
	Known        *tokenUsage `json:"known"`
}

func numericUsage(value *model.Usage) *tokenUsage {
	if value == nil {
		return nil
	}
	values := []int64{value.InputTokens, value.OutputTokens, value.CacheReadTokens, value.CacheWriteTokens, value.ReasoningTokens}
	for _, count := range values {
		// Bound untrusted provider numbers; at most 480 task calls are admitted.
		// A malformed receipt is unknown, never a negative or overflowed total.
		if count < 0 || count > 1<<50 {
			return nil
		}
	}
	return &tokenUsage{value.InputTokens, value.OutputTokens, value.CacheReadTokens, value.CacheWriteTokens, value.ReasoningTokens}
}

func (usage *usageCoverage) add(receipt *tokenUsage) {
	if receipt == nil {
		usage.MissingCalls++
		usage.Complete = false
		return
	}
	usage.KnownCalls++
	if usage.Known == nil {
		usage.Known = &tokenUsage{}
	}
	usage.Known.InputTokens += receipt.InputTokens
	usage.Known.OutputTokens += receipt.OutputTokens
	usage.Known.CacheReadTokens += receipt.CacheReadTokens
	usage.Known.CacheWriteTokens += receipt.CacheWriteTokens
	usage.Known.ReasoningTokens += receipt.ReasoningTokens
	usage.Complete = usage.MissingCalls == 0
}

func (usage *usageCoverage) merge(other usageCoverage) {
	if other.Known != nil {
		usage.add(other.Known)
		usage.KnownCalls += other.KnownCalls - 1
	}
	usage.MissingCalls += other.MissingCalls
	usage.Complete = usage.MissingCalls == 0
}

// A count attempt is the Host's call to an optional native counter, not
// proof of a physical HTTP request, a generation, or a billed token. An
// unavailable result may lead to Context's conservative byte fallback; only
// the Context manager itself makes that admission decision.
// No upstream messages, endpoints, source text or credentials are retained.
// A local rejection proves no native count HTTP was attempted. An HTTP class
// proves a native count response status was seen, not billable generation.
type inputCountUnavailableReasons struct {
	LocalRejected       int `json:"localRejected"`
	HTTP4xx             int `json:"http4xx"`
	HTTP5xx             int `json:"http5xx"`
	OtherHTTP           int `json:"otherHTTP"`
	TransportOrProtocol int `json:"transportOrProtocol"`
	Other               int `json:"other"`
}

type inputCountObservations struct {
	Attempts           int                           `json:"attempts"`
	Accepted           int                           `json:"accepted"`
	Unavailable        int                           `json:"unavailable"`
	UnavailableReasons *inputCountUnavailableReasons `json:"unavailableReasons,omitempty"`
	// Count-side wall time, including local rejections. This is not a
	// physical HTTP duration or model generation time, and is not additive
	// to task wall time under concurrent requests. Historical reports omit it.
	TotalDurationMs int64 `json:"totalDurationMs"`
	MaxDurationMs   int64 `json:"maxDurationMs"`
}

// A count-to-usage comparison is valid only for the exact model input
// byte identity and a successful model response with known Provider Usage.
// It is never an admission decision, a generation request or billing proof.
type inputCountUsageComparison struct {
	Compared         int   `json:"compared"`
	Equal            int   `json:"equal"`
	BelowUsage       int   `json:"belowUsage"`
	AboveUsage       int   `json:"aboveUsage"`
	MaxUnderestimate int64 `json:"maxUnderestimate"`
}

// Content-free aggregate of actual successful Artifact handler outputs and
// validated model Tool arguments; query text, Artifact IDs and previews never
// leave this process. Optional on the quality suite only.
type artifactReadStrategy struct {
	Queried             int `json:"queried"`
	OffsetOnly          int `json:"offsetOnly"`
	CandidatePages      int `json:"candidatePages"`
	NormalizedHintPages int `json:"normalizedHintPages"`
	HintedOffsetPages   int `json:"hintedOffsetPages"`
	DonePages           int `json:"donePages"`
}

type modelObservation struct {
	DurationMs int64
	Usage      *tokenUsage
}

type toolExecution struct {
	key, callID string
	committed   bool
}

type toolExecutionMetric struct {
	ToolKey    string `json:"toolKey"`
	CallIDHash string `json:"callIDHash"`
	Executions int    `json:"executions"`
}

// Recording occurs inside the registered handler, after the SDK's authority and
// argument checks. A model-selected call ID is stored only as a digest in reports.
type recordingHandler struct {
	state   *taskState
	handler tools.Handler
}

func (handler recordingHandler) Execute(ctx context.Context, request tools.ExecutionRequest) (result tools.ExecutionResult, err error) {
	defer func() {
		handler.state.mu.Lock()
		handler.state.executions = append(handler.state.executions, toolExecution{
			key: request.Call.ToolKey, callID: request.Call.ID,
			committed: err == nil && result.Receipt.ExecutionID != "" && result.Receipt.Disposition != tools.ReceiptDispositionPending,
		})
		handler.state.mu.Unlock()
	}()
	return handler.handler.Execute(ctx, request)
}

func (state *taskState) committedExecutions() map[string]int {
	state.mu.Lock()
	defer state.mu.Unlock()
	result := make(map[string]int)
	for _, execution := range state.executions {
		if execution.committed {
			result[execution.key+"\x00"+execution.callID]++
		}
	}
	return result
}

func unchangedCommitted(before, after map[string]int) bool {
	for key, count := range before {
		if after[key] != count {
			return false
		}
	}
	return true
}

func (state *taskState) collectMetrics(report *taskReport) {
	state.mu.Lock()
	defer state.mu.Unlock()
	report.ModelCalls = len(state.observations)
	report.ToolCalls = len(state.executions)
	report.LookupCalls, report.ArtifactReads, report.DraftWrites = state.lookupCalls, len(state.reads), len(state.drafts)
	if state.suite == qualitySuite {
		modes := &artifactReadStrategy{}
		for _, page := range state.reads {
			if page.queried {
				modes.Queried++
			} else {
				modes.OffsetOnly++
				if page.SearchHint != "" {
					modes.HintedOffsetPages++
				}
			}
			if len(page.Matches) != 0 {
				modes.CandidatePages++
			}
			if page.SearchMode == "normalized_hints" {
				modes.NormalizedHintPages++
			}
			if page.Done {
				modes.DonePages++
			}
		}
		report.ArtifactReadStrategy = modes
	}
	if state.inputCounts.Attempts > 0 {
		observed := state.inputCounts
		report.InputCountObservations = &observed
		comparison := state.inputCountComparison
		report.InputCountComparison = &comparison
	}
	if report.Stage == "start" && state.lookupCalls > 0 {
		report.Stage = "evidence"
	}
	report.Usage = usageCoverage{Complete: true}
	for _, observation := range state.observations {
		report.Usage.add(observation.Usage)
		report.ModelDurationsMs = append(report.ModelDurationsMs, observation.DurationMs)
	}
	byKey := make(map[string]int)
	for _, execution := range state.executions {
		identity := execution.key + "\x00" + execution.callID
		index, seen := byKey[identity]
		if !seen {
			byKey[identity] = len(report.ToolExecutions)
			report.ToolExecutions = append(report.ToolExecutions, toolExecutionMetric{
				ToolKey: execution.key, CallIDHash: digest([]byte(identity)), Executions: 1,
			})
		} else {
			report.ToolExecutions[index].Executions++
		}
	}
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type durationStats struct {
	Count int   `json:"count"`
	Total int64 `json:"total"`
	Min   int64 `json:"min"`
	Max   int64 `json:"max"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
}

func durations(values []int64) durationStats {
	if len(values) == 0 {
		return durationStats{}
	}
	values = append([]int64(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	result := durationStats{Count: len(values), Min: values[0], Max: values[len(values)-1]}
	for _, value := range values {
		result.Total += value
	}
	result.P50 = values[(50*len(values)+99)/100-1]
	result.P95 = values[(95*len(values)+99)/100-1]
	return result
}
