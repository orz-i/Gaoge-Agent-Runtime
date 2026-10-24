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
