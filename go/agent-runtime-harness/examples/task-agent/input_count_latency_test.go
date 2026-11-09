package main

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type delayedEvidenceCounter struct{ fail bool }

func (counter delayedEvidenceCounter) Count(_ context.Context, _ runtimecontext.TokenCountRequest) (int64, error) {
	time.Sleep(3 * time.Millisecond)
	if counter.fail {
		return 0, errors.New("opaque upstream failure")
	}
	return 9, nil
}

func TestNativeInputCountLatencyIsContentFreeAndIncludesUnavailablePaths(t *testing.T) {
	state := &taskState{suite: qualitySuite}
	request := model.Request{Model: "openai/test", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("private source")}}}
	payload, err := model.MarshalRequestForInputCount(request)
	if err != nil {
		t.Fatal(err)
	}
	input := runtimecontext.TokenCountRequest{Payload: payload, Context: runtimecontext.TokenCountContext{Protocol: "openai", Model: "test"}}
	positive := recordedInputTokenCounter{next: delayedEvidenceCounter{}, state: state}
	if result, err := positive.Count(t.Context(), input); err != nil || result != 9 {
		t.Fatalf("positive native count failed: %d %v", result, err)
	}
	negative := recordedInputTokenCounter{next: delayedEvidenceCounter{fail: true}, state: state}
	if result, err := negative.Count(t.Context(), input); err == nil || result != 0 {
		t.Fatalf("failed count was treated as accepted: %d %v", result, err)
	}
	counts := state.inputCounts
	if counts.Attempts != 2 || counts.Accepted != 1 || counts.Unavailable != 1 ||
		counts.TotalDurationMs < 4 || counts.MaxDurationMs < 2 ||
		counts.TotalDurationMs < counts.MaxDurationMs {
		t.Fatalf("count latency does not include both paths: %+v", counts)
	}
	if counts.UnavailableReasons == nil || counts.UnavailableReasons.Other != 1 {
		t.Fatalf("failure classification lost: %+v", counts)
	}
}
