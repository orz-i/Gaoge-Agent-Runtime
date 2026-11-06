package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type observedEffortScripted struct {
	scriptedModel // Preserve the fixture model\u0027s strict-schema capability provider.
	seen          *[][]byte
}

func (m observedEffortScripted) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	*m.seen = append(*m.seen, append([]byte(nil), request.ModelOptions...))
	return scriptedModel{}.Generate(ctx, request)
}

func TestBaselineEffortValidInputAndFrozenAcrossRecovery(t *testing.T) {
	configuration := unitBaselineInput()
	configuration.ReasoningEffort = "medium"
	raw, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	input, public, err := readBaselineInput(bytes.NewReader(raw))
	if err != nil || input.ReasoningEffort != "medium" || public.ReasoningEffort != "medium" {
		t.Fatalf("real baseline input lost effort: %+v %+v %v", input, public, err)
	}
	defaultStart, err := baselineStart(publicConfiguration{Provider: "openai", Model: "gpt-4.1", ContextTokens: 16384, MaxOutputTokens: 512, Repetitions: 1})
	if err != nil {
		t.Fatal(err)
	}
	effortStart, err := baselineStart(publicConfiguration{Provider: "openai", Model: "gpt-4.1", ContextTokens: 16384, MaxOutputTokens: 512, Repetitions: 1, ReasoningEffort: "medium"})
	if err != nil || defaultStart.DatasetHash != effortStart.DatasetHash {
		t.Fatalf("different effort must retain identical workload identity: %+v %+v %v", defaultStart, effortStart, err)
	}

	config := scriptedSettings(t)
	config.reasoningEffort = "medium"
	var seen [][]byte
	result, err := runTask(t.Context(), config, func() (model.Client, error) {
		return observedEffortScripted{seen: &seen}, nil
	}, defaultSource())
	if err != nil || result.Status != "passed" || !result.RecoveryChecks.CommittedToolsNotReplayed {
		t.Fatalf("scripted task/recovery regressed after freezing effort: %+v %v", result, err)
	}
	if len(seen) != result.ModelCalls || len(seen) < 3 {
		t.Fatalf("unexpected model call count: %d %+v", len(seen), result)
	}
	for i, options := range seen {
		if !bytes.Equal(options, config.modelOptions()) {
			t.Fatalf("model step %d lost exact frozen effort: %s", i, options)
		}
	}
}

func TestBaselineInvalidEffortCannotSendPhysicalHTTP(t *testing.T) {
	for _, tc := range []struct {
		provider, modelID, effort string
		invalidInput              bool
	}{
		{"openai", "gpt-4.1", "high", false}, // strict JSON is supported, reasoning effort is not
		{"openai", "private-unsupported", "high", false},
		{"anthropic", "claude-sonnet-4-5", "high", false},
		{"anthropic", "claude-sonnet-5-5", "none", true},
		{"openai", "gpt-6-sol", "unknown", true},
		{"openai", "gpt-6-sol", "", false}, // omitted effort is valid, but below use unsupported model
	} {
		t.Run(tc.provider+"-"+tc.modelID+"-"+tc.effort, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()
			modelID := tc.modelID
			if tc.effort == "" {
				modelID = "private-unsupported"
			}
			input := baselineHTTPInput(tc.provider, server.URL+"/never-dispatch", false)
			input["model"] = modelID
			if tc.effort != "" {
				input["reasoningEffort"] = tc.effort
			}
			code, events, _ := executeHTTPBaseline(t, input)
			if code == 0 || requests.Load() != 0 {
				t.Fatalf("unsafe requested effort reached provider: code=%d calls=%d", code, requests.Load())
			}
			if tc.invalidInput {
				if !reflect.DeepEqual(events[0]["type"], "error") || events[0]["failureCode"] != "invalid_configuration" {
					t.Fatalf("invalid effort not rejected at config gate: %+v", events)
				}
			} else {
				preflights := baselineEventsOfType(events, "preflight")
				if len(preflights) != 1 || preflights[0]["failureCode"] != "capability_unavailable" {
					t.Fatalf("unknown/unsupported capability was dispatched: %+v", events)
				}
			}
		})
	}
}
