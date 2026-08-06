package context_test

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	runtimectx "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
)

const (
	testCurrentTurnID  = "turn-current"
	testCallingLookup  = "Calling lookup"
	testToolNameLookup = "lookup"
	testResultID       = "result"
	testSystemID       = "system"
	testTurn2          = "turn-2"
)

func TestBuilderProducesStableCanonicalSnapshot(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	firstRequest := baseRequest()
	firstRequest.Prompt.Tools = []runtimectx.ToolDefinition{
		{Key: "zeta", Description: "Z", Schema: json.RawMessage(`{ "type": "object", "properties": {"b": {"type":"string"}, "a": {"type":"number"}}}`)},
		{Key: "alpha", Description: "A", Schema: json.RawMessage(`{"type":"object"}`)},
	}
	firstRequest.Prompt.Options = json.RawMessage(`{"temperature":0,"response":{"format":"json"}}`)
	secondRequest := firstRequest
	secondRequest.Prompt.Tools = []runtimectx.ToolDefinition{
		{Key: "alpha", Description: "A", Schema: json.RawMessage(`{ "type" : "object" }`)},
		{Key: "zeta", Description: "Z", Schema: json.RawMessage(`{"properties":{"a":{"type":"number"},"b":{"type":"string"}},"type":"object"}`)},
	}
	secondRequest.Prompt.Options = json.RawMessage(`{"response":{"format":"json"},"temperature":0}`)

	first, err := builder.Build(stdcontext.Background(), firstRequest)
	if err != nil {
		t.Fatalf("build first snapshot: %v", err)
	}
	second, err := builder.Build(stdcontext.Background(), secondRequest)
	if err != nil {
		t.Fatalf("build second snapshot: %v", err)
	}
	if first.Snapshot.ID != second.Snapshot.ID || first.Snapshot.ContentHash != second.Snapshot.ContentHash ||
		string(first.Snapshot.Content) != string(second.Snapshot.Content) {
		t.Fatalf("canonical snapshots differ:\nfirst=%#v\nsecond=%#v", first.Snapshot, second.Snapshot)
	}
	if first.Snapshot.Trace.Raw.TokenCountSource != runtimectx.CountEstimated ||
		first.Snapshot.Trace.Raw.AdjustedTokenEstimate <= first.Snapshot.Trace.Raw.RawTokenEstimate {
		t.Fatalf("expected estimated count with safety margin: %#v", first.Snapshot.Trace.Raw)
	}
}

func TestBuilderRejectsSplitToolPair(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	request := baseRequest()
	request.Prompt.Items = []runtimectx.Item{
		message("current", testCurrentTurnID, runtimectx.RoleUser, "Run lookup"),
		{
			ID: "call", TurnID: testCurrentTurnID, Kind: runtimectx.ItemToolCall,
			Role: runtimectx.RoleAssistant, Content: testCallingLookup,
			ToolCallID: "call-split", ToolName: testToolNameLookup,
		},
		{
			ID: testResultID, TurnID: "turn-other", Kind: runtimectx.ItemToolResult,
			Role: runtimectx.RoleTool, Content: testResultID,
			ToolCallID: "call-split", ToolName: testToolNameLookup,
		},
	}
	if _, err := builder.Build(stdcontext.Background(), request); !errors.Is(err, runtimectx.ErrInvalidInput) {
		t.Fatalf("expected split tool pair rejection, got %v", err)
	}
}

func TestBuilderUsesExactCounterAndMinimumHardBudget(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{Counter: exactCounter{tokens: 42}})
	request := baseRequest()
	request.Budget.MaxInputTokens = 1_000
	request.Budget.EffectiveModelTokens = 800
	result, err := builder.Build(stdcontext.Background(), request)
	if err != nil {
		t.Fatalf("build exact-count snapshot: %v", err)
	}
	assessment := result.Snapshot.Trace.Managed
	if assessment.TokenCountSource != runtimectx.CountExact || assessment.RawTokenEstimate != 42 ||
		assessment.AdjustedTokenEstimate != 42 || assessment.HardInputTokens != 800 {
		t.Fatalf("unexpected exact assessment: %#v", assessment)
	}
}

func TestBuilderFailsWhenProtectedContextExceedsBudget(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	request := baseRequest()
	request.Prompt.Items = []runtimectx.Item{
		{
			ID: testSystemID, TurnID: testSystemID, Kind: runtimectx.ItemMessage, Role: runtimectx.RoleSystem,
			Content: repeated("required instruction ", 80), Required: true,
		},
		{
			ID: "current", TurnID: testCurrentTurnID, Kind: runtimectx.ItemMessage, Role: runtimectx.RoleUser,
			Content: repeated("current user input ", 80),
		},
	}
	request.Budget.MaxInputTokens = 64
	request.Budget.EffectiveModelTokens = 64
	request.Budget.PreserveRecentTurns = 1
	_, err := builder.Build(stdcontext.Background(), request)
	if !errors.Is(err, runtimectx.ErrBudgetExceeded) {
		t.Fatalf("expected budget exceeded, got %v", err)
	}
}

func TestBuilderIsSafeForConcurrentReuse(t *testing.T) {
	t.Parallel()
	builder := runtimectx.NewBuilder(runtimectx.Dependencies{})
	request := baseRequest()
	const workers = 16
	results := make(chan runtimectx.BuildResult, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := builder.Build(stdcontext.Background(), request)
			results <- result
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent build failed: %v", err)
		}
	}
	var snapshotID string
	for result := range results {
		if snapshotID == "" {
			snapshotID = result.Snapshot.ID
			continue
		}
		if result.Snapshot.ID != snapshotID {
			t.Fatalf("concurrent build produced unstable ID: %s != %s", result.Snapshot.ID, snapshotID)
		}
	}
}

func baseRequest() runtimectx.BuildRequest {
	return runtimectx.BuildRequest{
		RunID: "run_1", Revision: 2, SupersedesSnapshotID: "ctxs_previous",
		ThreadPathHash: "thread-path-hash", CurrentTurnID: testCurrentTurnID,
		Prompt: runtimectx.Prompt{
			Instructions: "Answer accurately.",
			Items: []runtimectx.Item{
				{
					ID: testSystemID, TurnID: testSystemID, Kind: runtimectx.ItemMessage,
					Role: runtimectx.RoleSystem, Content: "System policy", Required: true,
				},
				{
					ID: "current", TurnID: testCurrentTurnID, Kind: runtimectx.ItemMessage,
					Role: runtimectx.RoleUser, Content: "Current request",
				},
			},
			Options: json.RawMessage(`{}`),
		},
		Budget: runtimectx.Budget{
			MaxInputTokens: 4_096, EffectiveModelTokens: 8_192,
			MaxSerializedBytes: 1 << 20, PreserveRecentTurns: 2,
			MaxSummaryTokens: 256, MaxToolResultBytes: 512,
		},
	}
}

func repeated(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}

type exactCounter struct {
	tokens int64
}

func (counter exactCounter) Count(stdcontext.Context, []byte) (int64, error) {
	return counter.tokens, nil
}
