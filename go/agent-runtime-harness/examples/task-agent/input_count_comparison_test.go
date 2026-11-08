package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The identity used to pair an accepted native count to a subsequent
// Provider Usage receipt is independent of the output-only token ceiling,
// but not of message, Tool, model or signed replay content.
func TestInputCountUsageComparisonOnlyForExactlyMatchedSuccessfulRequests(t *testing.T) {
	state := &taskState{suite: qualitySuite}
	base := model.Request{
		RunID: "run", Model: "openai/test",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("private deterministic source 甲")}},
	}
	fp, err := inputCountObservationFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	state.acceptedInputCounts = map[[32]byte]int64{fp: 42}
	for _, tc := range []struct {
		name                            string
		request                         model.Request
		usage                           int64
		err                             error
		wantCompared                    int
		wantEqual, wantBelow, wantAbove int
		maxUnder                        int64
	}{
		{"exact", base, 42, nil, 1, 1, 0, 0, 0},
		{"identical-input-without-fresh-count", base, 42, nil, 1, 1, 0, 0, 0},
		{"under", base, 49, nil, 2, 1, 1, 0, 7},
		{"over", base, 38, nil, 3, 1, 1, 1, 7},
		{"under-smaller-delta", base, 45, nil, 4, 1, 2, 1, 7},
		{"missing-provider-usage", base, 0, nil, 4, 1, 2, 1, 7},
		{"repeat-after-missing-usage", base, 42, nil, 4, 1, 2, 1, 7},
		{"failed-model", base, 42, errors.New("untrusted"), 4, 1, 2, 1, 7},
		{"repeat-after-failed-model", base, 42, nil, 4, 1, 2, 1, 7},
		{"other-model", func() model.Request { r := model.CloneRequest(base); r.Model = "openai/other"; return r }(), 42, nil, 4, 1, 2, 1, 7},
		{"changed-source", func() model.Request {
			r := model.CloneRequest(base)
			r.Messages[0].Content = model.TextParts("different")
			return r
		}(), 42, nil, 4, 1, 2, 1, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.name {
			case "under", "over", "under-smaller-delta", "missing-provider-usage", "failed-model":
				// Each counted operation requires its own freshly
				// accepted native result. A second identical Model
				// invocation cannot reuse the previous count receipt.
				state.acceptedInputCounts[fp] = 42
			}
			var receipt *tokenUsage
			if tc.usage > 0 {
				receipt = &tokenUsage{InputTokens: tc.usage}
			}
			request := model.CloneRequest(tc.request)
			request.MaxOutputTokens = 128000
			state.recordInputCountUsageComparison(request, receipt, tc.err)
			result := state.inputCountComparison
			if result.Compared != tc.wantCompared || result.Equal != tc.wantEqual ||
				result.BelowUsage != tc.wantBelow || result.AboveUsage != tc.wantAbove ||
				result.MaxUnderestimate != tc.maxUnder {
				t.Fatalf("comparison accepted missing/different request: %+v", result)
			}
		})
	}
	if len(state.acceptedInputCounts) != 0 {
		t.Fatalf("native counts remained usable after attempted Model call: %d", len(state.acceptedInputCounts))
	}
}

// Host Run identity and output ceiling are not part of Provider-native input,
// whereas messages, Tool definitions/arguments, output format and effort are.
// This property test rejects cross-request correlation without persisting
// request fingerprints or relying on an untrusted completion ID.
func TestInputCountFingerprintExcludesOnlyNonInputHostMetadata(t *testing.T) {
	base := model.Request{
		RunID: "old-run", InvocationID: "old-invocation", Model: "openai/native",
		ModelOptions: json.RawMessage(`{"reasoning":{"effort":"high"}}`),
		OutputContract: &model.OutputContract{
			Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
		},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: model.TextParts("proof")},
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
				ID: "call-1", ToolKey: "lookup", Name: "lookup", Arguments: json.RawMessage(`{"z":1,"a":2}`),
			}}},
		},
		Tools: []tools.Definition{{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	original, err := inputCountObservationFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	hostOnly := model.CloneRequest(base)
	hostOnly.RunID = "other"
	hostOnly.InvocationID = "other"
	hostOnly.MaxOutputTokens = 128000
	unchanged, err := inputCountObservationFingerprint(hostOnly)
	if err != nil || unchanged != original {
		t.Fatal("host identity/output-only ceiling changed exact Provider input identity")
	}
	cases := map[string]func(*model.Request){
		"model":  func(r *model.Request) { r.Model = "openai/other" },
		"effort": func(r *model.Request) { r.ModelOptions = json.RawMessage(`{"reasoning":{"effort":"low"}}`) },
		"output-schema": func(r *model.Request) {
			r.OutputContract.Schema = json.RawMessage(`{"type":"object","properties":{"different":{"type":"string"}}}`)
		},
		"user-content":               func(r *model.Request) { r.Messages[0].Content = model.TextParts("different") },
		"tool-arguments":             func(r *model.Request) { r.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"z":9,"a":2}`) },
		"tool-argument-signed-order": func(r *model.Request) { r.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"a":2,"z":1}`) },
		"tool-schema":                func(r *model.Request) { r.Tools[0].InputSchema = json.RawMessage(`{"type":"object","required":["x"]}`) },
		"tool-choice":                func(r *model.Request) { r.RequireToolCall = true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			other := model.CloneRequest(base)
			change(&other)
			fingerprint, err := inputCountObservationFingerprint(other)
			if err != nil || fingerprint == original {
				t.Fatalf("changed Provider input was attributed to a different count: err=%v", err)
			}
		})
	}
}

func TestInputCountComparisonIsPassiveAcrossModelOutputCeiling(t *testing.T) {
	state := &taskState{suite: qualitySuite}
	request := model.Request{RunID: "run", Model: "openai/local",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("compacted source")}}}
	fp, err := inputCountObservationFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	state.acceptedInputCounts = map[[32]byte]int64{fp: 97}
	var physicallySent model.Request
	calls := 0
	next := plugin.ModelNext(func(_ context.Context, r model.Request, _ model.StreamSink) (model.Response, error) {
		calls++
		physicallySent = model.CloneRequest(r)
		return model.Response{Usage: &model.Usage{InputTokens: 97, OutputTokens: 5}}, nil
	})
	_, err = (outputCeiling{tokens: 128000, state: state}).Model(t.Context(), request, nil, next)
	if err != nil || calls != 1 || physicallySent.MaxOutputTokens != 128000 || state.inputCountComparison.Equal != 1 ||
		len(state.observations) != 1 || state.observations[0].Usage.InputTokens != 97 {
		t.Fatalf("diagnostic comparison changed physical model request/outcome: calls=%d comparison=%+v err=%v",
			calls, state.inputCountComparison, err)
	}
}
