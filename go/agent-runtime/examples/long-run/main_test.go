package main

import (
	"context"
	"testing"
	"time"
)

func TestLongRunMeasurementUsesActualFencedWritesAndKnownReceiptRecovery(t *testing.T) {
	for _, replay := range []int{0, 2048} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		result, err := measure(ctx, settings{steps: 6, payloadBytes: 256, replayBytes: replay})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if result.ToolExecutions != 6 || result.ModelCalls != 7 || result.StateWrites < 6 || result.FinalStateBytes <= 0 ||
			result.SubmittedStateBytes <= int64(result.FinalStateBytes) || result.MaxStateBytes < result.FinalStateBytes ||
			!result.CommittedCallsNotReplayed || result.ConsumedRequestRetained != 0 || !result.FirstModelAfterResume || result.DatabaseWALMeasured || !result.FixtureOnly {
			t.Fatalf("measurement does not describe actual accepted work: %+v", result)
		}
	}
}

func TestLongRunMeasurementRejectsUnboundedConfiguration(t *testing.T) {
	for _, config := range []settings{{steps: 1}, {steps: 501}, {steps: 2, payloadBytes: -1}, {steps: 2, replayBytes: 65537}, {steps: 500, payloadBytes: 65536}} {
		if _, err := measure(t.Context(), config); err == nil {
			t.Fatalf("invalid fixture accepted: %+v", config)
		}
	}
}
